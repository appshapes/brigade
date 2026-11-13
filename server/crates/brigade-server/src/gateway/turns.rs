//! The loop: every gateway's turn.
//!
//! The Edge Functions were ticked once a minute by the database, and a tick kept the gateway's session and
//! carried what waited in its inbox. Here the two have their own pace: the inbox is read and carried every
//! two seconds, and the session is kept once a minute, well inside its lease of ten. The loop sleeps between
//! its passes and listens to nothing: the engine's signal moves at the gateway's own heartbeat, and a loop
//! woken by it would wake itself.
//!
//! One gateway's turn that fails is noted and is that gateway's alone: the others have theirs, and the
//! server goes on. A gateway the team no longer knows joins it again with the secret and the credential it
//! holds, so it comes back as the member it was, and a gateway the team's owner banned stays out. It reads
//! the secret's file again before every join: a team's owner that rotates the secret and revokes everyone
//! who joined with the old one revokes the gateways too, and the operator then only replaces the file. A
//! team that refuses a join, the first or a later one, is asked again once a minute and no more often.
//!
//! Before the first pass, and before the server listens, every gateway takes its place: it joins and
//! registers, and carries nothing. A request that comes in then finds a session to be carried from.

use std::sync::Arc;
use std::time::{Duration, Instant};

use brigade_core::wire::JoinSecret;
use brigade_core::{Code, Error};
use brigade_gateway::{Sending, resolve, slack as rules};
use tokio::sync::watch;

use super::config::Secret;
use super::member::Session;
use super::presence::{self, Failure};
use super::served::Served;
use super::state::Kind;
use super::{Gateways, Team, mail_out, plain, slack_out};
use crate::note::{self, note};

/// How long the loop rests between two passes over the gateways.
const TURN_EVERY: Duration = Duration::from_secs(2);

/// How often a gateway's session is kept. Its lease is ten minutes.
const HEARTBEAT_EVERY: Duration = Duration::from_secs(60);

/// How often the rows and the received ids older than ninety days are let go of: the Edge Functions' hour.
const SWEEP_EVERY: Duration = Duration::from_secs(60 * 60);

/// How many sentences a gateway remembers having said since its last quiet turn.
const SAID_AT_MOST: usize = 32;

/// What one turn did with the messages it found.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Tally {
    /// Messages carried out and acknowledged.
    pub delivered: u32,
    /// Messages that cannot be carried: acknowledged, their senders told.
    pub undeliverable: u32,
    /// Messages left in the inbox for a later turn.
    pub retry: u32,
    /// The code of each failure that left a message for a later turn: Slack's own, or the status a mail
    /// provider answered.
    pub codes: Vec<String>,
    /// The engine's code for each note to a sender that it refused.
    pub untold: Vec<&'static str>,
}

impl Tally {
    /// Counts a message left for a later turn, and why.
    pub fn again(&mut self, code: String) {
        self.retry += 1;
        self.codes.push(code);
    }

    /// What the turn did, as the server says it: counts and codes, and nothing a message carried. `None`
    /// for a turn that found nothing to do.
    fn sentence(&self) -> Option<String> {
        if *self == Self::default() {
            return None;
        }
        let mut said = format!(
            "delivered {}, could not deliver {}, left {} for a later turn",
            self.delivered, self.undeliverable, self.retry
        );
        if !self.codes.is_empty() {
            said.push_str(&format!(" ({})", plain(&self.codes.join(", "))));
        }
        if !self.untold.is_empty() {
            said.push_str(&format!(
                "; {} of the senders could not be told ({})",
                self.untold.len(),
                self.untold.join(", ")
            ));
        }
        Some(said)
    }
}

/// Tells a session that its message was not delivered. A note the engine refuses is counted, never fatal:
/// the message is acknowledged all the same, as it was when the Edge Functions only logged the refusal.
pub async fn tell_sender(gateway: &Served, note: Sending, tally: &mut Tally) {
    if let Err(refusal) = gateway.send(&note).await {
        tally.untold.push(refusal.code.as_str());
    }
}

/// What a gateway's session says of itself, at its registration and at every heartbeat.
struct About {
    name: &'static str,
    description: String,
    harness: &'static str,
    version: &'static str,
}

impl About {
    fn of(gateways: &Gateways, team: &Team) -> Self {
        match (team.kind, &gateways.slack) {
            (Kind::Slack, Some(slack)) => Self {
                name: rules::SLACK_SESSION_NAME,
                description: rules::slack_description(&slack.bot_handle, &slack.workspace),
                harness: rules::SLACK_HARNESS,
                version: rules::SLACK_GATEWAY_VERSION,
            },
            _ => Self {
                name: resolve::SESSION_NAME,
                description: resolve::description(&team.public_address),
                harness: resolve::HARNESS,
                version: resolve::GATEWAY_VERSION,
            },
        }
    }

    fn session(&self) -> Session<'_> {
        Session {
            name: self.name,
            description: &self.description,
            harness: self.harness,
            harness_version: self.version,
        }
    }
}

/// One team's gateway, between its turns.
struct Slot {
    /// Which of [`Gateways::teams`] it serves.
    at: usize,
    /// The team's name, once the gateway is a member of it.
    team_name: Option<String>,
    /// When the session was last kept.
    kept: Option<Instant>,
    /// When the gateway last asked to join its team again, so that it asks at most once a heartbeat for as
    /// long as the team refuses it.
    rejoined: Option<Instant>,
    /// When the team last refused the gateway's first join, and what it answered. It is asked again after
    /// a heartbeat's time; until then a turn fails with that answer and the team is asked nothing.
    refused: Option<(Instant, Error)>,
    /// The team's join secret as its file held it when the gateway last read it again, before a join; none
    /// until it has. It differs from the one the server started with once the file was replaced: the
    /// team's owner rotated the secret, or the operator mended a file that was wrong.
    join_secret: Option<Secret>,
    /// What was said of the turns since the last quiet one, so that a failure that lasts is said once and
    /// not every two seconds.
    said: Vec<String>,
}

impl Slot {
    fn new(at: usize) -> Self {
        Self {
            at,
            team_name: None,
            kept: None,
            rejoined: None,
            refused: None,
            join_secret: None,
            said: Vec::new(),
        }
    }

    /// The secret the gateway joins its team with: the one its file held when it was last read.
    fn join_secret<'a>(&'a self, team: &'a Team) -> &'a str {
        self.join_secret.as_ref().unwrap_or(&team.join_secret).reveal()
    }

    /// Reads the team's join secret again from its file. A join secret of the same team is the one the
    /// gateway joins with from now on. Anything else is said once, without the path and without what the
    /// file held, and the secret the gateway has is kept: a file that is being replaced reads right the
    /// next time.
    fn read_join_secret_again(&mut self, team: &Team) {
        let Some(read) = team.join_secret.read_again() else {
            return;
        };
        let who = self.who(team);
        match read {
            Ok(secret) => {
                let of_the_team =
                    JoinSecret::parse(secret.reveal()).is_ok_and(|parsed| parsed.team_ref() == team.team_ref);
                if of_the_team {
                    self.join_secret = Some(secret);
                } else {
                    self.once(format!(
                        "{who} found no join secret of its team in its join secret file"
                    ));
                }
            }
            Err(kind) => self.once(format!("{who} could not read its join secret file again: {kind}")),
        }
    }

    /// Who the server says did something: the gateway and its team, by the team's name once it is known.
    fn who(&self, team: &Team) -> String {
        let name = self.team_name.as_deref().unwrap_or(&team.team_ref);
        format!("the {} of team {}", presence::label(team.kind), plain(name))
    }

    /// Says a sentence once between two quiet turns.
    fn once(&mut self, sentence: String) {
        if !self.said.contains(&sentence) {
            note(&sentence);
            // A gateway that is never quiet does not remember everything it ever said.
            if self.said.len() >= SAID_AT_MOST {
                self.said.remove(0);
            }
            self.said.push(sentence);
        }
    }

    /// One turn: the gateway is a member, its session is kept when that is due, and what waits in its inbox
    /// is carried. Whatever fails is noted here and goes no further; what the turn did is answered.
    async fn turn(
        &mut self,
        gateways: &Gateways,
        pass: &mut slack_out::Pass,
        stopping: &watch::Receiver<bool>,
    ) -> Tally {
        let team = &gateways.teams[self.at];
        let mut tally = Tally::default();
        let outcome = match self.seat(gateways, team).await {
            Ok(gateway) => carry(gateways, team, &gateway, pass, &mut tally, stopping).await,
            Err(failure) => Err(failure),
        };
        let who = self.who(team);
        match tally.sentence() {
            Some(did) if tally.delivered + tally.undeliverable > 0 => {
                note(&format!("{who} {did}"));
                self.said.clear();
            }
            // A message that waits is tried every turn, and said when it begins to wait.
            Some(did) => self.once(format!("{who} {did}")),
            None if outcome.is_ok() => self.said.clear(),
            None => {}
        }
        if let Err(failure) = outcome {
            self.failed(gateways, team, &failure).await;
        }
        tally
    }

    /// The gateway's place before the server listens: it is a member with a session, and carries nothing
    /// yet. A request that arrives finds it there. A team that cannot be joined now is noted, and is the
    /// loop's to try again.
    async fn take_place(&mut self, gateways: &Gateways) {
        let team = &gateways.teams[self.at];
        if let Err(failure) = self.seat(gateways, team).await {
            self.failed(gateways, team, &failure).await;
        }
    }

    /// What follows a failure. It is said once. A session that is gone is kept anew at the next turn. A
    /// gateway the team no longer knows joins again with the secret its file holds now and the credential
    /// it holds, and has its session again at once: until it has one, nothing that comes for the team can
    /// be carried. It tries that once a minute for as long as the team refuses it, so a secret that was
    /// rotated lets the gateway back in within a minute of the operator replacing the file, and a gateway
    /// the team's owner banned asks once a minute and stays out.
    async fn failed(&mut self, gateways: &Gateways, team: &Team, failure: &Failure) {
        let who = self.who(team);
        // The contract's code and message, or the store's sentence: nothing a message carried.
        self.once(format!(
            "{who} could not finish a turn: {}",
            plain(&failure.to_string())
        ));
        let Failure::Refused(refusal) = failure else {
            return;
        };
        if matches!(refusal.code, Code::NotFound | Code::Conflict) {
            // The session is gone or closed: the next turn keeps it first, which registers it again.
            self.kept = None;
        }
        let may_rejoin = self.rejoined.is_none_or(|at| at.elapsed() >= HEARTBEAT_EVERY);
        if !(presence::lost(refusal) && self.team_name.is_some() && may_rejoin) {
            return;
        }
        self.rejoined = Some(Instant::now());
        self.read_join_secret_again(team);
        let joined = presence::join(
            &gateways.engine,
            &gateways.store,
            team.kind,
            self.join_secret(team),
        )
        .await;
        let registered = match joined {
            Ok((membership, same_member)) => {
                // The one line an operator looks for after replacing the file. It names the gateway and
                // the team, and nothing of the secret. A gateway whose credential the server no longer
                // knew is a new member, and the line says so.
                note(&format!(
                    "the {} joined team {} again{}",
                    presence::label(team.kind),
                    plain(&membership.team_name),
                    if same_member { "" } else { ", as a new member" }
                ));
                self.kept = None;
                self.seat(gateways, team).await.map(|_| ())
            }
            Err(failure) => Err(failure),
        };
        if let Err(failure) = registered {
            self.once(format!(
                "{who} could not join its team again: {}",
                plain(&failure.to_string())
            ));
        }
    }

    /// Makes the gateway a member of its team, joining it when it is none yet, and keeps its session when
    /// that is due, registering one when it has none. What it answers is the gateway as a turn acts for it.
    ///
    /// The first join reads the secret's file again before it asks, so a file the operator mended is the
    /// one joined with and the server is not started again for it. A team that refused the join is asked
    /// again after a heartbeat's time and no sooner: until then the turn fails with what the team
    /// answered. A join that failed for any other reason, an engine that is stopping among them, is tried
    /// at the next turn.
    async fn seat(&mut self, gateways: &Gateways, team: &Team) -> Result<Served, Failure> {
        let was_member = gateways
            .store
            .lock()
            .await
            .membership(&team.team_ref, team.kind)
            .is_some();
        if !was_member {
            if let Some((at, refusal)) = &self.refused
                && at.elapsed() < HEARTBEAT_EVERY
            {
                return Err(Failure::Refused(refusal.clone()));
            }
            self.read_join_secret_again(team);
        }
        let joined = presence::member_of(
            &gateways.engine,
            &gateways.store,
            team.kind,
            self.join_secret(team),
        )
        .await;
        if !was_member {
            self.refused = match &joined {
                Err(Failure::Refused(refusal)) if presence::lost(refusal) => {
                    Some((Instant::now(), refusal.clone()))
                }
                _ => None,
            };
        }
        let membership = joined?;
        if !was_member {
            note(&format!(
                "the {} joined team {}",
                presence::label(team.kind),
                plain(&membership.team_name)
            ));
        }
        self.team_name = Some(membership.team_name.clone());
        let due =
            membership.session_id.is_empty() || self.kept.is_none_or(|at| at.elapsed() >= HEARTBEAT_EVERY);
        let session_id = if due {
            let about = About::of(gateways, team);
            let session_id =
                presence::session(&gateways.engine, &gateways.store, &membership, &about.session()).await?;
            self.kept = Some(Instant::now());
            if session_id != membership.session_id {
                note(&format!("{} registered its session", self.who(team)));
            }
            session_id
        } else {
            membership.session_id.clone()
        };
        Ok(Served::new(&gateways.engine, &membership, &session_id, team))
    }
}

/// Carries what waits in a gateway's inbox, by the gateway's kind.
async fn carry(
    gateways: &Gateways,
    team: &Team,
    gateway: &Served,
    pass: &mut slack_out::Pass,
    tally: &mut Tally,
    stopping: &watch::Receiver<bool>,
) -> Result<(), Failure> {
    match (team.kind, &gateways.slack, &gateways.mail) {
        (Kind::Slack, Some(slack), _) => {
            slack_out::turn(gateways, slack, gateway, pass, tally, stopping).await
        }
        (Kind::Email, _, Some(mail)) => {
            mail_out::turn(gateways, &*mail.provider, gateway, tally, stopping).await
        }
        // A team is only ever configured under its gateway.
        _ => Ok(()),
    }
}

/// Every gateway's place between two passes of the loop.
pub struct Turns {
    slots: Vec<Slot>,
    /// When the store was last swept.
    swept: Option<Instant>,
}

impl Turns {
    /// The gateways before their first turn.
    #[must_use]
    pub fn new(gateways: &Gateways) -> Self {
        Self {
            slots: (0..gateways.teams.len()).map(Slot::new).collect(),
            swept: None,
        }
    }

    /// Gives every gateway its place before the server listens: each joins its team and registers its
    /// session, or keeps the one it has, and none carries anything yet. So no request can come before a
    /// gateway has a session to carry it from, unless the gateway's team could not be joined.
    pub async fn take_places(&mut self, gateways: &Gateways) {
        for slot in &mut self.slots {
            slot.take_place(gateways).await;
        }
    }

    /// One pass: every gateway has its turn, in the order the configuration names them, and what each turn
    /// did is answered. A server that is stopping gives no further gateway a turn.
    pub async fn pass(&mut self, gateways: &Gateways, stopping: &watch::Receiver<bool>) -> Vec<Tally> {
        if self.swept.is_none_or(|at| at.elapsed() >= SWEEP_EVERY) {
            self.swept = Some(Instant::now());
            if let Err(why) = gateways.store.lock().await.sweep(note::now()) {
                note(&why);
            }
        }
        // What the endpoints refused since they last said so, for a kind whose minute is over.
        for line in gateways.refusals.due(note::now()) {
            note(&line);
        }
        // What one pass learns from Slack is every gateway's of the pass, as it was every gateway's of a
        // tick.
        let mut pass = slack_out::Pass::default();
        let mut tallies = Vec::new();
        for slot in &mut self.slots {
            if *stopping.borrow() {
                break;
            }
            tallies.push(slot.turn(gateways, &mut pass, stopping).await);
        }
        tallies
    }
}

/// Gives every configured gateway its turn, pass after pass, until the server is told to stop. `turns` is
/// the gateways as they took their places before the server listened.
pub async fn run(gateways: Arc<Gateways>, mut turns: Turns, mut stopping: watch::Receiver<bool>) {
    while !*stopping.borrow() {
        turns.pass(&gateways, &stopping).await;
        tokio::select! {
            () = tokio::time::sleep(TURN_EVERY) => {}
            changed = stopping.changed() => {
                if changed.is_err() {
                    break;
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use brigade_core::hash::sha256_hex;
    use hyper::body::Bytes;
    use serde_json::{Value, json};

    use super::*;
    use crate::gateway::config::{self, MailProvider};
    use crate::gateway::standin::{self, StandIn};
    use crate::gateway::state::Membership;
    use crate::gateway::testing::{World, mail, scratch, slack};

    fn gateway_rows(roster: &[Value], harness: &str) -> Vec<Value> {
        roster
            .iter()
            .filter(|row| row["harness"] == json!(harness))
            .cloned()
            .collect()
    }

    /// What the gateways' state holds of the Slack gateway's membership of the team: its principal, its
    /// credential and its session.
    fn held(world: &World) -> Membership {
        let store = world.gateways.store.try_lock().unwrap();
        store
            .membership(&world.ops.team_ref, Kind::Slack)
            .expect("a membership of the Slack gateway")
            .clone()
    }

    /// The team's active members, as its owner's roster shows them.
    async fn members(world: &World) -> Vec<Value> {
        let body = json!({"team_ref": world.ops.team_ref});
        let roster = world.ops.administer("/v1/team/members", &body).await;
        roster["members"].as_array().unwrap().clone()
    }

    /// Makes the minute since the gateway last asked to join again be over.
    fn a_minute_later(world: &mut World) {
        world.turns.slots[0].rejoined = Instant::now().checked_sub(HEARTBEAT_EVERY);
    }

    #[tokio::test]
    async fn a_gateway_joins_once_registers_once_and_keeps_its_session_when_that_is_due() {
        let api = StandIn::start(standin::slack);
        let mut world = World::with_slack("turns-keep", &api).await;
        // The first turn made it a member like any other, with the session the rules describe.
        let roster = world.ops.roster().await;
        assert_eq!(roster.len(), 2);
        let own = gateway_rows(&roster, "gateway-slack").remove(0);
        assert_eq!(
            (
                &own["session_name"],
                &own["human_label"],
                &own["inbound"],
                &own["state"]
            ),
            (
                &json!("slack-gateway"),
                &json!("slack gateway"),
                &json!("accept"),
                &json!("idle")
            )
        );
        assert_eq!(
            own["session_description"],
            json!(rules::slack_description("brigade", "example.slack.com"))
        );
        assert_eq!(own["harness_version"], json!("0.1.0"));

        // A turn two seconds later joins nothing, registers nothing and keeps nothing: it only looks.
        assert_eq!(world.pass().await, [Tally::default()]);
        let roster = world.ops.roster().await;
        assert_eq!(roster.len(), 2);
        assert_eq!(gateway_rows(&roster, "gateway-slack")[0], own);

        // When the minute is over the session is kept, and it is the same session.
        world.turns.slots[0].kept = None;
        world.pass().await;
        let kept = gateway_rows(&world.ops.roster().await, "gateway-slack").remove(0);
        assert_eq!(kept["session_id"], own["session_id"]);
        assert_ne!(kept["last_seen_at"], own["last_seen_at"]);
        // Slack was asked nothing in all of this: there was nothing to carry.
        assert!(api.asked().is_empty());
    }

    #[tokio::test]
    async fn a_gateway_the_team_no_longer_knows_joins_again_and_its_failure_is_its_own() {
        let (slack_api, postmark) = (StandIn::start(standin::slack), StandIn::start(standin::postmark));
        let api = format!("{}/api", slack_api.url);
        let mut world = World::new("turns-rejoin", |join_secret| config::Gateways {
            slack: Some(slack(&api, join_secret)),
            mail: Some(mail(MailProvider::Postmark, &postmark.url, join_secret)),
        })
        .await;
        world.pass().await;
        let (before, mailer) = (world.gateway(Kind::Slack).await, world.gateway(Kind::Email).await);
        let credential = |world: &World| {
            let store = world.gateways.store.try_lock().unwrap();
            let membership = store.membership(&world.ops.team_ref, Kind::Slack).unwrap();
            (membership.credential.clone(), membership.session_id.clone())
        };
        let revoke = |world: &World, credential: String| {
            let engine = world.engine.clone();
            async move {
                engine
                    .ask(
                        Some(sha256_hex(&credential)),
                        false,
                        true,
                        "/v1/credential/revoke",
                        Bytes::from("{}"),
                    )
                    .await
                    .unwrap();
            }
        };

        // The Slack gateway's credential is revoked while a mail waits for the mail gateway.
        let (first, _) = credential(&world);
        revoke(&world, first.clone()).await;
        world
            .ops
            .send(&mailer.session_id, "to: pat@example.com\n\nhello", "", None)
            .await;
        let did = world.pass().await;
        // The Slack gateway's turn failed and it joined again; the mail gateway's turn was its own.
        assert_eq!(did[0], Tally::default());
        assert_eq!(did[1].delivered, 1);
        // It has a session again at once, as the new member it is: what comes for the team can be carried.
        let (second, session) = credential(&world);
        assert_ne!(second, first);
        let after = world.gateway(Kind::Slack).await;
        assert_eq!(after.session_id, session);
        assert_ne!(after.session_id, before.session_id);
        // And the next turn carries from it.
        world
            .ops
            .send(&after.session_id, "to: #general\n\nback", "", None)
            .await;
        assert_eq!(world.pass().await[0].delivered, 1);
        // The member it was is still on the roster until its lease runs out: two sessions carry the
        // gateway's harness for that long.
        assert_eq!(gateway_rows(&world.ops.roster().await, "gateway-slack").len(), 2);

        // A gateway that is lost again within the minute does not join again: every join is a new member.
        revoke(&world, second.clone()).await;
        assert_eq!(world.pass().await[0], Tally::default());
        assert_eq!(credential(&world).0, second);
        assert_eq!(world.turns.slots[0].said.len(), 1);
        // The same failure two seconds later is not said again.
        world.pass().await;
        assert_eq!(world.turns.slots[0].said.len(), 1);
    }

    #[tokio::test]
    async fn a_gateway_revoked_after_a_rotation_joins_again_with_the_secret_its_file_holds_now() {
        let slack_api = StandIn::start(standin::slack);
        let api = format!("{}/api", slack_api.url);
        let files = scratch("turns-rotated-files");
        let file = files.join("ops.secret");
        let mut world = World::new("turns-rotated", |join_secret| {
            // The gateway's join secret is in a file, as the configuration names one.
            std::fs::write(&file, format!("{join_secret}\n")).unwrap();
            let mut slack = slack(&api, join_secret);
            slack.teams = vec![Secret::kept_in(&file)];
            config::Gateways {
                slack: Some(slack),
                mail: None,
            }
        })
        .await;
        world.pass().await;
        let before = world.gateway(Kind::Slack).await;
        let member_before = held(&world);
        let said = |world: &World| world.turns.slots[0].said.clone();

        // The team's owner rotates the secret and revokes everyone who joined with the old one. The
        // gateway joined with it, so it is revoked with them, and its session leaves the roster.
        let team_ref = world.ops.team_ref.clone();
        let rotated = world
            .ops
            .administer("/v1/team/rotate-secret", &json!({"team_ref": team_ref}))
            .await;
        let new_secret = rotated["join_secret"].as_str().unwrap().to_owned();
        let swept = world
            .ops
            .administer(
                "/v1/team/revoke-member",
                &json!({"team_ref": team_ref, "joined_secret_version_lte": 1}),
            )
            .await;
        assert_eq!(
            (&swept["revoked"], &swept["sessions_closed"]),
            (&json!(1), &json!(1))
        );
        assert!(gateway_rows(&world.ops.roster().await, "gateway-slack").is_empty());

        // Its next turn is refused. It reads its file again and joins with what the file holds, which
        // is still the old secret, and says once what the team answered to each.
        assert_eq!(world.pass().await, [Tally::default()]);
        let first = said(&world);
        assert_eq!(first.len(), 2, "{first:?}");
        assert!(first[0].ends_with("unauthorized: not an active member of this team"));
        assert!(first[1].ends_with("could not join its team again: unauthorized: join secret rejected"));
        // It asks to join once a minute and no more often, however often its turn comes and whatever the
        // file holds by then: a file that is not there, another team's secret, and the new secret.
        std::fs::remove_file(&file).unwrap();
        world.pass().await;
        let (other, _) = world.ops.join_secret.rsplit_once('.').unwrap();
        std::fs::write(&file, format!("{other}x.{}\n", "0".repeat(32))).unwrap();
        world.pass().await;
        std::fs::write(&file, format!("{new_secret}\n")).unwrap();
        world.pass().await;
        assert_eq!(said(&world), first);
        assert!(gateway_rows(&world.ops.roster().await, "gateway-slack").is_empty());

        // When the minute is over it reads the file again and is a member again: the member it was, with
        // the principal, the credential and the session it had. The operator replaced a file and did
        // nothing else.
        a_minute_later(&mut world);
        world.pass().await;
        assert_eq!(held(&world), member_before);
        let after = world.gateway(Kind::Slack).await;
        assert_eq!(after.session_id, before.session_id);
        let rows = gateway_rows(&world.ops.roster().await, "gateway-slack");
        assert_eq!(rows.len(), 1);
        assert_eq!(
            (&rows[0]["session_id"], &rows[0]["principal_ref"]),
            (
                &json!(member_before.session_id),
                &json!(member_before.principal_ref)
            )
        );
        world
            .ops
            .send(&after.session_id, "to: #general\n\nback", "", None)
            .await;
        assert_eq!(world.pass().await[0].delivered, 1);
        // It joined with the secret of the team's second version, as a member's roster shows: the owner
        // is of the first, and the gateway is the one member it always was.
        let members = members(&world).await;
        let version = |human_label: &str| {
            let of_label: Vec<&Value> = members
                .iter()
                .filter(|member| member["human_label"] == json!(human_label))
                .map(|member| &member["joined_secret_version"])
                .collect();
            of_label
        };
        assert_eq!(members.len(), 2);
        assert_eq!(version("alice@example.com"), [&json!(1)]);
        assert_eq!(version("slack gateway"), [&json!(2)]);
        let _ = std::fs::remove_dir_all(&files);
    }

    #[tokio::test]
    async fn a_gateway_its_teams_owner_banned_stays_out_and_one_it_revoked_is_the_member_it_was() {
        let slack_api = StandIn::start(standin::slack);
        let mut world = World::with_slack("turns-banned", &slack_api).await;
        let before = held(&world);
        let team_ref = world.ops.team_ref.clone();
        let revoke =
            |ban: bool| json!({"team_ref": team_ref, "principal_ref": before.principal_ref, "ban": ban});
        let said = |world: &World| world.turns.slots[0].said.clone();

        // The team's owner bans its Slack gateway: the gateway's membership ends and its session closes.
        let banned = world
            .ops
            .administer("/v1/team/revoke-member", &revoke(true))
            .await;
        assert_eq!(
            (&banned["status"], &banned["changed"], &banned["sessions_closed"]),
            (&json!("banned"), &json!(true), &json!(1))
        );
        // Its next turn is refused, and so is its join: it asks with the credential it holds, so it is
        // the principal the owner banned, whose join is refused as a wrong secret is.
        assert_eq!(world.pass().await, [Tally::default()]);
        let first = said(&world);
        assert_eq!(first.len(), 2, "{first:?}");
        assert!(first[0].ends_with("unauthorized: not an active member of this team"));
        assert!(first[1].ends_with("could not join its team again: unauthorized: join secret rejected"));
        // It asks again every minute and is refused every time. It never comes back as somebody new: its
        // state holds the principal and the credential it had, and the team has its owner and no other
        // member.
        for _ in 0..3 {
            a_minute_later(&mut world);
            assert_eq!(world.pass().await, [Tally::default()]);
        }
        assert_eq!(held(&world), before);
        assert_eq!(said(&world), first);
        assert_eq!(members(&world).await.len(), 1);
        assert!(gateway_rows(&world.ops.roster().await, "gateway-slack").is_empty());

        // The owner lifts the ban with a revocation that is none. Within the minute nothing is asked; when
        // it is over the gateway joins and is the member it was: the same principal, the same credential,
        // and the session it had, open again.
        let lifted = world
            .ops
            .administer("/v1/team/revoke-member", &revoke(false))
            .await;
        assert_eq!(
            (&lifted["status"], &lifted["changed"]),
            (&json!("revoked"), &json!(true))
        );
        world.pass().await;
        assert_eq!(members(&world).await.len(), 1);
        a_minute_later(&mut world);
        world.pass().await;
        assert_eq!(held(&world), before);
        let members = members(&world).await;
        assert_eq!(members.len(), 2);
        assert!(
            members
                .iter()
                .any(|member| member["principal_ref"] == json!(before.principal_ref))
        );
        let rows = gateway_rows(&world.ops.roster().await, "gateway-slack");
        assert_eq!(rows.len(), 1);
        assert_eq!(
            (
                &rows[0]["session_id"],
                &rows[0]["principal_ref"],
                &rows[0]["state"]
            ),
            (
                &json!(before.session_id),
                &json!(before.principal_ref),
                &json!("idle")
            )
        );
        world
            .ops
            .send(&before.session_id, "to: #general\n\nback", "", None)
            .await;
        assert_eq!(world.pass().await[0].delivered, 1);
    }

    #[tokio::test]
    async fn a_gateway_whose_first_join_was_refused_reads_its_file_again_and_asks_once_a_minute() {
        let slack_api = StandIn::start(standin::slack);
        let api = format!("{}/api", slack_api.url);
        let files = scratch("turns-first-join-files");
        let file = files.join("ops.secret");
        let mut world = World::new("turns-first-join", |join_secret| {
            // The file holds a secret of the team's shape that is not the team's: a file the operator got
            // wrong before the server started.
            let (team, _) = join_secret.rsplit_once('.').unwrap();
            std::fs::write(&file, format!("{team}.{}\n", "0".repeat(32))).unwrap();
            let mut slack = slack(&api, join_secret);
            slack.teams = vec![Secret::kept_in(&file)];
            config::Gateways {
                slack: Some(slack),
                mail: None,
            }
        })
        .await;
        let member = |world: &World| {
            let store = world.gateways.store.try_lock().unwrap();
            store.membership(&world.ops.team_ref, Kind::Slack).is_some()
        };

        // The first join is refused, and that is said once.
        world.pass().await;
        assert!(!member(&world));
        let first = world.turns.slots[0].said.clone();
        assert_eq!(first.len(), 1, "{first:?}");
        assert!(first[0].ends_with("unauthorized: join secret rejected"));

        // The operator mends the file. Within the minute the team is not asked again, however often the
        // gateway's turn comes: with the file as it is now, a join that was asked would be let in.
        std::fs::write(&file, format!("{}\n", world.ops.join_secret)).unwrap();
        for _ in 0..3 {
            assert_eq!(world.pass().await, [Tally::default()]);
        }
        assert!(!member(&world));
        assert_eq!(world.turns.slots[0].said, first);
        assert_eq!(members(&world).await.len(), 1);

        // When the minute is over the gateway reads its file again and joins with what it holds now. The
        // server was not started again for it.
        let (at, _) = world.turns.slots[0]
            .refused
            .as_mut()
            .expect("the refusal of the first join");
        *at = Instant::now()
            .checked_sub(HEARTBEAT_EVERY)
            .expect("a machine that has been up for a minute");
        world.pass().await;
        assert!(member(&world));
        assert!(world.turns.slots[0].refused.is_none());
        assert_eq!(members(&world).await.len(), 2);
        assert_eq!(gateway_rows(&world.ops.roster().await, "gateway-slack").len(), 1);
        let _ = std::fs::remove_dir_all(&files);
    }

    #[tokio::test]
    async fn a_join_secret_file_that_does_not_read_is_said_without_its_path_and_the_secret_is_kept() {
        let slack_api = StandIn::start(standin::slack);
        let api = format!("{}/api", slack_api.url);
        let files = scratch("turns-unread-files");
        let file = files.join("PATH-MUST-NOT-LEAK.secret");
        let mut world = World::new("turns-unread", |join_secret| {
            std::fs::write(&file, format!("{join_secret}\n")).unwrap();
            let mut slack = slack(&api, join_secret);
            slack.teams = vec![Secret::kept_in(&file)];
            config::Gateways {
                slack: Some(slack),
                mail: None,
            }
        })
        .await;
        world.pass().await;
        let before = held(&world);
        // How many sessions of the gateway's the team's roster shows: one, or none while it is out.
        async fn on_roster(world: &World) -> usize {
            gateway_rows(&world.ops.roster().await, "gateway-slack").len()
        }
        // The owner revokes everyone who joined with the team's first secret, which it never rotated.
        async fn revoke(world: &World) -> Value {
            let body = json!({"team_ref": world.ops.team_ref, "joined_secret_version_lte": 1});
            world.ops.administer("/v1/team/revoke-member", &body).await
        }

        // The gateway is revoked while its file is gone. The secret was not rotated, so the one it has
        // still joins: the file's absence is said, by its kind, and the gateway is back all the same.
        std::fs::remove_file(&file).unwrap();
        assert_eq!(revoke(&world).await["revoked"], json!(1));
        world.pass().await;
        let said = world.turns.slots[0].said.clone();
        assert!(
            said.iter()
                .any(|line| line.ends_with("could not read its join secret file again: entity not found")),
            "{said:?}"
        );
        assert_eq!(on_roster(&world).await, 1);
        assert_eq!(held(&world), before);

        // The same when the file holds another team's secret, or nothing that is a join secret: neither
        // is joined with, and nothing the file held is said.
        for held in [
            format!(
                "{}x.MUST-NOT-LEAK\n",
                world.ops.join_secret.rsplit_once('.').unwrap().0
            ),
            "MUST-NOT-LEAK is no join secret\n".to_owned(),
            String::new(),
        ] {
            std::fs::write(&file, &held).unwrap();
            assert_eq!(revoke(&world).await["revoked"], json!(1));
            assert_eq!(on_roster(&world).await, 0);
            world.turns.slots[0].rejoined = None;
            world.pass().await;
            let said = world.turns.slots[0].said.clone();
            assert!(
                said.iter()
                    .any(|line| line.ends_with("found no join secret of its team in its join secret file")),
                "{said:?}"
            );
            assert!(
                said.iter().all(|line| !line.contains("MUST-NOT-LEAK")),
                "{said:?}"
            );
            assert_eq!(on_roster(&world).await, 1);
        }
        assert_eq!(held(&world), before);
        let _ = std::fs::remove_dir_all(&files);
    }

    #[tokio::test]
    async fn a_team_that_cannot_be_joined_is_said_once_and_does_not_stop_the_others() {
        let postmark = StandIn::start(standin::postmark);
        let mut world = World::new("turns-refused", |join_secret| {
            // A secret of the team's shape that is not the team's.
            let (team, _) = join_secret.rsplit_once('.').unwrap();
            config::Gateways {
                slack: Some(slack(
                    "http://127.0.0.1:1/api",
                    &format!("{team}.{}", "0".repeat(32)),
                )),
                mail: Some(mail(MailProvider::Postmark, &postmark.url, join_secret)),
            }
        })
        .await;
        world.pass().await;
        world.pass().await;
        assert!(
            world
                .gateways
                .store
                .lock()
                .await
                .membership(&world.ops.team_ref, Kind::Slack)
                .is_none()
        );
        assert_eq!(world.turns.slots[0].said.len(), 1);
        assert!(world.turns.slots[0].said[0].ends_with("unauthorized: join secret rejected"));
        // The mail gateway of the same team is a member and at work.
        let mailer = world.gateway(Kind::Email).await;
        world
            .ops
            .send(&mailer.session_id, "to: pat@example.com\n\nhello", "", None)
            .await;
        assert_eq!(world.pass().await[1].delivered, 1);
        assert_eq!(world.gateways.served(Kind::Slack).await.len(), 0);
    }

    #[tokio::test]
    async fn every_gateway_takes_its_place_before_anything_is_carried() {
        let (slack_api, postmark) = (StandIn::start(standin::slack), StandIn::start(standin::postmark));
        let api = format!("{}/api", slack_api.url);
        let mut world = World::new("turns-places", |join_secret| config::Gateways {
            slack: Some(slack(&api, join_secret)),
            mail: Some(mail(MailProvider::Postmark, &postmark.url, join_secret)),
        })
        .await;
        // Before the server listens no gateway is anywhere: a request that came now could not be carried.
        assert!(world.gateways.served(Kind::Slack).await.is_empty());
        assert!(world.gateways.served(Kind::Email).await.is_empty());

        world.turns.take_places(&world.gateways).await;
        // Each is a member with a session, on the roster like any other.
        let (slack_gateway, mail_gateway) =
            (world.gateway(Kind::Slack).await, world.gateway(Kind::Email).await);
        let roster = world.ops.roster().await;
        assert_eq!(
            gateway_rows(&roster, "gateway-slack")[0]["session_id"],
            json!(slack_gateway.session_id)
        );
        assert_eq!(
            gateway_rows(&roster, "gateway-email")[0]["session_id"],
            json!(mail_gateway.session_id)
        );

        // Taking a place carries nothing: what waits is the first turn's.
        world
            .ops
            .send(&slack_gateway.session_id, "to: #general\n\nhello", "", None)
            .await;
        world.turns.take_places(&world.gateways).await;
        assert_eq!(world.waiting(Kind::Slack).await, 1);
        assert!(slack_api.asked().is_empty());
        // The first turn keeps nothing again, the places being a moment old, and carries.
        let did = world.pass().await;
        assert_eq!((did[0].delivered, &did[1]), (1, &Tally::default()));
        let kept = gateway_rows(&world.ops.roster().await, "gateway-email").remove(0);
        assert_eq!(
            kept["last_seen_at"],
            gateway_rows(&roster, "gateway-email")[0]["last_seen_at"]
        );
        assert_eq!(world.ops.roster().await.len(), 3);
    }

    #[tokio::test]
    async fn a_team_that_cannot_be_joined_at_the_start_is_noted_and_left_to_the_loop() {
        let postmark = StandIn::start(standin::postmark);
        let mut world = World::new("turns-places-refused", |join_secret| {
            let (team, _) = join_secret.rsplit_once('.').unwrap();
            config::Gateways {
                slack: Some(slack(
                    "http://127.0.0.1:1/api",
                    &format!("{team}.{}", "0".repeat(32)),
                )),
                mail: Some(mail(MailProvider::Postmark, &postmark.url, join_secret)),
            }
        })
        .await;
        world.turns.take_places(&world.gateways).await;
        // The one that was refused has no place, and the other has its own.
        assert!(world.gateways.served(Kind::Slack).await.is_empty());
        assert_eq!(world.gateways.served(Kind::Email).await.len(), 1);
        assert_eq!(world.turns.slots[0].said.len(), 1);
        assert!(world.turns.slots[0].said[0].ends_with("unauthorized: join secret rejected"));
        // The loop's turn finds the team's refusal a moment old, asks nothing and says nothing again.
        world.pass().await;
        assert_eq!(world.turns.slots[0].said.len(), 1);
        assert!(world.gateways.served(Kind::Slack).await.is_empty());
    }

    #[test]
    fn what_a_turn_did_is_said_in_counts_and_codes() {
        assert_eq!(Tally::default().sentence(), None);
        let mut tally = Tally {
            delivered: 2,
            undeliverable: 1,
            ..Tally::default()
        };
        assert_eq!(
            tally.sentence().as_deref(),
            Some("delivered 2, could not deliver 1, left 0 for a later turn")
        );
        tally.again("ratelimited".to_owned());
        tally.again("http_503\nforged line".to_owned());
        tally.untold.push("rate_limited");
        assert_eq!(
            tally.sentence().as_deref(),
            Some(
                "delivered 2, could not deliver 1, left 2 for a later turn (ratelimited, http_503 forged line); 1 of the senders could not be told (rate_limited)"
            )
        );
    }
}
