//! The gateways in the running server: a team's sessions reach people on Slack and by mail, and people reach
//! the sessions, through the real binary with a stand-in for Slack and one for the mail provider beside it.
//!
//! What a handler says and decides is `brigade-gateway`'s tests, and each handler's own paths are tested
//! beside it against the engine. These tests are what only the process can show: the configuration read at
//! the start, the loop that turns by itself, the endpoints on the server's listener, a restart, and that
//! nothing secret is ever written to the server's own lines.
//!
//! Every secret here is an obvious fake.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::sync::atomic::{AtomicU8, Ordering};
use std::time::{Duration, Instant};

use brigade_gateway::slack::sign_slack;
use brigade_gateway::webhook_signature::sign;
use serde_json::{Value, json};

mod common;
#[path = "common/standin.rs"]
mod standin;

use common::{Scratch, Server, log_arguments, parse, request, text};
use standin::{Asked, Said, StandIn};

const BOT_TOKEN: &str = "xoxb-FAKE-BOT-TOKEN-MUST-NOT-LEAK";
const SIGNING_SECRET: &str = "fake-signing-secret-MUST-NOT-LEAK";
const API_KEY: &str = "fake-provider-key-MUST-NOT-LEAK";
const POSTMARK_HOOK: &str = "fake-webhook-password-MUST-NOT-LEAK";
/// A webhook secret in Resend's shape: `whsec_` and base64.
const RESEND_HOOK: &str = "whsec_ZmFrZS13ZWJob29rLXNlY3JldC1NVVNULU5PVC1MRUFL";

const FOOTER: &str = "\n\nReply in this thread to answer.\n_To write to another session, message @brigade with a first line \"to: <session or member>\"._";

/// Waits for something the loop or an endpoint does, which a test cannot be told of.
fn until<T>(what: &str, mut ready: impl FnMut() -> Option<T>) -> T {
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        if let Some(found) = ready() {
            return found;
        }
        assert!(Instant::now() < deadline, "it did not happen: {what}");
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The requests a stand-in was asked under one name, once there are `count` of them.
fn asked(stand_in: &StandIn, name: &str, count: usize) -> Vec<Asked> {
    until(&format!("{count} of {name}"), || {
        let asked = named(stand_in, name);
        (asked.len() >= count).then_some(asked)
    })
}

fn named(stand_in: &StandIn, name: &str) -> Vec<Asked> {
    stand_in
        .asked()
        .into_iter()
        .filter(|asked| asked.named() == name)
        .collect()
}

fn base64(bytes: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut text = String::new();
    for chunk in bytes.chunks(3) {
        let bits = chunk
            .iter()
            .fold(0_u32, |bits, byte| (bits << 8) | u32::from(*byte))
            << (8 * (3 - chunk.len()));
        for at in 0..4 {
            if at <= chunk.len() {
                text.push(char::from(ALPHABET[((bits >> (18 - 6 * at)) & 63) as usize]));
            } else {
                text.push('=');
            }
        }
    }
    text
}

fn seconds_now() -> String {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        .to_string()
}

/// A team named `ops` on a server whose Slack and mail gateways speak to two stand-ins.
struct World {
    scratch: Scratch,
    server: Server,
    arguments: Vec<String>,
    slack: StandIn,
    mail: StandIn,
    team_ref: String,
    join_secret: String,
    /// The credential of the team's creator, labelled `alice@example.com`.
    credential: String,
    /// Her session, `payments-api`.
    session: String,
    /// The five characters people name her session by.
    tail: String,
}

impl World {
    /// Creates the team on a server with no gateway, stops it, and starts it again with the gateways
    /// configured. `mail_answers` is what the mail provider's stand-in says; `allowed_senders` is the JSON
    /// list of who may write in by mail.
    fn start(
        test: &str,
        provider: &str,
        allowed_senders: &str,
        mail_answers: impl Fn(&Asked) -> Said + Send + Sync + 'static,
    ) -> Self {
        Self::start_with(test, provider, allowed_senders, standin::slack, mail_answers)
    }

    /// The same, with a Slack that answers what `slack_answers` says.
    fn start_with(
        test: &str,
        provider: &str,
        allowed_senders: &str,
        slack_answers: impl Fn(&Asked) -> Said + Send + Sync + 'static,
        mail_answers: impl Fn(&Asked) -> Said + Send + Sync + 'static,
    ) -> Self {
        let scratch = Scratch::new(&format!("gateway-{test}"));
        let mut arguments = log_arguments("file", &scratch);
        let server = Server::start(&scratch, &arguments);
        // A server nobody configured a gateway for has no such endpoint.
        for path in ["/gateway/slack", "/gateway/mail/postmark", "/gateway/mail/resend"] {
            let (status, envelope) = server.post(None, path, &json!({}));
            assert_eq!(
                (status, text(&envelope["error"], "code").as_str()),
                (404, "not_found"),
                "{path}"
            );
        }
        let (status, created) = server.post(
            None,
            "/v1/team/create",
            &json!({"team_name": "ops", "human_label": "alice@example.com"}),
        );
        assert_eq!(status, 200, "{created}");
        let created = &created["result"];
        let (team_ref, join_secret, credential) = (
            text(created, "team_ref"),
            text(created, "join_secret"),
            text(created, "credential"),
        );
        server.stop();

        let (slack, mail) = (StandIn::start(slack_answers), StandIn::start(mail_answers));
        let put = |name: &str, secret: &str| {
            let path = scratch.path(name);
            std::fs::write(&path, format!("{secret}\n")).unwrap();
            path.display().to_string()
        };
        let hook = if provider == "resend" {
            RESEND_HOOK
        } else {
            POSTMARK_HOOK
        };
        let config = json!({
            "slack": {
                "bot_token_file": put("slack-bot-token", BOT_TOKEN),
                "signing_secret_file": put("slack-signing-secret", SIGNING_SECRET),
                "bot_handle": "brigade",
                "workspace": "example.slack.com",
                "api": format!("{}/api", slack.url),
                "teams": [{"join_secret_file": put("ops.secret", &join_secret)}],
            },
            "mail": {
                "provider": provider,
                "api_key_file": put("mail-key", API_KEY),
                "webhook_secret_file": put("mail-webhook-secret", hook),
                "api": mail.url,
                "teams": [{
                    "join_secret_file": scratch.path("ops.secret").display().to_string(),
                    "inbox": "ops@in.example.com",
                    "public_address": "ops@example.com",
                    "from": "Ops <ops@example.com>",
                    "allowed_senders": serde_json::from_str::<Value>(allowed_senders).unwrap(),
                }],
            },
        });
        let file = scratch.path("gateways.json");
        std::fs::write(&file, config.to_string()).unwrap();
        arguments.extend(["--gateway-config".to_owned(), file.display().to_string()]);
        let server = Server::start(&scratch, &arguments);
        let registration = json!({
            "team_ref": team_ref, "harness": "claude-code", "harness_version": "2.1.0",
            "session_name": "payments-api", "activity": "busy", "inbound": "accept", "lease_seconds": 600,
        });
        let session = text(
            &server.ok(&credential, "/v1/session/register", &registration),
            "session_id",
        );
        let tail = session[session.len() - 5..].to_owned();
        let world = Self {
            scratch,
            server,
            arguments,
            slack,
            mail,
            team_ref,
            join_secret,
            credential,
            session,
            tail,
        };
        // The gateways join and register by themselves, at the first pass of the loop.
        world.gateway("gateway-slack");
        world.gateway("gateway-email");
        world
    }

    /// The same world after its server was killed and started again on the same directory.
    fn restarted(self) -> Self {
        self.started_again(Server::kill)
    }

    /// The same world after its server was told to stop, went, and was started again.
    fn stopped_and_started(self) -> Self {
        self.started_again(Server::stop)
    }

    fn started_again(self, end: impl FnOnce(Server)) -> Self {
        let Self {
            scratch,
            server,
            arguments,
            slack,
            mail,
            team_ref,
            join_secret,
            credential,
            session,
            tail,
        } = self;
        end(server);
        Self {
            server: Server::start(&scratch, &arguments),
            scratch,
            arguments,
            slack,
            mail,
            team_ref,
            join_secret,
            credential,
            session,
            tail,
        }
    }

    fn roster(&self) -> Vec<Value> {
        let list = json!({"team_ref": self.team_ref, "include_offline": true});
        self.server.ok(&self.credential, "/v1/session/list", &list)["sessions"]
            .as_array()
            .unwrap()
            .clone()
    }

    /// The one session on the roster with a gateway's harness, once it is there.
    fn gateway(&self, harness: &str) -> Value {
        until(&format!("{harness} on the roster"), || {
            let mut rows: Vec<Value> = self
                .roster()
                .into_iter()
                .filter(|row| row["harness"] == json!(harness))
                .collect();
            assert!(rows.len() <= 1, "{rows:?}");
            rows.pop()
        })
    }

    fn gateway_session(&self, harness: &str) -> String {
        text(&self.gateway(harness), "session_id")
    }

    /// Sends a message from alice's session and answers its id.
    fn send(&self, to: &str, body: &str, reply_to: Option<&str>) -> String {
        static SENT: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let key = SENT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let mut message = json!({
            "sender_session_id": self.session, "recipient_session_id": to, "body": body,
            "idempotency_key": format!("test-{key}"),
        });
        if let Some(reply_to) = reply_to {
            message["reply_to"] = json!(reply_to);
        }
        text(
            &self.server.ok(&self.credential, "/v1/message/send", &message),
            "message_id",
        )
    }

    /// What waits for alice's session, once there are `count` messages.
    fn inbox(&self, count: usize) -> Vec<Value> {
        until(&format!("{count} messages for the session"), || {
            let receive = json!({"session_id": self.session, "limit": 50, "wait_seconds": 0});
            let messages = self.server.ok(&self.credential, "/v1/message/receive", &receive)["messages"]
                .as_array()
                .unwrap()
                .clone();
            (messages.len() >= count).then_some(messages)
        })
    }

    /// How many messages wait for a gateway's session, asked as the gateway with what its state holds.
    fn waiting_for_gateway(&self, kind: &str) -> usize {
        let state: Value =
            serde_json::from_slice(&std::fs::read(self.scratch.path("data/gateway/state.json")).unwrap())
                .unwrap();
        let membership = state["memberships"]
            .as_array()
            .unwrap()
            .iter()
            .find(|membership| membership["kind"] == json!(kind))
            .unwrap()
            .clone();
        let receive = json!({"session_id": membership["session_id"], "limit": 50, "wait_seconds": 0});
        self.server
            .ok(&text(&membership, "credential"), "/v1/message/receive", &receive)["messages"]
            .as_array()
            .unwrap()
            .len()
    }

    /// One request to the server, as its bytes, and the status and the JSON it is answered with.
    fn raw(&self, bytes: &[u8]) -> (u16, Value) {
        let authority = self.server.url.strip_prefix("http://").unwrap();
        let mut stream = TcpStream::connect(authority).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(60))).unwrap();
        stream.write_all(bytes).unwrap();
        let mut response = Vec::new();
        stream.read_to_end(&mut response).unwrap();
        parse(&response)
    }

    /// A JSON request to one of the gateways' endpoints, with the headers a sender adds.
    fn post(&self, path: &str, body: &str, headers: &[(&str, &str)]) -> (u16, Value) {
        let authority = self.server.url.strip_prefix("http://").unwrap();
        self.raw(&request(authority, None, path, body, headers))
    }

    /// An Events API request as Slack signs one now, as its bytes.
    fn slack_event_bytes(&self, id: &str, event: &Value) -> Vec<u8> {
        let body = json!({"type": "event_callback", "event_id": id, "event": event}).to_string();
        let timestamp = seconds_now();
        let signature = sign_slack(SIGNING_SECRET, &timestamp, &body).unwrap();
        let authority = self.server.url.strip_prefix("http://").unwrap();
        request(
            authority,
            None,
            "/gateway/slack",
            &body,
            &[
                ("X-Slack-Request-Timestamp", &timestamp),
                ("X-Slack-Signature", &signature),
            ],
        )
    }

    /// An Events API request as Slack signs one now, and what it is answered.
    fn slack_event(&self, id: &str, event: &Value) -> (u16, Value) {
        self.raw(&self.slack_event_bytes(id, event))
    }

    /// A slash command as Slack signs one now: a form, not JSON.
    fn slash_command(&self, form: &str) -> (u16, Value) {
        let authority = self.server.url.strip_prefix("http://").unwrap();
        let timestamp = seconds_now();
        let signature = sign_slack(SIGNING_SECRET, &timestamp, form).unwrap();
        let head = format!(
            "POST /gateway/slack HTTP/1.1\r\nHost: {authority}\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: {}\r\nX-Slack-Request-Timestamp: {timestamp}\r\nX-Slack-Signature: {signature}\r\n\r\n{form}",
            form.len()
        );
        self.raw(head.as_bytes())
    }

    /// A direct message to the bot from the one person the stand-in knows.
    fn direct_message(&self, id: &str, ts: &str, text: &str) -> (u16, Value) {
        self.slack_event(
            id,
            &json!({
                "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
                "text": text, "ts": ts,
            }),
        )
    }

    /// What the server wrote about itself since it was last started.
    fn said(&self) -> String {
        std::fs::read_to_string(self.scratch.path("stderr")).unwrap()
    }

    /// Nothing the server wrote about itself holds a secret, a credential or a word of a message.
    fn nothing_leaked(&self, texts: &[&str]) {
        let said = self.said();
        let join_secret = self.join_secret.rsplit('.').next().unwrap();
        for secret in [
            BOT_TOKEN,
            SIGNING_SECRET,
            API_KEY,
            POSTMARK_HOOK,
            RESEND_HOOK,
            "MUST-NOT-LEAK",
            join_secret,
            self.credential.as_str(),
        ] {
            assert!(!said.contains(secret), "the server wrote a secret:\n{said}");
        }
        for text in texts {
            assert!(!said.contains(text), "the server wrote {text:?}:\n{said}");
        }
    }
}

/// Postmark's webhook for a received mail to the team's inbox.
fn postmark_mail(id: &str, from: &str, subject: &str, text: &str) -> String {
    json!({
        "MessageID": id, "MessageStream": "inbound", "From": from,
        "FromFull": {"Email": from, "Name": "Pat Example"},
        "ToFull": [{"Email": "ops@in.example.com", "Name": "", "MailboxHash": ""}],
        "OriginalRecipient": "ops@in.example.com", "Subject": subject,
        "Date": "2026-11-12T10:00:00Z", "TextBody": text,
        "Headers": [{"Name": "Message-ID", "Value": format!("<{id}@example.com>")}],
    })
    .to_string()
}

#[test]
fn the_gateways_are_members_on_the_roster_and_their_endpoints_are_the_configured_ones() {
    let world = World::start("roster", "postmark", "[]", standin::postmark);
    let slack = world.gateway("gateway-slack");
    assert_eq!(
        (&slack["session_name"], &slack["human_label"], &slack["state"]),
        (&json!("slack-gateway"), &json!("slack gateway"), &json!("idle"))
    );
    assert_eq!(
        slack["session_description"],
        json!(
            "slack gateway for example.slack.com: a person DMs @brigade or mentions it with a first line \"to: <session or member>\"; a session writes to me with \"to: @name\", \"to: #channel\" or \"to: <email>\""
        )
    );
    let mail = world.gateway("gateway-email");
    assert_eq!(
        (&mail["session_name"], &mail["human_label"], &mail["inbound"]),
        (&json!("mail-gateway"), &json!("mail gateway"), &json!("accept"))
    );
    assert_eq!(
        mail["session_description"],
        json!(
            "email gateway: a person writes to ops@example.com with a first line \"to: <session or member>\"; a session writes to me with the same line, \"to: <address>\""
        )
    );
    // Two members more than the team's creator, and each joined once.
    let members = world.server.ok(
        &world.credential,
        "/v1/team/members",
        &json!({"team_ref": world.team_ref}),
    );
    assert_eq!(members["members"].as_array().unwrap().len(), 3, "{members}");
    let said = until("the server to say where it listens", || {
        let said = world.said();
        said.contains("listening on").then_some(said)
    });
    assert_eq!(
        said.matches("the slack gateway joined team ops").count(),
        1,
        "{said}"
    );
    assert_eq!(
        said.matches("the mail gateway joined team ops").count(),
        1,
        "{said}"
    );
    // Each took its place before the server listened: no request could come before a gateway had a
    // session to carry it from.
    let listening = said.find("listening on").expect("the line that says where");
    for placed in [
        "the slack gateway of team ops registered its session",
        "the mail gateway of team ops registered its session",
    ] {
        assert!(said.find(placed).is_some_and(|at| at < listening), "{said}");
    }

    // Under /gateway/ only the configured endpoints are endpoints; the contract's paths are as they were.
    for path in [
        "/gateway/",
        "/gateway/slack/events",
        "/gateway/mail/resend",
        "/gateway/teams",
    ] {
        let (status, envelope) = world.post(path, "{}", &[]);
        assert_eq!(
            (status, text(&envelope["error"], "code").as_str()),
            (404, "not_found"),
            "{path}"
        );
    }
    let (status, info) = world.server.post(None, "/v1/info", &json!({}));
    assert_eq!((status, &info["result"]["contract_version"]), (200, &json!("1")));
    let authority = world.server.url.strip_prefix("http://").unwrap();
    assert_eq!(
        world.raw(format!("GET /gateway/slack HTTP/1.1\r\nHost: {authority}\r\n\r\n").as_bytes()),
        (405, json!({"error": "method not allowed"}))
    );
    // A request longer than Slack's megabyte is refused as too long, not read.
    let long = format!("{{\"padding\":\"{}\"}}", "x".repeat(1 << 20));
    assert_eq!(world.post("/gateway/slack", &long, &[]).0, 413);

    // What a member's own request changed about a team, and each request to a gateway's endpoint, left one
    // line (work item 313): the team, the member's reference and its label, or the gateway and the status.
    let (status, joined) = world.server.post(
        None,
        "/v1/team/join",
        &json!({"join_secret": world.join_secret, "human_label": "dana\nof the night shift"}),
    );
    assert_eq!(status, 200, "{joined}");
    let dana = text(&joined["result"], "principal_ref");
    let said = world.said();
    let team = format!("team ops ({})", world.team_ref);
    assert!(
        said.contains(&format!(
            "member {dana} joined {team}, labelled \"dana?of the night shift\""
        )),
        "{said}"
    );
    // The gateways joined inside the server, and their turns said so: theirs is no member's own request,
    // and it has no second line.
    assert_eq!(said.matches(": member ").count(), 1, "{said}");
    for line in [
        "the slack gateway refused a request that is not a POST (405)",
        "the slack gateway refused a request whose body is too long (413)",
    ] {
        assert_eq!(said.matches(line).count(), 1, "{line}\n{said}");
    }
    world.nothing_leaked(&[]);

    // Told to stop, the server ends its gateways' turns with everything else and goes.
    let World { scratch, server, .. } = world;
    server.stop();
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(said.contains("stopped at position"), "{said}");
}

#[test]
fn a_sessions_message_is_posted_to_slack_once_and_one_slack_cannot_place_is_its_senders_to_know() {
    let world = World::start("slack-out", "postmark", "[]", standin::postmark);
    let gateway = world.gateway_session("gateway-slack");
    let sent = world.send(&gateway, "to: #general\n\nthe migration has landed", None);
    let posts = asked(&world.slack, "POST /api/chat.postMessage", 1);
    assert_eq!(
        posts[0].header("authorization"),
        Some(format!("Bearer {BOT_TOKEN}").as_str())
    );
    assert_eq!(
        posts[0].json(),
        json!({
            "channel": "C0GENERAL1",
            "text": format!("*payments-api* · alice@example.com\n\nthe migration has landed{FOOTER}"),
            "unfurl_links": false,
            "unfurl_media": false,
            "metadata": {
                "event_type": "brigade_message",
                "event_payload": {"message_id": sent, "from_session": world.session, "team": "ops"},
            },
        })
    );

    // A name Slack does not know: the sender is told, in answer to the message, and nothing is posted.
    let lost = world.send(&gateway, "to: #nowhere\n\nis anybody there?", None);
    let told = world.inbox(1);
    assert_eq!(
        (&told[0]["body"], &told[0]["summary"], &told[0]["reply_to"]),
        (
            &json!(format!(
                "The Slack gateway could not deliver your message {lost}: no Slack user or channel matches \"#nowhere\""
            )),
            &json!("slack gateway: not delivered"),
            &json!(lost)
        )
    );
    assert_eq!(told[0]["sender"]["session_id"], json!(gateway));
    // That note came from a later turn than the post, and the later turn posted nothing: the first
    // message was acknowledged, and once the second is, the gateway's inbox is empty.
    until("the slack gateway's inbox to be empty", || {
        (world.waiting_for_gateway("slack") == 0).then_some(())
    });
    assert_eq!(named(&world.slack, "POST /api/chat.postMessage").len(), 1);
    // The server says what each turn did, in counts.
    for did in [
        "the slack gateway of team ops delivered 1, could not deliver 0, left 0 for a later turn",
        "the slack gateway of team ops delivered 0, could not deliver 1, left 0 for a later turn",
    ] {
        until(did, || world.said().contains(did).then_some(()));
    }
    world.nothing_leaked(&[
        "the migration has landed",
        "is anybody there",
        "payments-api",
        "#general",
    ]);
}

#[test]
fn a_person_on_slack_reaches_a_session_and_its_answer_lands_in_their_thread() {
    let world = World::start("slack-in", "postmark", "[]", standin::postmark);

    // Slack's check of the request URL carries no signature.
    assert_eq!(
        world.post(
            "/gateway/slack",
            r#"{"type":"url_verification","challenge":"a-challenge"}"#,
            &[]
        ),
        (200, json!({"challenge": "a-challenge"}))
    );

    // A message with a signature that is not Slack's reaches nobody.
    let hello = json!({
        "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
        "text": format!("to: {}\n\nis the deploy done?", world.tail), "ts": "1700000001.000100",
    });
    let body = json!({"type": "event_callback", "event_id": "Ev0001", "event": hello}).to_string();
    let timestamp = seconds_now();
    let forged = sign_slack("another-apps-secret", &timestamp, &body).unwrap();
    assert_eq!(
        world.post(
            "/gateway/slack",
            &body,
            &[
                ("X-Slack-Request-Timestamp", &timestamp),
                ("X-Slack-Signature", &forged)
            ]
        ),
        (
            401,
            json!({"error": "the request is not signed by this gateway's Slack app"})
        )
    );
    assert!(
        world
            .said()
            .contains("a request to the Slack endpoint was refused: unsigned")
    );
    assert!(world.inbox(0).is_empty());
    // A megabyte of names, each once, which a parser that is careless with them takes seconds over, is
    // not read at all before it is known to be Slack's: unsigned, it is refused in no time.
    let mut flood = String::from("{");
    for name in 0.. {
        if flood.len() > (1 << 20) - 64 {
            break;
        }
        flood.push_str(&format!("\"{name}\":0,"));
    }
    flood.push_str("\"type\":\"event_callback\"}");
    let began = Instant::now();
    assert!(flood.len() > 1_000_000 && flood.len() <= 1 << 20);
    assert_eq!(world.post("/gateway/slack", &flood, &[]).0, 401);
    assert!(began.elapsed() < Duration::from_secs(1), "{:?}", began.elapsed());

    // The same message, signed, reaches the session its first line names.
    let (status, carried) = world.slack_event("Ev0001", &hello);
    assert_eq!(status, 200, "{carried}");
    assert_eq!(
        (
            &carried["to"],
            &carried["team"],
            &carried["source"],
            &carried["duplicate"]
        ),
        (
            &json!(world.session),
            &json!("ops"),
            &json!("block"),
            &json!(false)
        )
    );
    let inbox = world.inbox(1);
    assert_eq!(inbox[0]["message_id"], carried["message_id"]);
    assert_eq!(
        inbox[0]["body"],
        json!(
            "Slack message from @Pat (U0PERSON01, unverified name) in a direct message:\n\nis the deploy done?\n\n[brigade]\nvia: slack\nfrom: U0PERSON01\nfrom-name: Pat\nchannel: D0PERSON01\nts: 1700000001.000100\npermalink: https://example.slack.com/archives/C0GENERAL1/p1700000001000200\n[/brigade]"
        )
    );
    assert_eq!(inbox[0]["sender"]["session_name"], json!("slack-gateway"));
    // What Slack was asked for that body, and the reaction that tells the person it was sent on.
    assert_eq!(
        named(&world.slack, "GET /api/users.info")[0].target(),
        "/api/users.info?user=U0PERSON01"
    );
    assert_eq!(
        named(&world.slack, "GET /api/chat.getPermalink")[0].target(),
        "/api/chat.getPermalink?channel=D0PERSON01&message_ts=1700000001.000100"
    );
    assert_eq!(
        named(&world.slack, "POST /api/reactions.add")[0].json(),
        json!({"channel": "D0PERSON01", "timestamp": "1700000001.000100", "name": "eyes"})
    );

    // Slack delivers the event again: it is carried once.
    assert_eq!(
        world.slack_event("Ev0001", &hello),
        (200, json!({"duplicate": true}))
    );
    assert_eq!(world.inbox(1).len(), 1);

    // A slash command is answered in its own response: the sessions a person can write to, the gateways
    // left out.
    let command = "command=%2Fbrigade&text=sessions&user_id=U0PERSON01&channel_id=C0GENERAL1&channel_name=general&trigger_id=1234.5678";
    assert_eq!(
        world.slash_command(command),
        (
            200,
            json!({
                "response_type": "ephemeral",
                "text": format!(
                    "Sessions on Brigade team *ops* right now (names and labels are their owners' own words):\n\n• `{}`  payments-api  _active_ · alice@example.com\n\nTo write to one: message @brigade with a first line `to: <the five characters, or a member's label>`, or `/brigade send <five characters or label> <text>`.",
                    world.tail
                ),
            })
        )
    );

    // The session answers the message it received, and the answer goes into the person's thread, with
    // their mention in front.
    let gateway = world.gateway_session("gateway-slack");
    let answer = world.send(&gateway, "yes, an hour ago", carried["message_id"].as_str());
    let posts = asked(&world.slack, "POST /api/chat.postMessage", 1);
    assert_eq!(
        posts[0].json(),
        json!({
            "channel": "D0PERSON01",
            "text": format!("<@U0PERSON01> *payments-api* · alice@example.com\n\nyes, an hour ago{FOOTER}"),
            "unfurl_links": false,
            "unfurl_media": false,
            "thread_ts": "1700000001.000100",
            "metadata": {
                "event_type": "brigade_message",
                "event_payload": {"message_id": answer, "from_session": world.session, "team": "ops"},
            },
        })
    );
    world.nothing_leaked(&["is the deploy done", "an hour ago", "U0PERSON01", "a-challenge"]);
    // Each of Slack's requests that was signed and taken left its line, and one that was not signed its own.
    let said = world.said();
    assert!(said.contains("the slack gateway took a request (200)"), "{said}");
}

#[test]
fn a_message_is_carried_to_its_end_when_slack_has_stopped_waiting_for_the_answer() {
    // Slack is slow to say who the person is, and the sender of the event hangs up as Slack does after
    // its three seconds.
    let world = World::start_with(
        "slack-hangup",
        "postmark",
        "[]",
        |asked| {
            if asked.named() == "GET /api/users.info" {
                std::thread::sleep(Duration::from_millis(1500));
            }
            standin::slack(asked)
        },
        standin::postmark,
    );
    let hello = json!({
        "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
        "text": format!("to: {}\n\nis the deploy done?", world.tail), "ts": "1700000001.000100",
    });
    let authority = world.server.url.strip_prefix("http://").unwrap();
    let mut stream = TcpStream::connect(authority).unwrap();
    stream
        .write_all(&world.slack_event_bytes("Ev0001", &hello))
        .unwrap();
    // The gateway is in the middle of the delivery, waiting for Slack, when the sender goes.
    asked(&world.slack, "GET /api/users.info", 1);
    drop(stream);
    // The delivery Slack makes again while the first is being carried is told to come again, at once and
    // without being gone through: the first decides what becomes of the request.
    assert_eq!(
        world.slack_event("Ev0001", &hello),
        (
            500,
            json!({"error": "an earlier delivery of the request is being carried; deliver it again"})
        )
    );
    // The first is carried to its end though nobody listens for its answer, and the message reaches the
    // session.
    let inbox = world.inbox(1);
    assert!(
        inbox[0]["body"]
            .as_str()
            .unwrap()
            .starts_with("Slack message from @Pat (U0PERSON01, unverified name) in a direct message:")
    );
    asked(&world.slack, "POST /api/reactions.add", 1);
    // The delivery Slack makes after that is one of a request the gateway is done with, and the message
    // has reached the session exactly once.
    assert_eq!(
        until("the request to be done with", || {
            let answer = world.slack_event("Ev0001", &hello);
            (answer.0 == 200).then_some(answer)
        }),
        (200, json!({"duplicate": true}))
    );
    assert_eq!(world.inbox(1).len(), 1);
    assert_eq!(named(&world.slack, "GET /api/users.info").len(), 1);
    world.nothing_leaked(&["is the deploy done"]);
}

/// A Slack whose answer about a person is plain, slow by a second, or held back for as long as the test
/// says: `0`, `1` and `2` of `how`.
fn slack_slow_about_people(how: &Arc<AtomicU8>) -> impl Fn(&Asked) -> Said + Send + Sync + 'static {
    let how = Arc::clone(how);
    move |asked| {
        if asked.named() == "GET /api/users.info" {
            match how.load(Ordering::SeqCst) {
                1 => std::thread::sleep(Duration::from_secs(1)),
                2 => {
                    let held = Instant::now();
                    while how.load(Ordering::SeqCst) == 2 && held.elapsed() < Duration::from_secs(60) {
                        std::thread::sleep(Duration::from_millis(20));
                    }
                }
                _ => {}
            }
        }
        standin::slack(asked)
    }
}

#[test]
fn no_message_is_lost_to_a_stop_in_the_middle_of_its_carrying() {
    let how = Arc::new(AtomicU8::new(0));
    let world = World::start_with(
        "slack-stop",
        "postmark",
        "[]",
        slack_slow_about_people(&how),
        standin::postmark,
    );
    let message = |ts: &str, text: &str| {
        json!({
            "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
            "text": format!("to: {}\n\n{text}", world.tail), "ts": ts,
        })
    };
    // Slack delivers an event and hangs up; the gateway is waiting for Slack's word about the person
    // when the server is told to stop.
    let deliver_and_hang_up = |world: &World, id: &str, event: &Value, asked_before: usize| {
        let authority = world.server.url.strip_prefix("http://").unwrap();
        let mut stream = TcpStream::connect(authority).unwrap();
        stream.write_all(&world.slack_event_bytes(id, event)).unwrap();
        asked(&world.slack, "GET /api/users.info", asked_before + 1);
        drop(stream);
    };

    // A stop waits for a message that is being carried, within the time it gives every request: the
    // server that goes has carried it, and the one that starts knows it as done.
    let (first, second) = (
        message("1700000001.000100", "is the deploy done?"),
        message("1700000002.000100", "and the migration?"),
    );
    how.store(1, Ordering::SeqCst);
    deliver_and_hang_up(&world, "Ev0001", &first, 0);
    let world = world.stopped_and_started();
    assert_eq!(named(&world.slack, "POST /api/reactions.add").len(), 1);
    assert_eq!(world.inbox(1).len(), 1);
    assert_eq!(
        world.slack_event("Ev0001", &first),
        (200, json!({"duplicate": true}))
    );

    // A stop that cannot wait so long cuts the carrying short: here Slack says nothing about the person
    // for longer than the server waits. Nothing says the event came, so the delivery Slack makes again
    // is gone through in full, and the message reaches the session, once.
    how.store(2, Ordering::SeqCst);
    deliver_and_hang_up(&world, "Ev0002", &second, 1);
    let world = world.stopped_and_started();
    how.store(0, Ordering::SeqCst);
    assert_eq!(world.inbox(1).len(), 1);
    let (status, carried) = world.slack_event("Ev0002", &second);
    assert_eq!((status, &carried["duplicate"]), (200, &json!(false)), "{carried}");
    let inbox = world.inbox(2);
    assert_eq!(inbox.len(), 2);
    assert_eq!(inbox[1]["message_id"], carried["message_id"]);
    assert_eq!(
        world.slack_event("Ev0002", &second),
        (200, json!({"duplicate": true}))
    );
    assert_eq!(world.inbox(2).len(), 2);
    world.nothing_leaked(&["is the deploy done", "and the migration"]);
}

#[test]
fn more_requests_than_the_gateways_answer_at_once_are_told_to_come_again() {
    // Slack says nothing about the person until the test lets it: every request that is being carried
    // stays where it is.
    let how = Arc::new(AtomicU8::new(2));
    let world = World::start_with(
        "slack-places",
        "postmark",
        "[]",
        slack_slow_about_people(&how),
        standin::postmark,
    );
    let message = |at: usize| {
        json!({
            "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
            "text": format!("to: {}\n\nmessage {at}", world.tail), "ts": format!("17000000{at:02}.000100"),
        })
    };
    const AT_ONCE: usize = 64;
    let answers = std::thread::scope(|scope| {
        let carrying: Vec<_> = (0..AT_ONCE)
            .map(|at| {
                let (world, event) = (&world, message(at));
                scope.spawn(move || world.slack_event(&format!("Ev{at:04}"), &event))
            })
            .collect();
        // Every place is taken once each of them waits for Slack.
        asked(&world.slack, "GET /api/users.info", AT_ONCE);
        // One more is answered at once, with a fixed text and a status that Slack takes for "again".
        let began = Instant::now();
        assert_eq!(
            world.slack_event("Ev9999", &message(99)),
            (
                503,
                json!({"error": "the gateways are busy; deliver the request again"})
            )
        );
        assert!(began.elapsed() < Duration::from_secs(5));
        // What the contract serves is none of the gateways' business, and is served.
        let (status, _) = world.server.post(None, "/v1/info", &json!({}));
        assert_eq!(status, 200);
        how.store(0, Ordering::SeqCst);
        carrying
            .into_iter()
            .map(|carried| carried.join().unwrap().0)
            .collect::<Vec<_>>()
    });
    // Every one of the sixty-four was answered, carried or told what the engine's limits say, and the
    // one that was turned away finds a place when it comes again.
    assert_eq!(answers, vec![200; AT_ONCE]);
    assert_eq!(world.slack_event("Ev9999", &message(99)).0, 200);
    assert_eq!(named(&world.slack, "GET /api/users.info").len(), AT_ONCE + 1);
}

#[test]
fn mail_reaches_a_session_through_postmark_and_a_session_reaches_a_person() {
    let world = World::start("postmark", "postmark", r#"["@example.com"]"#, standin::postmark);
    let deploy = postmark_mail(
        "pm-in-1",
        "pat@example.com",
        "deploy?",
        "to: alice@example.com\n\nshall we deploy today?",
    );

    // A webhook that does not carry the secret is refused with the status that ends Postmark's retries.
    let refused = (
        403,
        json!({"error": "the request does not carry this connector's webhook secret"}),
    );
    assert_eq!(world.post("/gateway/mail/postmark", &deploy, &[]), refused);
    let wrong = format!("Basic {}", base64(b"brigade:not-the-password"));
    assert_eq!(
        world.post("/gateway/mail/postmark", &deploy, &[("Authorization", &wrong)]),
        refused
    );
    assert_eq!(
        world.post("/gateway/mail/postmark/not-the-password", &deploy, &[]),
        refused
    );
    // And it is refused by what comes before its body, none of which is read: the answer comes though
    // not one of the eight megabytes the request announces was sent.
    let authority = world.server.url.strip_prefix("http://").unwrap();
    let announced = format!(
        "POST /gateway/mail/postmark HTTP/1.1\r\nHost: {authority}\r\nContent-Type: application/json\r\nContent-Length: 8388608\r\n\r\n"
    );
    let asked_at = Instant::now();
    assert_eq!(world.raw(announced.as_bytes()), refused);
    assert!(asked_at.elapsed() < Duration::from_secs(10));
    // The four refusals are one line of the server's: a stranger cannot fill them.
    let said = world.said();
    assert_eq!(
        said.matches("a webhook of postmark was refused").count(),
        1,
        "{said}"
    );
    assert!(
        said.contains("a webhook of postmark was refused: 403\n"),
        "{said}"
    );

    // With the secret as the password Postmark is given in the URL, the mail reaches the session.
    let basic = format!("Basic {}", base64(format!("brigade:{POSTMARK_HOOK}").as_bytes()));
    let (status, carried) = world.post("/gateway/mail/postmark", &deploy, &[("Authorization", &basic)]);
    assert_eq!(status, 200, "{carried}");
    assert_eq!(
        (&carried["to"], &carried["source"], &carried["duplicate"]),
        (&json!(world.session), &json!("block"), &json!(false))
    );
    let inbox = world.inbox(1);
    assert_eq!(inbox[0]["message_id"], carried["message_id"]);
    assert_eq!(
        inbox[0]["body"],
        json!(
            "Email from Pat Example <pat@example.com> (unverified), subject \"deploy?\":\n\nshall we deploy today?\n\n[brigade]\nvia: email\nfrom: pat@example.com\nsubject: deploy?\nmail-id: <pm-in-1@example.com>\nreceived: 2026-11-12T10:00:00.000Z\n[/brigade]"
        )
    );
    assert_eq!(inbox[0]["sender"]["session_name"], json!("mail-gateway"));
    // Postmark delivers it again: it is carried once.
    assert_eq!(
        world.post("/gateway/mail/postmark", &deploy, &[("Authorization", &basic)]),
        (200, json!({"duplicate": true}))
    );

    // A sender the team does not list is dropped, and answered nothing: no bounce leaves for them.
    let stranger = postmark_mail(
        "pm-in-2",
        "mallory@elsewhere.test",
        "hello",
        "to: alice@example.com\n\nlet me in",
    );
    let bearer = format!("Bearer {POSTMARK_HOOK}");
    let state = || std::fs::read(world.scratch.path("data/gateway/state.json")).unwrap();
    let before = state();
    let dropped = (
        200,
        json!({"ignored": "the sender is not on the team's allowed-senders list"}),
    );
    assert_eq!(
        world.post("/gateway/mail/postmark", &stranger, &[("Authorization", &bearer)]),
        dropped
    );
    // Nothing is written down of a mail that is dropped: delivered again it is dropped again, and the
    // state's file is what it was, to the byte, however many strangers write.
    assert_eq!(
        world.post("/gateway/mail/postmark", &stranger, &[("Authorization", &bearer)]),
        dropped
    );
    assert_eq!(state(), before);
    assert!(world.mail.asked().is_empty());
    assert!(world.said().contains(
        "the mail gateway of team ops dropped a mail: its sender is not on the team's allowed-senders list"
    ));

    // A mail that names nobody is answered by mail, through the provider, with the reason and the roster.
    // The secret may also be the path's last segment.
    let unaddressed = postmark_mail("pm-in-3", "pat@example.com", "hello", "is anybody there?");
    assert_eq!(
        world.post(
            &format!("/gateway/mail/postmark/{POSTMARK_HOOK}"),
            &unaddressed,
            &[]
        ),
        (200, json!({"bounced": "unaddressed"}))
    );
    let bounce = asked(&world.mail, "POST /email", 1).remove(0);
    assert_eq!(bounce.header("x-postmark-server-token"), Some(API_KEY));
    let mail = bounce.json();
    assert_eq!(
        (&mail["From"], &mail["To"], &mail["Subject"], &mail["ReplyTo"]),
        (
            &json!("Ops <ops@example.com>"),
            &json!("pat@example.com"),
            &json!("Re: hello"),
            &json!("ops@in.example.com")
        )
    );
    assert_eq!(
        mail["Headers"],
        json!([
            {"Name": "Auto-Submitted", "Value": "auto-replied"},
            {"Name": "In-Reply-To", "Value": "<pm-in-3@example.com>"},
            {"Name": "References", "Value": "<pm-in-3@example.com>"},
        ])
    );
    assert_eq!(
        mail["TextBody"],
        json!(format!(
            "Your email was not delivered to a Brigade session.\n\nNo session was named. Start your email with a line \"to: <session>\" (see the list below), or reply to an email a session sent you.\n\nSessions on Brigade team ops right now (names and labels are their owners' own words):\n\n  {}  payments-api  active  alice@example.com\n\nTo write to one, send an email to ops@example.com whose first line is:\n\n  to: <the five characters in the first column, or a member's label>\n",
            world.tail
        ))
    );

    // A session writes to a person: the provider is asked to send the mail the rules wrote.
    let gateway = world.gateway_session("gateway-email");
    let out = world.send(&gateway, "to: Person@Example.com\n\nthe release is out", None);
    let sent = asked(&world.mail, "POST /email", 2).remove(1).json();
    assert_eq!(
        (
            &sent["From"],
            &sent["To"],
            &sent["Subject"],
            &sent["MessageStream"]
        ),
        (
            &json!("Ops <ops@example.com>"),
            &json!("person@example.com"),
            &json!("[brigade/ops] the release is out"),
            &json!("outbound")
        )
    );
    assert_eq!(sent["ReplyTo"], json!(format!("ops+{out}@in.example.com")));
    assert_eq!(
        sent["Headers"],
        json!([{"Name": "Auto-Submitted", "Value": "auto-generated"}])
    );
    let written = sent["TextBody"].as_str().unwrap();
    assert!(
        written
            .starts_with("payments-api · alice@example.com\n\nthe release is out\n\n[brigade]\nteam: ops\n"),
        "{written}"
    );
    assert!(
        written.ends_with("[/brigade]\n\nReply to this email to answer. To write to another session, send a new email to ops@example.com whose first line is \"to: <session>\".\n"),
        "{written}"
    );

    // The person answers that mail by replying to it: the Reply-To address carries the message's id, which on
    // this server is not a UUID, and that alone brings the answer back to the session that wrote, as a reply.
    let answer = json!({
        "MessageID": "pm-in-4", "MessageStream": "inbound", "From": "person@example.com",
        "FromFull": {"Email": "person@example.com", "Name": ""},
        "ToFull": [{"Email": "ops@in.example.com", "Name": "", "MailboxHash": out}],
        "OriginalRecipient": format!("ops+{out}@in.example.com"), "Subject": "Re: [brigade/ops] the release is out",
        "Date": "2026-11-12T10:05:00Z", "TextBody": "thanks, updating now",
        "Headers": [{"Name": "Message-ID", "Value": "<pm-in-4@example.com>"}],
    })
    .to_string();
    let (status, threaded) = world.post("/gateway/mail/postmark", &answer, &[("Authorization", &basic)]);
    assert_eq!(status, 200, "{threaded}");
    assert_eq!(
        (&threaded["to"], &threaded["source"]),
        (&json!(world.session), &json!("reply-address"))
    );
    let inbox = world.inbox(2);
    let arrived = inbox
        .iter()
        .find(|message| message["message_id"] == threaded["message_id"])
        .expect("the person's answer in the session's inbox");
    assert_eq!(arrived["reply_to"], json!(out));

    // The session answers the mail it received: the answer goes to the person, in their thread.
    world.send(&gateway, "yes, at noon", carried["message_id"].as_str());
    let reply = asked(&world.mail, "POST /email", 3).remove(2).json();
    assert_eq!(
        (&reply["To"], &reply["Subject"]),
        (&json!("pat@example.com"), &json!("Re: deploy?"))
    );
    assert_eq!(
        reply["Headers"],
        json!([
            {"Name": "Auto-Submitted", "Value": "auto-generated"},
            {"Name": "In-Reply-To", "Value": "<pm-in-1@example.com>"},
            {"Name": "References", "Value": "<pm-in-1@example.com>"},
        ])
    );
    until("the mail gateway's inbox to be empty", || {
        (world.waiting_for_gateway("email") == 0).then_some(())
    });

    // A webhook that is Postmark's may carry a whole mail, far longer than anything else the server
    // reads: two megabytes of it are read, and what a Brigade message can hold of them is carried.
    let long = postmark_mail(
        "pm-in-5",
        "pat@example.com",
        "the logs",
        &format!(
            "to: alice@example.com\n\n{}",
            "0123456789abcdef\n".repeat(2 << 16)
        ),
    );
    assert!(long.len() > 2 << 20);
    let (status, carried) = world.post("/gateway/mail/postmark", &long, &[("Authorization", &basic)]);
    assert_eq!((status, &carried["duplicate"]), (200, &json!(false)), "{carried}");
    let inbox = world.inbox(3);
    let arrived = inbox
        .iter()
        .find(|message| message["message_id"] == carried["message_id"])
        .expect("the long mail in the session's inbox");
    assert!(
        arrived["body"]
            .as_str()
            .unwrap()
            .contains("[… cut by the mail gateway: the email was longer than a Brigade message may be]")
    );
    world.nothing_leaked(&[
        "shall we deploy",
        "let me in",
        "the release is out",
        "updating now",
        "pat@example.com",
        "person@example.com",
        "mallory",
        "elsewhere.test",
    ]);
}

#[test]
fn a_session_reaches_a_person_through_resend_and_resends_webhook_is_fetched() {
    // Resend's webhook carries an id; the mail itself is fetched.
    let world = World::start("resend", "resend", "[]", |asked| {
        if asked.named() == "GET /emails/receiving/re-in-1" {
            return Said::json(
                200,
                &json!({
                    "id": "re-in-1", "from": "Pat Example <pat@example.com>", "to": ["ops@in.example.com"],
                    "subject": "deploy?", "text": "to: alice@example.com\n\nshall we deploy today?",
                    "message_id": "<re-in-1@example.com>", "headers": {},
                    "created_at": "2026-11-12T10:00:00.000Z",
                }),
            );
        }
        standin::resend(asked)
    });
    let gateway = world.gateway_session("gateway-email");
    let out = world.send(
        &gateway,
        "to: person@example.com\nsubject: the release\n\nit is out",
        None,
    );
    let sends = asked(&world.mail, "POST /emails", 1);
    assert_eq!(
        sends[0].header("authorization"),
        Some(format!("Bearer {API_KEY}").as_str())
    );
    let sent = sends[0].json();
    assert_eq!(
        (&sent["from"], &sent["to"], &sent["subject"], &sent["reply_to"]),
        (
            &json!("Ops <ops@example.com>"),
            &json!(["person@example.com"]),
            &json!("the release"),
            &json!([format!("ops+{out}@in.example.com")])
        )
    );
    assert_eq!(sent["headers"], json!({"Auto-Submitted": "auto-generated"}));
    assert!(
        sent["text"]
            .as_str()
            .unwrap()
            .starts_with("payments-api · alice@example.com\n\nit is out\n\n[brigade]\nteam: ops\n")
    );

    // A webhook Resend did not sign is refused; a signed one is fetched and carried.
    let event = json!({"type": "email.received", "data": {"email_id": "re-in-1"}}).to_string();
    let timestamp = seconds_now();
    let signed = |secret: &str| sign(secret, "msg_1", &timestamp, &event).unwrap();
    let headers = |signature: &str| -> Vec<(&'static str, String)> {
        vec![
            ("svix-id", "msg_1".to_owned()),
            ("svix-timestamp", timestamp.clone()),
            ("svix-signature", signature.to_owned()),
        ]
    };
    let post = |signature: &str| {
        let headers = headers(signature);
        let headers: Vec<(&str, &str)> = headers
            .iter()
            .map(|(name, value)| (*name, value.as_str()))
            .collect();
        world.post("/gateway/mail/resend", &event, &headers)
    };
    // Resend's webhook is an id and an envelope, and only reading it says whose it is: a megabyte of it
    // is read, and a longer one is refused as too long.
    let padded = format!("{{\"padding\":\"{}\"}}", "x".repeat(1 << 20));
    assert_eq!(
        world.post("/gateway/mail/resend", &padded, &[]),
        (
            413,
            json!({"error": "the request's body is longer than this endpoint reads"})
        )
    );
    assert_eq!(
        post(&signed("whsec_YW5vdGhlci1zZWNyZXQ=")),
        (401, json!({"error": "the webhook signature does not verify"}))
    );
    assert!(named(&world.mail, "GET /emails/receiving/re-in-1").is_empty());
    let (status, carried) = post(&signed(RESEND_HOOK));
    assert_eq!(status, 200, "{carried}");
    assert_eq!(carried["to"], json!(world.session));
    assert_eq!(
        named(&world.mail, "GET /emails/receiving/re-in-1")[0].header("authorization"),
        Some(format!("Bearer {API_KEY}").as_str())
    );
    let inbox = world.inbox(1);
    assert!(
        inbox[0]["body"]
            .as_str()
            .unwrap()
            .starts_with("Email from Pat Example <pat@example.com> (unverified), subject \"deploy?\":\n\nshall we deploy today?\n\n[brigade]\n")
    );

    // The session's answer threads under the person's mail.
    world.send(&gateway, "yes, at noon", carried["message_id"].as_str());
    let reply = asked(&world.mail, "POST /emails", 2).remove(1).json();
    assert_eq!(
        (&reply["to"], &reply["subject"]),
        (&json!(["pat@example.com"]), &json!("Re: deploy?"))
    );
    assert_eq!(
        reply["headers"],
        json!({
            "Auto-Submitted": "auto-generated",
            "In-Reply-To": "<re-in-1@example.com>",
            "References": "<re-in-1@example.com>",
        })
    );
    world.nothing_leaked(&["it is out", "shall we deploy", "at noon", "pat@example.com"]);
}

#[test]
fn a_restart_keeps_the_gateways_memberships_sessions_and_thread_rows() {
    let world = World::start("restart", "postmark", "[]", standin::postmark);
    let before = (world.gateway("gateway-slack"), world.gateway("gateway-email"));
    // A person writes on Slack, and the session's answer is not sent before the server ends.
    let (status, carried) = world.direct_message(
        "Ev0001",
        "1700000001.000100",
        &format!("to: {}\n\nis the deploy done?", world.tail),
    );
    assert_eq!(status, 200, "{carried}");
    world.inbox(1);
    world.nothing_leaked(&["is the deploy done"]);

    // The server ends as a power cut ends it, and is started again on the same directory.
    let world = world.restarted();
    let after = (world.gateway("gateway-slack"), world.gateway("gateway-email"));
    // The same members with the same sessions: nothing joined, and nothing registered anew.
    for (was, is) in [(&before.0, &after.0), (&before.1, &after.1)] {
        assert_eq!(
            (&was["session_id"], &was["principal_ref"]),
            (&is["session_id"], &is["principal_ref"])
        );
    }
    let members = world.server.ok(
        &world.credential,
        "/v1/team/members",
        &json!({"team_ref": world.team_ref}),
    );
    assert_eq!(members["members"].as_array().unwrap().len(), 3, "{members}");

    // The thread row of the person's message is still there: the session's answer finds their thread.
    let gateway = text(&after.0, "session_id");
    world.send(&gateway, "yes, an hour ago", carried["message_id"].as_str());
    let posts = asked(&world.slack, "POST /api/chat.postMessage", 1);
    let post = posts[0].json();
    assert_eq!(
        (&post["channel"], &post["thread_ts"]),
        (&json!("D0PERSON01"), &json!("1700000001.000100"))
    );
    assert!(post["text"].as_str().unwrap().starts_with("<@U0PERSON01> "));
    // And the event Slack delivered before the restart is still known as delivered.
    assert_eq!(
        world.direct_message("Ev0001", "1700000001.000100", "to: nobody\n\nagain"),
        (200, json!({"duplicate": true}))
    );
    let said = world.said();
    assert!(!said.contains("joined team"), "{said}");
    assert!(!said.contains("registered its session"), "{said}");
    world.nothing_leaked(&["an hour ago"]);
}

#[test]
fn the_gateways_join_again_by_themselves_once_their_file_holds_the_rotated_secret() {
    let world = World::start("rotation", "postmark", "[]", standin::postmark);
    let before = (
        world.gateway_session("gateway-slack"),
        world.gateway_session("gateway-email"),
    );
    let principals = (
        text(&world.gateway("gateway-slack"), "principal_ref"),
        text(&world.gateway("gateway-email"), "principal_ref"),
    );
    let team = json!({"team_ref": world.team_ref});

    // The team's owner rotates the join secret. A rotation alone changes nothing for a gateway: it is the
    // member it was, with the session it had.
    let rotated = world
        .server
        .ok(&world.credential, "/v1/team/rotate-secret", &team);
    let new_secret = text(&rotated, "join_secret");
    assert_eq!(world.gateway_session("gateway-slack"), before.0);
    assert_eq!(world.gateway_session("gateway-email"), before.1);

    // The operator replaces the file both gateways are configured with, and the owner then revokes
    // everyone who joined with the old secret, which the two gateways did.
    std::fs::write(world.scratch.path("ops.secret"), format!("{new_secret}\n")).unwrap();
    let swept = world.server.ok(
        &world.credential,
        "/v1/team/revoke-member",
        &json!({"team_ref": world.team_ref, "joined_secret_version_lte": 1}),
    );
    assert_eq!(
        (&swept["revoked"], &swept["sessions_closed"]),
        (&json!(2), &json!(2))
    );

    // Each gateway finds itself refused at its next turn, reads the file again and joins with what it
    // holds now and the credential it has: the member it was, with the session it had, and the server was
    // never stopped.
    let said = until("both gateways to say they joined again", || {
        let said = world.said();
        (said.contains("the slack gateway joined team ops again")
            && said.contains("the mail gateway joined team ops again"))
        .then_some(said)
    });
    assert!(!said.contains("could not join its team again"), "{said}");
    assert!(!said.contains("as a new member"), "{said}");
    let after = (world.gateway("gateway-slack"), world.gateway("gateway-email"));
    assert_eq!(
        (text(&after.0, "session_id"), text(&after.0, "principal_ref")),
        (before.0.clone(), principals.0)
    );
    assert_eq!(
        (text(&after.1, "session_id"), text(&after.1, "principal_ref")),
        (before.1.clone(), principals.1)
    );
    let after = (before.0, before.1);
    // The owner and the two gateways are the team's members, the gateways of the secret's second version.
    let members = world.server.ok(&world.credential, "/v1/team/members", &team);
    let mut versions: Vec<(String, u64)> = members["members"]
        .as_array()
        .unwrap()
        .iter()
        .map(|member| {
            (
                text(member, "human_label"),
                member["joined_secret_version"].as_u64().unwrap(),
            )
        })
        .collect();
    versions.sort();
    assert_eq!(
        versions,
        [
            ("alice@example.com".to_owned(), 1),
            ("mail gateway".to_owned(), 2),
            ("slack gateway".to_owned(), 2)
        ]
    );

    // And they carry again, from the sessions they have now.
    world.send(&after.0, "to: #general\n\nstill here after the rotation", None);
    let posts = asked(&world.slack, "POST /api/chat.postMessage", 1);
    assert_eq!(posts[0].json()["channel"], json!("C0GENERAL1"));
    // Neither the secret the team had nor the one it has now is in anything the server wrote.
    world.nothing_leaked(&[
        new_secret.rsplit('.').next().unwrap(),
        "still here after the rotation",
    ]);
}

#[test]
fn a_configuration_that_does_not_read_ends_the_start_with_its_reason() {
    let scratch = Scratch::new("gateway-config");
    let arguments = |file: &str, content: &str| {
        let path = scratch.path(file);
        std::fs::write(&path, content).unwrap();
        let mut arguments = log_arguments("file", &scratch);
        arguments.extend(["--gateway-config".to_owned(), path.display().to_string()]);
        arguments
    };
    let token = scratch.path("token");
    std::fs::write(&token, format!("{BOT_TOKEN}\n")).unwrap();
    let token = token.display().to_string();

    let refused = Server::try_start(&scratch, &arguments("not-json.json", "{\"slack\": "))
        .err()
        .expect("a start that ends");
    assert!(
        refused.contains("does not read as a gateway configuration"),
        "{refused}"
    );
    let no_team = json!({"slack": {"bot_token_file": token, "signing_secret_file": token, "teams": []}});
    let refused = Server::try_start(&scratch, &arguments("no-team.json", &no_team.to_string()))
        .err()
        .expect("a start that ends");
    assert!(refused.contains("slack names no team"), "{refused}");
    // A file that holds something other than a join secret is found before the server listens.
    let not_a_secret = json!({
        "slack": {
            "bot_token_file": token, "signing_secret_file": token,
            "teams": [{"join_secret_file": token}],
        },
    });
    let refused = Server::try_start(&scratch, &arguments("no-secret.json", &not_a_secret.to_string()))
        .err()
        .expect("a start that ends");
    assert!(refused.contains("does not hold a join secret"), "{refused}");
    assert!(!refused.contains("MUST-NOT-LEAK"), "{refused}");
    // Nothing was listened on and no log was opened for any of them.
    assert!(!scratch.path("data/log").exists());

    // A file that names neither gateway is a server with none.
    let server = Server::start(&scratch, &arguments("none.json", "{}"));
    let (status, envelope) = server.post(None, "/gateway/slack", &json!({}));
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (404, "not_found")
    );
    server.kill();

    // A team that cannot be joined is noted before the server listens, and the server starts all the
    // same: the loop tries the join again.
    let unknown = scratch.path("unknown-team.secret");
    std::fs::write(
        &unknown,
        format!("brg1.{}.{}\n", "0123456789abcdef".repeat(2), "f".repeat(32)),
    )
    .unwrap();
    let refused_join = json!({
        "slack": {
            "bot_token_file": token, "signing_secret_file": token, "api": "http://127.0.0.1:1/api",
            "teams": [{"join_secret_file": unknown.display().to_string()}],
        },
    });
    let server = Server::start(
        &scratch,
        &arguments("refused-join.json", &refused_join.to_string()),
    );
    let said = until("the server to say where it listens", || {
        let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
        said.contains("listening on").then_some(said)
    });
    let refused = said
        .find("could not finish a turn: unauthorized: join secret rejected")
        .expect("the refusal, said");
    assert!(said.find("listening on").is_some_and(|at| refused < at), "{said}");
    assert!(
        !said.contains("MUST-NOT-LEAK") && !said.contains(&"f".repeat(32)),
        "{said}"
    );
    server.kill();
}
