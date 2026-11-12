//! The Slack and mail gateways, inside the server (the plan's section 4.6).
//!
//! A gateway is an ordinary member of a team with one session, which people outside the team reach by Slack
//! or by mail and which carries a session's message out to them. It acts only through the engine's request
//! path, with the credential the server issued it when it joined, so every rule and every limit applies to
//! it as to any member. What it says and how it decides is `brigade-gateway`, which does no I/O; this module
//! is the I/O: the state, the configuration, the calls to Slack and to a mail provider, the endpoints and
//! the loop.
//!
//! The four handlers are the Edge Functions' four, call for call and in their order: [`slack_in`] and
//! [`mail_in`] answer a request on the server's own listener ([`endpoint`]), and [`slack_out`] and
//! [`mail_out`] are one gateway's turn of the loop ([`turns`]). What they share is here: what the operator
//! configured, the state, the way out and the way to the engine.

pub mod client;
pub mod config;
pub mod endpoint;
pub mod mail_in;
pub mod mail_out;
pub mod member;
pub mod presence;
pub mod refusals;
pub mod served;
pub mod slack_in;
pub mod slack_out;
pub mod state;
pub mod threads;
pub mod turns;

#[cfg(test)]
#[path = "../../tests/common/standin.rs"]
pub mod standin;
#[cfg(test)]
pub mod testing;

use std::collections::HashSet;
use std::path::Path;
use std::sync::Arc;

use brigade_core::Timestamp;
use brigade_core::wire::JoinSecret;
use brigade_gateway::connector::{Provider, SendError};
use brigade_gateway::notes::slack as ns;
use brigade_gateway::providers::{Postmark, Resend, Slack};
use brigade_gateway::{HttpRequest, OutboundMail};
use tokio::sync::{Mutex, OwnedSemaphorePermit, Semaphore};

use self::client::{Outbound, Reply};
use self::config::{MailProvider, Secret};
use self::refusals::Refusals;
use self::served::Served;
use self::state::{Kind, Store};
use crate::engine::Client;
use crate::note::note;

/// How many requests to the gateways' endpoints are answered at once. A request is answered in a task of
/// its own, which lasts longer than its connection when the caller hangs up, so the connections' places do
/// not bound them: these do.
const REQUESTS_AT_ONCE: u32 = 64;

/// One team a gateway serves, as the operator configured it.
#[derive(Clone)]
pub struct Team {
    /// Which gateway serves it.
    pub kind: Kind,
    /// The team, as its join secret names it.
    pub team_ref: String,
    /// The team's join secret: what makes the gateway a member.
    pub join_secret: Secret,
    /// For mail, the address the provider receives the team's mail at; empty for Slack, like the three
    /// members below.
    pub inbox: String,
    /// The address people are told to write to.
    pub public_address: String,
    /// The From of every mail the gateway sends for the team.
    pub from: String,
    /// Who may write in by mail; empty admits anyone.
    pub allowed_senders: Vec<String>,
}

impl Team {
    /// The team a join secret names. `member` is the configuration's member that names the secret's file,
    /// and is all a refusal says of it: never the secret, and never the path.
    fn new(kind: Kind, join_secret: &Secret, member: &str) -> Result<Self, String> {
        let team_ref = JoinSecret::parse(join_secret.reveal())
            .map_err(|_| format!("the file {member} names does not hold a join secret"))?
            .team_ref()
            .to_owned();
        Ok(Self {
            kind,
            team_ref,
            join_secret: join_secret.clone(),
            inbox: String::new(),
            public_address: String::new(),
            from: String::new(),
            allowed_senders: Vec::new(),
        })
    }
}

/// The Slack app every Slack gateway of the server speaks through.
pub struct SlackSide {
    /// The bot at Slack's Web API: its token, and where the API is.
    pub api: Slack,
    /// What a request from Slack is checked with.
    signing_secret: Secret,
    /// The bot's handle, as people write it after an `@`.
    pub bot_handle: String,
    /// The workspace's host, for the roster's line.
    pub workspace: String,
    /// The bot's own user id, once `auth.test` has said it: the Edge Function learned it once an instance.
    bot_user: std::sync::Mutex<Option<String>>,
}

impl SlackSide {
    /// The app's signing secret, for the one place that checks a request with it.
    #[must_use]
    pub fn signing_secret(&self) -> &str {
        self.signing_secret.reveal()
    }

    /// The bot's own user id, asked of Slack until Slack has answered it once. `None` while Slack does not
    /// say: a channel message is then ignored, and a direct message is carried all the same.
    pub async fn bot_user(&self, gateways: &Gateways) -> Option<String> {
        let known = |id: &Option<String>| id.clone().filter(|id| !id.is_empty());
        if let Some(id) = self.bot_user.lock().ok().and_then(|id| known(&id)) {
            return Some(id);
        }
        let asked = gateways.asked(&self.api.auth_test_request()).await.ok()?;
        let id = Slack::auth_test_answer(asked.status, &asked.body).ok()?.user_id;
        if let Ok(mut kept) = self.bot_user.lock() {
            *kept = Some(id.clone());
        }
        Some(id)
    }
}

/// The mail provider every mail gateway of the server speaks through.
pub struct MailSide {
    /// The provider's rules: its webhook, its fetch and its send.
    pub provider: Box<dyn Provider + Send + Sync>,
    /// Which provider it is.
    kind: MailProvider,
}

impl MailSide {
    /// Whether what comes before a webhook's body already says whether the webhook is the provider's.
    /// Postmark's secret arrives in a header or in the path, so a webhook without it is refused before any
    /// of its body is read. Resend signs the body, so its body has to be read first.
    #[must_use]
    pub fn checked_before_body(&self) -> bool {
        self.kind == MailProvider::Postmark
    }

    /// The most of a webhook's body that is read. Postmark's carries the whole mail, and is read only once
    /// the webhook is known to be Postmark's; Resend's carries the mail's id and its envelope, and nothing
    /// says whose it is until it is read.
    #[must_use]
    pub fn most(&self) -> usize {
        match self.kind {
            MailProvider::Postmark => 10 << 20,
            MailProvider::Resend => 1 << 20,
        }
    }
}

/// What the handlers and the loop share.
pub struct Gateways {
    /// The way to the engine.
    pub engine: Client,
    /// The gateways' bookkeeping. It is locked for one operation of the store at a time, and never across a
    /// request to the engine, to Slack or to a mail provider.
    pub store: Mutex<Store>,
    /// The way out.
    outbound: Outbound,
    /// The Slack app, when the operator configured one.
    pub slack: Option<SlackSide>,
    /// The mail provider, when the operator configured one.
    pub mail: Option<MailSide>,
    /// Every team a gateway serves, Slack's first, each in the order the configuration names them.
    pub teams: Vec<Team>,
    /// The requests the endpoints turned away, counted so that a kind is said at most once a minute.
    pub refusals: Refusals,
    /// The deliveries that are being carried now, each by the key its provider's next delivery of the
    /// same thing has.
    carrying: std::sync::Mutex<HashSet<String>>,
    /// The places of the requests that are being answered.
    places: Arc<Semaphore>,
}

/// A delivery that is being carried. While it is held, another delivery of the same thing is not gone
/// through beside it; it is let go of when the carrying has ended, however it ended.
pub struct Carrying<'a> {
    gateways: &'a Gateways,
    key: String,
}

impl Drop for Carrying<'_> {
    fn drop(&mut self) {
        let mut carrying = self
            .gateways
            .carrying
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        carrying.remove(&self.key);
    }
}

/// The gateways before the engine runs: what the operator configured, and the state.
pub struct Ready {
    store: Store,
    slack: Option<SlackSide>,
    mail: Option<MailSide>,
    teams: Vec<Team>,
}

/// Makes the gateways of a configuration ready on the data directory. `None` when the configuration names
/// neither gateway: such a server has none.
///
/// # Errors
/// A sentence for the operator: a join secret that is none, a team named twice for one gateway, or a state
/// that does not read.
pub fn ready(config: config::Gateways, data: &Path) -> Result<Option<Ready>, String> {
    if config.slack.is_none() && config.mail.is_none() {
        return Ok(None);
    }
    let mut teams: Vec<Team> = Vec::new();
    let slack = match config.slack {
        None => None,
        Some(slack) => {
            for (at, join_secret) in slack.teams.iter().enumerate() {
                let member = format!("slack.teams[{at}].join_secret_file");
                teams.push(Team::new(Kind::Slack, join_secret, &member)?);
            }
            let or = |value: String, default: &str| {
                if value.is_empty() {
                    default.to_owned()
                } else {
                    value
                }
            };
            Some(SlackSide {
                api: Slack {
                    bot_token: slack.bot_token.reveal().to_owned(),
                    base_url: slack.api.trim_end_matches('/').to_owned(),
                },
                signing_secret: slack.signing_secret,
                bot_handle: or(slack.bot_handle, ns::DEFAULT_BOT_HANDLE),
                workspace: or(slack.workspace, ns::DEFAULT_WORKSPACE),
                bot_user: std::sync::Mutex::new(None),
            })
        }
    };
    let mail = match config.mail {
        None => None,
        Some(mail) => {
            for (at, team) in mail.teams.iter().enumerate() {
                let member = format!("mail.teams[{at}].join_secret_file");
                teams.push(Team {
                    inbox: team.inbox.clone(),
                    public_address: team.public_address.clone(),
                    from: team.from.clone(),
                    allowed_senders: team.allowed_senders.clone(),
                    ..Team::new(Kind::Email, &team.join_secret, &member)?
                });
            }
            let (key, secret) = (
                mail.api_key.reveal().to_owned(),
                Some(mail.webhook_secret.reveal().to_owned()),
            );
            let base = mail
                .api
                .as_deref()
                .map(|api| api.trim_end_matches('/').to_owned());
            let provider: Box<dyn Provider + Send + Sync> = match mail.provider {
                MailProvider::Resend => {
                    let mut resend = Resend::new(key, secret);
                    if let Some(base) = base {
                        resend.base_url = base;
                    }
                    Box::new(resend)
                }
                MailProvider::Postmark => {
                    let mut postmark = Postmark::new(key, secret);
                    if let Some(base) = base {
                        postmark.base_url = base;
                    }
                    Box::new(postmark)
                }
            };
            Some(MailSide {
                provider,
                kind: mail.provider,
            })
        }
    };
    // Two turns on one session's inbox would carry every message twice.
    for (at, team) in teams.iter().enumerate() {
        if teams[..at]
            .iter()
            .any(|earlier| earlier.kind == team.kind && earlier.team_ref == team.team_ref)
        {
            return Err(format!(
                "the gateway configuration names one team twice for the {}",
                presence::label(team.kind)
            ));
        }
    }
    Ok(Some(Ready {
        store: Store::open(data)?,
        slack,
        mail,
        teams,
    }))
}

impl Ready {
    /// The gateways, on a running engine.
    #[must_use]
    pub fn on(self, engine: Client) -> Gateways {
        Gateways {
            engine,
            store: Mutex::new(self.store),
            outbound: Outbound::new(),
            slack: self.slack,
            mail: self.mail,
            teams: self.teams,
            refusals: Refusals::default(),
            carrying: std::sync::Mutex::new(HashSet::new()),
            places: Arc::new(Semaphore::new(REQUESTS_AT_ONCE as usize)),
        }
    }
}

impl Gateways {
    /// Makes one request to Slack or to the mail provider.
    ///
    /// # Errors
    /// What the way out says of a request that got no answer: the host, and never what the request carried.
    pub async fn asked(&self, request: &HttpRequest) -> Result<Reply, String> {
        self.outbound.request(request).await
    }

    /// The gateways of one kind that serve a team now, in the order they joined: the Edge Functions' list of
    /// their `gateways` rows, oldest first. A team the configuration no longer names is not served, and
    /// neither is one whose gateway has no session yet.
    pub async fn served(&self, kind: Kind) -> Vec<Served> {
        let store = self.store.lock().await;
        store
            .memberships(kind)
            .filter(|membership| !membership.session_id.is_empty())
            .filter_map(|membership| {
                let team = self
                    .teams
                    .iter()
                    .find(|team| team.kind == kind && team.team_ref == membership.team_ref)?;
                Some(Served::new(
                    &self.engine,
                    membership,
                    &membership.session_id,
                    team,
                ))
            })
            .collect()
    }

    /// Says that an endpoint turned a request away: refused it, dropped its mail, or failed on it. Anyone
    /// can make an endpoint do that, so one kind is said at most once a minute, with how many there were
    /// since. `what` names the kind in the server's own words, and holds nothing a request carried.
    pub fn turned_away(&self, what: &str, now: Timestamp) {
        if let Some(line) = self.refusals.refused(what, now) {
            note(&line);
        }
    }

    /// A place for one request to an endpoint, held for as long as the request is being answered. `None`
    /// when as many are being answered as the server answers at once: the request is told to come again.
    #[must_use]
    pub fn place(&self) -> Option<OwnedSemaphorePermit> {
        Arc::clone(&self.places).try_acquire_owned().ok()
    }

    /// Whether every place is taken.
    #[must_use]
    pub fn full(&self) -> bool {
        self.places.available_permits() == 0
    }

    /// Waits until no request to an endpoint is being answered.
    pub async fn idle(&self) {
        // The places are never closed, so they are all had in the end.
        let _ = self.places.acquire_many(REQUESTS_AT_ONCE).await;
    }

    /// Begins carrying a provider's delivery, which `key` names. `None` when a delivery of the same thing
    /// is being carried now: that one decides what becomes of it, and this one is told to come again.
    ///
    /// This is a deliberate departure from the Edge Functions. They wrote a delivery down as received
    /// before they carried it, and answered every later delivery of it as a duplicate, so a message whose
    /// carrying failed, or was cut short by a stop, was lost. Here a delivery is written down only once
    /// the gateway is done with it ([`Gateways::done`]); until then it is known only for as long as it is
    /// being carried, and a delivery that comes after a failure, a stop or a crash is gone through in
    /// full. That is safe: the engine holds one message for an event's idempotency key, however often it
    /// is sent.
    #[must_use]
    pub fn carry(&self, key: &str) -> Option<Carrying<'_>> {
        let mut carrying = self
            .carrying
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        carrying.insert(key.to_owned()).then(|| Carrying {
            gateways: self,
            key: key.to_owned(),
        })
    }

    /// Whether the gateway is done with a provider's delivery: one of it that comes again is a duplicate.
    pub async fn is_done(&self, key: &str) -> bool {
        self.store.lock().await.delivered(key)
    }

    /// Writes down that the gateway is done with a provider's delivery: its message was carried and its
    /// thread row recorded, or the person was given an answer that is not a message.
    ///
    /// # Errors
    /// The file could not be written, and the delivery is not remembered as done.
    pub async fn done(&self, key: &str, now: Timestamp) -> Result<(), String> {
        self.store.lock().await.record_delivery(key, now)
    }

    /// The same for a delivery whose person has their answer already: an answer is not taken back, so a
    /// mark that cannot be written is said, sparingly, and the delivery is answered as it was.
    pub async fn answered(&self, key: &str, now: Timestamp) {
        if let Err(why) = self.done(key, now).await {
            self.turned_away(&why, now);
        }
    }

    /// Sends a mail through the provider and answers the provider's id for it.
    ///
    /// # Errors
    /// The provider refused the mail, or did not answer; a send that got no answer can be tried again.
    pub async fn send_mail(
        &self,
        provider: &(dyn Provider + Send + Sync),
        mail: &OutboundMail,
    ) -> Result<String, SendError> {
        match self.asked(&provider.send_request(mail)).await {
            Ok(reply) => provider.sent(reply.status, &reply.body),
            Err(why) => Err(SendError::new(0, why, None)),
        }
    }
}

/// A text as a line of the server's own may carry it: with nothing in it that ends the line or moves the
/// cursor. A team's name is its creator's own words.
#[must_use]
pub fn plain(text: &str) -> String {
    text.chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::config::MailProvider;
    use super::testing::{mail, scratch, slack};
    use super::*;

    /// A join secret's shape for a team that does not exist. It is put together here and not written out:
    /// the repository's secret scan refuses a file that holds anything shaped like a join secret.
    fn secret() -> String {
        format!(
            "brg1.{}.{}",
            "0123456789abcdef".repeat(2),
            "fedcba9876543210".repeat(2)
        )
    }

    #[test]
    fn a_configuration_is_made_ready_or_refused_before_anything_runs() {
        let data = scratch("ready");
        // Neither gateway is a server with none, and nothing is made in the directory for it.
        assert!(ready(config::Gateways::default(), &data).unwrap().is_none());
        assert!(!data.join("gateway").exists());

        let both = config::Gateways {
            slack: Some(slack("https://slack.example.com/api/", &secret())),
            mail: Some(mail(MailProvider::Resend, "http://127.0.0.1:1/", &secret())),
        };
        let made = ready(both.clone(), &data).unwrap().unwrap();
        // A team under both gateways is two gateways, Slack's first; the API's base has no slash to end it.
        let kinds: Vec<(Kind, &str)> = made
            .teams
            .iter()
            .map(|team| (team.kind, team.team_ref.as_str()))
            .collect();
        assert_eq!(
            kinds,
            [
                (Kind::Slack, "0123456789abcdef0123456789abcdef"),
                (Kind::Email, "0123456789abcdef0123456789abcdef")
            ]
        );
        assert_eq!(made.teams[1].inbox, "ops@in.example.com");
        assert_eq!(
            made.slack.as_ref().unwrap().api.base_url,
            "https://slack.example.com/api"
        );
        let mail_side = made.mail.as_ref().unwrap();
        assert_eq!(mail_side.provider.name(), "resend");
        // Resend signs the body, which is small; Postmark is known by what comes before a body that is
        // the whole mail.
        assert_eq!(
            (mail_side.checked_before_body(), mail_side.most()),
            (false, 1 << 20)
        );
        assert!(data.join("gateway").is_dir());

        // A file that holds no join secret, and a team named twice for one gateway, end the start; neither
        // sentence repeats a secret.
        let mut not_one = both.clone();
        not_one.slack.as_mut().unwrap().teams = vec![Secret::of("xoxb-NOT-A-JOIN-SECRET")];
        let refused = ready(not_one, &data).err().unwrap();
        assert!(
            refused.contains("the file slack.teams[0].join_secret_file names does not hold a join secret"),
            "{refused}"
        );
        assert!(!refused.contains("NOT-A-JOIN-SECRET"));
        let mut twice = both;
        twice.slack.as_mut().unwrap().teams.push(Secret::of(&secret()));
        let refused = ready(twice, &data).err().unwrap();
        assert!(
            refused.contains("one team twice for the slack gateway"),
            "{refused}"
        );
        assert!(!refused.contains("fedcba"));

        assert_eq!(plain("ops\nteam\u{1b}[2J"), "ops team [2J");
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn the_requests_answered_at_once_are_bounded_and_a_stop_can_wait_for_them() {
        let data = scratch("places");
        let engine = super::testing::engine_on(&data);
        let gateways = ready(
            config::Gateways {
                slack: Some(slack("http://127.0.0.1:1/api", &secret())),
                mail: None,
            },
            &data,
        )
        .unwrap()
        .unwrap()
        .on(engine);
        // Sixty-four requests have a place each, and the next has none: it is told to come again.
        let mut held: Vec<OwnedSemaphorePermit> = (0..REQUESTS_AT_ONCE)
            .map(|_| gateways.place().expect("a place"))
            .collect();
        assert!(gateways.full());
        assert!(gateways.place().is_none());
        // A request that is answered leaves its place to the next.
        held.pop();
        assert!(!gateways.full());
        held.push(gateways.place().expect("the place that was left"));
        // A stop waits for the requests that are being answered, and for no longer than they take.
        let waited = tokio::time::timeout(std::time::Duration::from_millis(50), gateways.idle()).await;
        assert!(waited.is_err());
        drop(held);
        let waited = tokio::time::timeout(std::time::Duration::from_secs(5), gateways.idle()).await;
        assert!(waited.is_ok());
        assert!(gateways.place().is_some());

        // A delivery is carried by one request at a time, and is free to be carried again once that one
        // has let go of it, however it ended.
        let first = gateways.carry("slack:Ev1").expect("nothing carries it yet");
        assert!(gateways.carry("slack:Ev1").is_none());
        assert!(gateways.carry("slack:Ev2").is_some());
        drop(first);
        assert!(gateways.carry("slack:Ev1").is_some());
        let _ = std::fs::remove_dir_all(&data);
    }
}
