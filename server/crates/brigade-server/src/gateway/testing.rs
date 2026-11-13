//! What the gateways' tests share: a directory of the test's own, a running engine, a team with one member's
//! session, and gateways pointed at a stand-in on loopback.
//!
//! Every secret here is an obvious fake.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU32, Ordering};

use brigade_core::hash::sha256_hex;
use hyper::body::Bytes;
use serde_json::{Value, json};
use tokio::sync::watch;

use super::config::{self, MailProvider, MailTeam, Secret};
use super::member::{Member, Outgoing, Session};
use super::served::Served;
use super::standin::StandIn;
use super::state::Kind;
use super::turns::{Tally, Turns};
use super::{Gateways, Team};
use crate::engine::{self, Client, Settings};
use crate::options::LogKind;

/// The bot token every test's Slack gateway carries.
pub const BOT_TOKEN: &str = "xoxb-FAKE-BOT-TOKEN-MUST-NOT-LEAK";
/// The signing secret every test's Slack app has.
pub const SIGNING_SECRET: &str = "fake-signing-secret";
/// The key every test's mail provider is asked with.
pub const API_KEY: &str = "fake-provider-key-MUST-NOT-LEAK";
/// A webhook secret in Resend's shape: `whsec_` and base64.
pub const WEBHOOK_SECRET: &str = "whsec_ZmFrZS13ZWJob29rLXNlY3JldA==";

/// A directory of the test's own.
pub fn scratch(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("brigade-gateway-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

/// A running engine on the file log.
pub fn engine_on(data: &Path) -> Client {
    engine::start(Settings {
        data: data.to_path_buf(),
        log: LogKind::File,
        feed_control: String::new(),
        feed_self: String::new(),
        driver_dir: None,
        snapshot_every: 2000,
        feed_roll_bytes: 16 << 20,
    })
    .unwrap()
    .client
}

/// A team and its first member, with one session: `ops`, in every test that needs one team.
pub struct Ops {
    /// The team.
    pub team_ref: String,
    /// The team's join secret.
    pub join_secret: String,
    /// The member's principal.
    pub principal_ref: String,
    /// The member's session.
    pub session: String,
    member: Member,
    engine: Client,
    /// The hash of the member's credential: what the engine knows the team's owner by.
    owner: String,
}

/// Creates the team `ops`, whose creator is labelled `alice@example.com` and has the session
/// `payments-api`.
pub async fn team(engine: &Client) -> Ops {
    team_named(engine, "ops", "alice@example.com", "payments-api").await
}

/// Creates a team whose creator has one session.
pub async fn team_named(engine: &Client, team_name: &str, human_label: &str, session_name: &str) -> Ops {
    let body = json!({"team_name": team_name, "human_label": human_label});
    let created = engine
        .ask(None, true, true, "/v1/team/create", Bytes::from(body.to_string()))
        .await
        .unwrap();
    let text = |name: &str| created[name].as_str().unwrap().to_owned();
    let member = Member::new(engine.clone(), &text("credential"));
    let owner = sha256_hex(&text("credential"));
    let coding = Session {
        name: session_name,
        description: "",
        harness: "claude-code",
        harness_version: "2.1.0",
    };
    let team_ref = text("team_ref");
    let session = member.register(&team_ref, &coding, None).await.unwrap();
    Ops {
        team_ref,
        join_secret: text("join_secret"),
        principal_ref: text("principal_ref"),
        session,
        member,
        engine: engine.clone(),
        owner,
    }
}

impl Ops {
    /// Sends a message from the member's session and answers its id.
    pub async fn send(&self, to: &str, body: &str, summary: &str, reply_to: Option<&str>) -> String {
        static SENT: AtomicU32 = AtomicU32::new(0);
        let key = format!("test-{}", SENT.fetch_add(1, Ordering::Relaxed));
        let sent = self
            .member
            .send(&Outgoing {
                sender_session_id: &self.session,
                recipient_session_id: to,
                body,
                summary,
                reply_to,
                idempotency_key: &key,
            })
            .await
            .unwrap();
        sent["message_id"].as_str().unwrap().to_owned()
    }

    /// Asks one of the administration requests as the member, who created the team and so owns it, the
    /// way a request over HTTP asks it.
    pub async fn administer(&self, path: &str, body: &Value) -> Value {
        self.engine
            .ask(
                Some(self.owner.clone()),
                false,
                true,
                path,
                Bytes::from(body.to_string()),
            )
            .await
            .unwrap()
    }

    /// What waits for the member's session, oldest first.
    pub async fn inbox(&self) -> Vec<Value> {
        self.member.receive(&self.session, 50).await.unwrap()
    }

    /// The team's roster, the offline sessions too.
    pub async fn roster(&self) -> Vec<Value> {
        self.member.sessions(&self.team_ref, true).await.unwrap()
    }

    /// The team as a Slack gateway is configured to serve it.
    pub fn slack_team(&self) -> Team {
        Team::new(
            Kind::Slack,
            &Secret::of(&self.join_secret),
            "slack.teams[0].join_secret_file",
        )
        .unwrap()
    }
}

/// A Slack gateway's configuration for one team, its Web API at `api`.
pub fn slack(api: &str, join_secret: &str) -> config::Slack {
    config::Slack {
        bot_token: Secret::of(BOT_TOKEN),
        signing_secret: Secret::of(SIGNING_SECRET),
        bot_handle: "brigade".to_owned(),
        workspace: "example.slack.com".to_owned(),
        api: api.to_owned(),
        teams: vec![Secret::of(join_secret)],
    }
}

/// A mail gateway's configuration for one team, its provider's API at `api`. Anyone may write in.
pub fn mail(provider: MailProvider, api: &str, join_secret: &str) -> config::Mail {
    config::Mail {
        provider,
        api_key: Secret::of(API_KEY),
        webhook_secret: Secret::of(WEBHOOK_SECRET),
        api: Some(api.to_owned()),
        teams: vec![MailTeam {
            join_secret: Secret::of(join_secret),
            inbox: "ops@in.example.com".to_owned(),
            public_address: "ops@example.com".to_owned(),
            from: "Ops <ops@example.com>".to_owned(),
            allowed_senders: Vec::new(),
        }],
    }
}

/// The gateways of a configuration, their state under `data`.
pub fn gateways(data: &Path, engine: &Client, config: config::Gateways) -> Gateways {
    super::ready(config, data)
        .unwrap()
        .expect("a configuration with a gateway")
        .on(engine.clone())
}

/// A team, its gateways and the loop's place between two passes: what a test of a handler stands on.
pub struct World {
    /// The test's directory.
    pub data: PathBuf,
    /// The engine.
    pub engine: Client,
    /// The team `ops` and its member's session.
    pub ops: Ops,
    /// The gateways.
    pub gateways: Gateways,
    /// The loop, pass by pass.
    pub turns: Turns,
    /// What the gateways were configured with, for a server that is started again.
    config: config::Gateways,
    /// Says the server is not stopping; kept so that the loop's side of it stays open.
    stop: watch::Sender<bool>,
}

impl World {
    /// A team whose gateways are the ones `config` makes of its join secret. No gateway has had a turn.
    pub async fn new(name: &str, config: impl FnOnce(&str) -> config::Gateways) -> Self {
        let data = scratch(name);
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        let config = config(&ops.join_secret);
        let gateways = gateways(&data, &engine, config.clone());
        let turns = Turns::new(&gateways);
        let (stop, _) = watch::channel(false);
        Self {
            data,
            engine,
            ops,
            gateways,
            turns,
            config,
            stop,
        }
    }

    /// The gateways as a server that ended without a word and was started again has them: whatever was
    /// being carried is forgotten, and what the state's file holds is all they know. `lost` is what of the
    /// file's content the ending took with it, for a test that ends the server between two writes.
    pub fn crash(&mut self, lost: impl FnOnce(&mut Value)) {
        let file = self.data.join("gateway").join("state.json");
        let mut state: Value = serde_json::from_slice(&std::fs::read(&file).unwrap()).unwrap();
        lost(&mut state);
        std::fs::write(&file, state.to_string()).unwrap();
        self.gateways = gateways(&self.data, &self.engine, self.config.clone());
        self.turns = Turns::new(&self.gateways);
    }

    /// A team with a Slack gateway whose Slack is `api`, after the turn that made it a member.
    pub async fn with_slack(name: &str, api: &StandIn) -> Self {
        let api = format!("{}/api", api.url);
        let mut world = Self::new(name, |join_secret| config::Gateways {
            slack: Some(slack(&api, join_secret)),
            mail: None,
        })
        .await;
        world.pass().await;
        world
    }

    /// A team with a mail gateway whose provider is at `api`, after the turn that made it a member.
    pub async fn with_mail(name: &str, provider: MailProvider, api: &StandIn) -> Self {
        let mut world = Self::new(name, |join_secret| config::Gateways {
            slack: None,
            mail: Some(mail(provider, &api.url, join_secret)),
        })
        .await;
        world.pass().await;
        world
    }

    /// One pass of the loop: every gateway's turn, and what each did.
    pub async fn pass(&mut self) -> Vec<Tally> {
        self.turns.pass(&self.gateways, &self.stop.subscribe()).await
    }

    /// The gateway of a kind, as an endpoint finds it.
    pub async fn gateway(&self, kind: Kind) -> Served {
        self.gateways
            .served(kind)
            .await
            .into_iter()
            .next()
            .expect("a gateway with a session")
    }

    /// What waits for the gateway's session of a kind.
    pub async fn waiting(&self, kind: Kind) -> usize {
        self.gateway(kind).await.inbox().await.unwrap().len()
    }
}

impl Drop for World {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.data);
    }
}
