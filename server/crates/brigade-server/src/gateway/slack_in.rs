//! Slack, inbound: the Events API and the slash command (`supabase/functions/slack-in/index.ts`).
//!
//! A person's direct message, mention, reply in a Brigade thread or `/brigade` command becomes a Brigade
//! message from the gateway session of the team the session it names is in. What cannot be sent on is
//! answered where the person is. The steps are the Edge Function's, and the numbers beside them are the ones
//! the port's reference gave them; what each step says and decides is `brigade-gateway`'s.
//!
//! One thing is the function's own and kept: the lookups of the person, the channel and the link come
//! before the send, so a slow Slack makes a slow answer. Three are not.
//!
//! Nothing of a request is read before it is known to be Slack's, but the handshake Slack makes when the
//! request URL is saved, and that only of a body small enough to be one. The function looked for the
//! handshake in any body, with a parser that anyone could keep busy.
//!
//! And no message is lost between its coming and its going on. The function wrote a request down as
//! received before it carried it, and answered every later delivery of it as a duplicate. Here it is
//! written down once the gateway is done with it ([`Gateways::carry`]).
//!
//! And one team cannot stop Slack for the others. A team's owner can end its gateway's membership, by
//! revoking it or by revoking everyone who joined with a secret it rotated, and until the gateway has
//! joined again its team's roster is refused. The function failed a request when any served team's roster
//! could not be read. Here such a team is left out of the request: the other teams' sessions are found and
//! written to as before, and a person who is shown the roster, or where their message went, is told that
//! some sessions cannot be reached now.

use brigade_core::Timestamp;
use brigade_gateway::connector::Request;
use brigade_gateway::notes::{self, slack as ns};
use brigade_gateway::providers::Slack;
use brigade_gateway::slack::SIGNATURE_TOLERANCE_SECONDS;
use brigade_gateway::{
    Answer, InboundSlack, ParsedRequest, SlackContext, SlackInbound, TeamSessions, resolve_recipient_across,
    resolve_slack_inbound, slack_body, slack_roster, verify_slack_signature,
};
use ring::rand::{SecureRandom, SystemRandom};

use super::endpoint::Received;
use super::served::{Sent, carried_before, detail};
use super::state::Kind;
use super::{Gateways, SlackSide, plain, presence, threads};
use crate::note;

/// The longest body that is looked at for Slack's handshake before its signature is checked. The handshake
/// is a token, a nonce and a type: a few hundred bytes.
const HANDSHAKE_BYTES: usize = 4 << 10;

/// What Slack is answered when the server failed on a request. The text is fixed: why it failed is the
/// server's to know, and is said in its own lines.
pub const FAILED: &str = "the gateway could not carry the request; it can be delivered again";

/// What Slack is answered for a request whose earlier delivery is still being carried.
pub const BEING_CARRIED: &str = "an earlier delivery of the request is being carried; deliver it again";

/// The id of a slash command Slack gave no `trigger_id`: sixteen bytes of the operating system's
/// randomness, as hexadecimal. It only has to be one no other command has, so where the system gives no
/// randomness the time stands in.
fn new_trigger() -> String {
    let mut bytes = [0_u8; 16];
    match SystemRandom::new().fill(&mut bytes) {
        Ok(()) => brigade_core::hash::hex(&bytes),
        Err(_) => format!("{:x}", note::now().0),
    }
}

/// The challenge of Slack's check of the request URL, when a small body is one. It is read with a parser
/// that takes time in proportion to what it reads, and nothing else is ever read of a body that is not
/// signed.
fn handshake(content_type: &str, raw: &str) -> Option<String> {
    if raw.len() > HANDSHAKE_BYTES || !content_type.contains("application/json") {
        return None;
    }
    let request: serde_json::Value = serde_json::from_str(raw).ok()?;
    if request.get("type")?.as_str()? != "url_verification" {
        return None;
    }
    request.get("challenge")?.as_str().map(str::to_owned)
}

/// Answers one request to Slack's endpoint.
pub async fn answer(gateways: &Gateways, slack: &SlackSide, received: &Received, now: Timestamp) -> Answer {
    // 1. Only a POST.
    if received.method != "POST" {
        return notes::answer_method_not_allowed();
    }
    let raw = received.body.as_str();
    let headers = received.headers();
    let request = Request {
        path: &received.path,
        headers: &headers,
    };
    let content_type = request.header("content-type").unwrap_or_default();
    // 2. The challenge, before the signature: Slack sends it when the request URL is saved, which can be
    // before the gateway has the app's secret.
    if let Some(challenge) = handshake(&content_type, raw) {
        return ns::answer_challenge(&challenge);
    }
    // 3. The signature, over the body as it came. Until it holds, the body is not read.
    if !verify_slack_signature(
        slack.signing_secret(),
        &request,
        raw,
        now,
        SIGNATURE_TOLERANCE_SECONDS,
    ) {
        gateways.turned_away("a request to the Slack endpoint was refused: unsigned", now);
        return ns::answer_unsigned();
    }
    // 4. The bot's own user id, from auth.test; any failure is "not known".
    let bot = slack.bot_user(gateways).await;
    // 5. What the request is.
    let message = match ns::parse_request(&content_type, raw, bot.as_deref(), new_trigger) {
        ParsedRequest::UrlVerification { challenge } => return ns::answer_challenge(&challenge),
        ParsedRequest::Ignore { reason } => return ns::answer_ignored(&reason),
        ParsedRequest::Event { message } | ParsedRequest::Command { message, .. } => message,
    };
    match carry(gateways, slack, &message, now).await {
        Ok(answer) => answer,
        Err(error) => {
            // Slack is answered a failure and delivers the request again, and that delivery is gone
            // through in full: nothing says the request came. Why it failed is said in the server's own
            // lines, sparingly, and Slack is told a fixed text: the sentence is the engine's, the store's
            // or this crate's own, and the store's names the server's directory.
            gateways.turned_away(&format!("the Slack endpoint failed: {}", plain(&error)), now);
            notes::answer_failed(FAILED)
        }
    }
}

/// Answers a person where they are. A post that fails is not tried again.
async fn tell(gateways: &Gateways, slack: &SlackSide, tell: ns::Tell) -> Answer {
    match tell {
        ns::Tell::Response(answer) => answer,
        ns::Tell::Post(post) => {
            let _ = gateways.asked(&slack.api.post_message_request(&post)).await;
            ns::answer_told()
        }
        ns::Tell::Ephemeral(post) => {
            let _ = gateways.asked(&slack.api.post_ephemeral_request(&post)).await;
            ns::answer_told()
        }
    }
}

/// Carries one message a person wrote. An error is a failure the request is answered with, after which
/// Slack delivers it again.
async fn carry(
    gateways: &Gateways,
    slack: &SlackSide,
    m: &InboundSlack,
    now: Timestamp,
) -> Result<Answer, String> {
    // 6. Where the message goes, before anything is recorded or read. What is not the gateway's is never
    // written down.
    let resolution = {
        let store = gateways.store.lock().await;
        resolve_slack_inbound(m, &*store)
    };
    if let SlackInbound::Ignore { reason } = &resolution {
        return Ok(ns::answer_ignored(reason));
    }
    // 7. Once only. A delivery of a request that is being carried now is not gone through beside it: Slack
    // is told to come again, and by then the first has decided what the request became. One the gateway
    // is done with is a duplicate.
    let key = ns::received_key(&m.event_id);
    let Some(_carrying) = gateways.carry(&key) else {
        return Ok(notes::answer_failed(BEING_CARRIED));
    };
    if gateways.is_done(&key).await {
        return Ok(notes::answer_duplicate());
    }
    // 8. The gateways, and 9. every served team's roster, its own gateway session left out. A team whose
    // gateway is no longer the member it was is left out of this request: its roster cannot be read, and
    // that is its own affair and not the other teams'. Any other refusal of a roster is a fault, and
    // fails the request so that Slack delivers it again.
    let configured = gateways.served(Kind::Slack).await;
    if configured.is_empty() {
        return Err(ns::NOT_INSTALLED.to_owned());
    }
    let (mut served, mut rosters, mut left_out) = (Vec::new(), Vec::new(), Vec::new());
    for gateway in configured {
        match gateway.sessions().await {
            Ok(roster) => {
                served.push(gateway);
                rosters.push(roster);
            }
            Err(refusal) if presence::lost(&refusal) => left_out.push(refusal.message),
            Err(refusal) => return Err(refusal.message),
        }
    }
    // With every team left out the request fails, as it does for a gateway that serves no team: Slack
    // delivers it again, and by then the gateway may have joined again.
    if served.is_empty() {
        let why = left_out.into_iter().next();
        return Err(why.unwrap_or_else(|| ns::NOT_INSTALLED.to_owned()));
    }
    let teams: Vec<TeamSessions<'_>> = served
        .iter()
        .zip(&rosters)
        .map(|(gateway, roster)| TeamSessions {
            team_id: &gateway.team_ref,
            team_name: &gateway.team_name,
            sessions: roster
                .iter()
                .filter(|session| session.session_id != gateway.session_id)
                .collect(),
        })
        .collect();
    let gateway_ids: Vec<&str> = served.iter().map(|gateway| gateway.session_id.as_str()).collect();
    // 10. The roster text: the teams that answered, and a sentence under them when a team did not.
    let roster = ns::roster_of_connected(
        &slack_roster(&teams, &gateway_ids, &slack.bot_handle),
        left_out.len(),
    );
    // What a person is told is their answer, and the gateway is done with the request once they have it.
    let told = async |said: ns::Tell| {
        let answer = tell(gateways, slack, said).await;
        gateways.answered(&key, now).await;
        answer
    };
    // 11. What is not sent on is answered.
    let SlackInbound::Message {
        target,
        reply_to,
        summary,
        text,
        source,
    } = resolution
    else {
        let text = ns::answer_for(&resolution, &roster).unwrap_or_default();
        return Ok(told(ns::tell(m, &text)).await);
    };
    // 12. The session.
    let recipient = match resolve_recipient_across(&target, &teams) {
        Ok(recipient) => recipient,
        Err(error) => return Ok(told(ns::tell(m, &ns::not_sent(&error, &roster))).await),
    };
    let to = recipient.recipient.session.session_id.as_str();
    // 13. The gateway of the session's team.
    let gateway = served
        .iter()
        .find(|gateway| gateway.team_ref == recipient.team_id)
        .ok_or_else(|| ns::no_gateway(recipient.team_name))?;
    // 14. What Slack knows of the person, the channel and the message; each failure leaves a field out.
    let mut context = SlackContext::default();
    if let Ok(reply) = gateways.asked(&slack.api.user_info_request(&m.user)).await {
        context.user_name = Slack::user_info_answer(&m.user, reply.status, &reply.body)
            .ok()
            .and_then(|user| user.name);
    }
    if ns::wants_channel_name(m)
        && let Ok(reply) = gateways.asked(&slack.api.channel_info_request(&m.channel)).await
    {
        context.channel_name = Slack::channel_info_answer(&m.channel, reply.status, &reply.body)
            .ok()
            .flatten()
            .and_then(|channel| channel.name);
    }
    if let Some(ts) = m.posted_ts()
        && let Ok(reply) = gateways.asked(&slack.api.permalink_request(&m.channel, ts)).await
    {
        context.permalink = Slack::permalink_answer(reply.status, &reply.body);
    }
    // 15. The Brigade message, and 16. its send.
    let carried = slack_body(m, &text, &context);
    let send = ns::send(m, to, &carried, summary.as_deref(), reply_to.as_deref());
    let sent = match gateway.send(&send).await {
        Ok(sent) => sent,
        Err(refusal) if send.sends_again_unthreaded(refusal.code.as_str()) => gateway
            .send(&send.unthreaded())
            .await
            .map_err(|refusal| refusal.message)?,
        // A request that is delivered again after its carrying failed or was cut short is sent again, and
        // the engine may hold its message already. Sent with the same words the engine answers that
        // message, as a duplicate. Sent with other words, because Slack said something else of the person
        // or the channel this time, it refuses the key, and the row the earlier carrying left says which
        // message it is: the request is carried, and the person is not told that it was refused. With no
        // such row the engine holds the message all the same, and the request is one that came before.
        Err(refusal) if carried_before(&refusal) => {
            let known = gateways
                .store
                .lock()
                .await
                .carried_in(Kind::Slack, &m.event_id)
                .map(|row| row.brigade_message_id.clone());
            match known {
                Some(message_id) => Sent {
                    message_id,
                    duplicate: true,
                },
                None => {
                    gateways.answered(&key, now).await;
                    return Ok(notes::answer_duplicate());
                }
            }
        }
        Err(refusal) => {
            let text = notes::refused(refusal.code.as_str(), detail(&refusal));
            return Ok(told(ns::tell(m, &text)).await);
        }
    };
    // 17. The thread row, and with it the mark that the gateway is done with the request: the engine has
    // the message, and a reply to it finds its way. A stop or a crash before this leaves no mark, and the
    // delivery Slack makes again records the row.
    let row = ns::thread_row_in(&sent.message_id, to, m, &carried.summary);
    gateways
        .store
        .lock()
        .await
        .record(threads::thread(&gateway.team_ref, &row, now))?;
    gateways.done(&key, now).await?;
    // 18. The reaction, 19. the confirmation.
    if let Some(ts) = m.posted_ts() {
        let _ = gateways
            .asked(&slack.api.add_reaction_request(&m.channel, ts, ns::REACTION))
            .await;
    }
    match ns::confirmation_among(m, &recipient, teams.len(), left_out.len()) {
        Some(ns::Tell::Response(answer)) => return Ok(answer),
        Some(other) => {
            tell(gateways, slack, other).await;
        }
        None => {}
    }
    Ok(ns::answer_carried(
        &sent.message_id,
        to,
        &gateway.team_name,
        source,
        sent.duplicate,
    ))
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::{Duration, Instant};

    use brigade_gateway::slack::sign_slack;
    use serde_json::{Value, json};

    use tokio::sync::watch;

    use super::*;
    use crate::gateway::config::{self, Secret};
    use crate::gateway::endpoint::{self, Route};
    use crate::gateway::standin::{self, Said, StandIn};
    use crate::gateway::state::Store;
    use crate::gateway::testing::{
        self, SIGNING_SECRET, World, engine_on, gateways, scratch, team, team_named,
    };
    use crate::gateway::turns::Turns;

    /// A request as Slack signs one now.
    fn signed(content_type: &str, body: &str) -> Received {
        let timestamp = (note::now().0 / 1_000_000_000).to_string();
        let signature = sign_slack(SIGNING_SECRET, &timestamp, body).unwrap();
        Received {
            method: "POST".to_owned(),
            path: "/gateway/slack".to_owned(),
            headers: vec![
                ("content-type".to_owned(), content_type.to_owned()),
                ("x-slack-request-timestamp".to_owned(), timestamp),
                ("x-slack-signature".to_owned(), signature),
            ],
            body: body.to_owned(),
        }
    }

    fn event(id: &str, event: &Value) -> Received {
        let body = json!({"type": "event_callback", "event_id": id, "event": event});
        signed("application/json", &body.to_string())
    }

    /// A direct message to the bot.
    fn dm(id: &str, ts: &str, text: &str) -> Received {
        event(
            id,
            &json!({
                "type": "message", "channel": "D0PERSON01", "channel_type": "im", "user": "U0PERSON01",
                "text": text, "ts": ts,
            }),
        )
    }

    async fn asked(world: &World, received: &Received) -> (u16, Value) {
        asked_of(&world.gateways, received).await
    }

    async fn asked_of(gateways: &Gateways, received: &Received) -> (u16, Value) {
        let answer = endpoint::answer(gateways, Route::Slack, received).await;
        (
            answer.status,
            serde_json::from_str(&answer.body).unwrap_or(Value::Null),
        )
    }

    fn named(slack: &StandIn, name: &str) -> Vec<standin::Asked> {
        slack
            .asked()
            .into_iter()
            .filter(|asked| asked.named() == name)
            .collect()
    }

    fn roster(world: &World) -> String {
        let tail = &world.ops.session[world.ops.session.len() - 5..];
        format!(
            "Sessions on Brigade team *ops* right now (names and labels are their owners' own words):\n\n• `{tail}`  payments-api  _idle_ · alice@example.com\n\nTo write to one: message @brigade with a first line `to: <the five characters, or a member's label>`, or `/brigade send <five characters or label> <text>`."
        )
    }

    #[tokio::test]
    async fn a_reply_in_a_sessions_thread_reaches_the_session_as_an_answer_without_a_mention() {
        let slack = StandIn::start(standin::slack);
        let mut world = World::with_slack("slack-in-thread", &slack).await;
        let gateway = world.gateway(Kind::Slack).await;
        let question = world
            .ops
            .send(
                &gateway.session_id,
                "to: #general\n\nshall I deploy?",
                "deploy?",
                None,
            )
            .await;
        world.pass().await;

        // A reply in the thread of the session's post, with no mention of the bot.
        let reply = json!({
            "type": "message", "channel": "C0GENERAL1", "channel_type": "channel", "user": "U0PERSON01",
            "text": "yes, go ahead", "ts": "1700000001.000200", "thread_ts": "1700000000.000100",
        });
        let (status, said) = asked(&world, &event("Ev1", &reply)).await;
        assert_eq!(status, 200, "{said}");
        assert_eq!(
            (&said["to"], &said["team"], &said["source"], &said["duplicate"]),
            (
                &json!(world.ops.session),
                &json!("ops"),
                &json!("thread"),
                &json!(false)
            )
        );
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["message_id"], said["message_id"]);
        assert_eq!(
            inbox[0]["body"],
            json!(
                "Slack message from @Pat (U0PERSON01, unverified name) in #general:\n\nyes, go ahead\n\n[brigade]\nvia: slack\nfrom: U0PERSON01\nfrom-name: Pat\nchannel: C0GENERAL1\nchannel-name: general\nts: 1700000001.000200\nthread: 1700000000.000100\npermalink: https://example.slack.com/archives/C0GENERAL1/p1700000001000200\n[/brigade]"
            )
        );
        assert_eq!(inbox[0]["summary"], json!("yes, go ahead"));
        assert_eq!(inbox[0]["reply_to"], json!(question));
        assert_eq!(inbox[0]["sender"]["session_id"], json!(gateway.session_id));
        assert_eq!(inbox[0]["sender"]["session_name"], json!("slack-gateway"));
        assert_eq!(inbox[0]["sender"]["human_label"], json!("slack gateway"));

        // What Slack was asked for the body, and the reaction that tells the person it was sent on.
        assert_eq!(
            named(&slack, "GET /api/users.info")[0].target(),
            "/api/users.info?user=U0PERSON01"
        );
        assert_eq!(
            named(&slack, "GET /api/conversations.info")[0].target(),
            "/api/conversations.info?channel=C0GENERAL1"
        );
        assert_eq!(
            named(&slack, "GET /api/chat.getPermalink")[0].target(),
            "/api/chat.getPermalink?channel=C0GENERAL1&message_ts=1700000001.000200"
        );
        assert_eq!(
            named(&slack, "POST /api/reactions.add")[0].json(),
            json!({"channel": "C0GENERAL1", "timestamp": "1700000001.000200", "name": "eyes"})
        );
        // A person who answered in a thread named a session: the reaction is the whole of it.
        assert!(named(&slack, "POST /api/chat.postEphemeral").is_empty());

        // The same event again is carried once.
        let (status, said) = asked(&world, &event("Ev1", &reply)).await;
        assert_eq!((status, said), (200, json!({"duplicate": true})));
        assert_eq!(world.ops.inbox().await.len(), 1);

        // A second message in the thread goes to the same session and answers nothing: the newest row of
        // the thread is the person's own.
        let more = json!({
            "type": "message", "channel": "C0GENERAL1", "channel_type": "channel", "user": "U0PERSON01",
            "text": "and tell me when it is done", "ts": "1700000002.000300",
            "thread_ts": "1700000000.000100",
        });
        let (status, said) = asked(&world, &event("Ev2", &more)).await;
        assert_eq!((status, &said["source"]), (200, &json!("thread")), "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 2);
        assert_eq!(inbox[1]["reply_to"], Value::Null);

        // A reply in a thread that is not the gateway's is no business of it, and is not even recorded.
        let elsewhere = json!({
            "type": "message", "channel": "C0GENERAL1", "channel_type": "channel", "user": "U0PERSON01",
            "text": "lunch?", "ts": "1700000003.000400", "thread_ts": "1699999999.000001",
        });
        let (status, said) = asked(&world, &event("Ev3", &elsewhere)).await;
        assert_eq!(
            (status, said),
            (
                200,
                json!({"ignored": "a channel reply outside a Brigade thread"})
            )
        );
        assert!(!world.gateways.is_done("slack:Ev3").await);
        // The bot learned who it is once, for all of these.
        assert_eq!(named(&slack, "POST /api/auth.test").len(), 2);
    }

    #[tokio::test]
    async fn a_person_is_answered_where_they_are_and_told_which_session_a_label_chose() {
        let slack = StandIn::start(standin::slack);
        let world = World::with_slack("slack-in-told", &slack).await;
        let tail = &world.ops.session[world.ops.session.len() - 5..];

        // A member's label picks their session, and the person is told which.
        let labelled = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        let (status, said) = asked(&world, &labelled).await;
        assert_eq!((status, &said["source"]), (200, &json!("block")), "{said}");
        let told = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(
            told[0].json(),
            json!({
                "channel": "D0PERSON01",
                "text": format!(
                    "Sent to *payments-api* (`…{tail}`): alice@example.com's the idle session of theirs seen most recently. Accepted means stored for that session, not read; its reply will come to you here."
                ),
                "unfurl_links": false,
                "unfurl_media": false,
                "thread_ts": "1700000001.000100",
            })
        );
        let inbox = world.ops.inbox().await;
        assert!(
            inbox[0]["body"]
                .as_str()
                .unwrap()
                .starts_with("Slack message from @Pat (U0PERSON01, unverified name) in a direct message:\n\nhello\n\n[brigade]\n")
        );
        // A direct message has no channel to name.
        assert!(named(&slack, "GET /api/conversations.info").is_empty());

        // A message that names nobody is answered in the direct message, with the roster.
        let (status, said) = asked(&world, &dm("Ev2", "1700000002.000100", "hello?")).await;
        assert_eq!((status, said), (200, json!({"told": true})));
        let told = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(
            told[1].json()["text"],
            json!(format!(
                "No session was named. Start your message with a line `to: <session or member>`, or reply in the thread under a session's message.\n\n{}",
                roster(&world)
            ))
        );

        // The person has their answer, so the gateway is done with the request: delivered again it is a
        // duplicate, and the person is not told twice.
        assert_eq!(
            asked(&world, &dm("Ev2", "1700000002.000100", "hello?")).await,
            (200, json!({"duplicate": true}))
        );
        assert_eq!(named(&slack, "POST /api/chat.postMessage").len(), 2);

        // A mention in a channel that names nobody known is answered to the person alone.
        let mention = json!({
            "type": "app_mention", "channel": "C0GENERAL1", "user": "U0PERSON01",
            "text": "<@U0BOT00001> to: nobody-here\n\nhello", "ts": "1700000003.000100",
        });
        let (status, said) = asked(&world, &event("Ev3", &mention)).await;
        assert_eq!((status, said), (200, json!({"told": true})));
        let ephemeral = named(&slack, "POST /api/chat.postEphemeral")[0].json();
        assert_eq!(
            (&ephemeral["channel"], &ephemeral["user"]),
            (&json!("C0GENERAL1"), &json!("U0PERSON01"))
        );
        assert!(
            ephemeral["text"]
                .as_str()
                .unwrap()
                .starts_with("The message was not sent: "),
            "{ephemeral}"
        );

        // What the engine refuses, the person is told with the engine's code and reason.
        let long = format!("to: {tail}\nsummary: {}\n\nhello", "x".repeat(201));
        let (status, said) = asked(&world, &dm("Ev4", "1700000004.000100", &long)).await;
        assert_eq!((status, said), (200, json!({"told": true})));
        assert_eq!(
            named(&slack, "POST /api/chat.postMessage")[2].json()["text"],
            json!("Brigade refused the message (invalid_input: too_long). Try again later.")
        );

        // An answer to a message the engine no longer has is sent all the same, answering nothing.
        let gone = format!("to: {tail}\nreply-to: 00000000000000000000000000000000\n\nstill there?");
        let (status, said) = asked(&world, &dm("Ev5", "1700000005.000100", &gone)).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 2);
        assert_eq!(inbox[1]["reply_to"], Value::Null);
        assert_eq!(world.ops.roster().await.len(), 2);
    }

    #[tokio::test]
    async fn a_slash_command_is_answered_in_its_own_response() {
        let slack = StandIn::start(standin::slack);
        let world = World::with_slack("slack-in-command", &slack).await;
        let tail = &world.ops.session[world.ops.session.len() - 5..];
        let form = |text: &str| {
            format!(
                "command=%2Fbrigade&user_id=U0PERSON01&channel_id=C0GENERAL1&channel_name=general&text={text}"
            )
        };
        let command = |text: &str| signed("application/x-www-form-urlencoded", &form(text));

        let (status, said) = asked(&world, &command("sessions")).await;
        assert_eq!(
            (status, said),
            (200, json!({"response_type": "ephemeral", "text": roster(&world)}))
        );
        // A command Slack gave no trigger id is given one of its own, so the same words twice are two
        // commands and not one delivered twice.
        let (status, said) = asked(&world, &command("sessions")).await;
        assert_eq!((status, &said["response_type"]), (200, &json!("ephemeral")));

        let (status, said) = asked(&world, &command(&format!("send+{tail}+ship+it"))).await;
        assert_eq!(status, 200);
        assert_eq!(
            said["text"],
            json!(format!(
                "Sent to *payments-api* (`…{tail}`). Accepted means stored for that session, not read; its reply will come to you here."
            ))
        );
        let inbox = world.ops.inbox().await;
        assert_eq!(
            inbox[0]["body"],
            json!(
                "Slack message from @Pat (U0PERSON01, unverified name) in #general:\n\nship it\n\n[brigade]\nvia: slack\nfrom: U0PERSON01\nfrom-name: Pat\nchannel: C0GENERAL1\nchannel-name: general\n[/brigade]"
            )
        );
        // A command with Slack's own trigger id is one command however often it comes.
        let with_trigger = signed(
            "application/x-www-form-urlencoded",
            &format!("{}&trigger_id=1234.5678", form("help")),
        );
        assert_eq!(
            asked(&world, &with_trigger).await.1["response_type"],
            json!("ephemeral")
        );
        assert_eq!(asked(&world, &with_trigger).await.1, json!({"duplicate": true}));
        // Nothing was posted for any of it: a command's answer is its response.
        assert!(named(&slack, "POST /api/chat.postMessage").is_empty());
        assert!(named(&slack, "POST /api/reactions.add").is_empty());
    }

    #[tokio::test]
    async fn one_slack_app_serves_several_teams_and_each_message_is_its_own_teams_gateways() {
        let slack = StandIn::start(standin::slack);
        let data = scratch("slack-in-teams");
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        let web = team_named(&engine, "web", "bob@example.com", "storefront").await;
        let mut app = testing::slack(&format!("{}/api", slack.url), &ops.join_secret);
        app.teams.push(Secret::of(&web.join_secret));
        let both = gateways(
            &data,
            &engine,
            config::Gateways {
                slack: Some(app),
                mail: None,
            },
        );
        let (_stop, stopping) = watch::channel(false);
        Turns::new(&both).pass(&both, &stopping).await;
        let tail = |session: &str| session[session.len() - 5..].to_owned();
        let (ops_tail, web_tail) = (tail(&ops.session), tail(&web.session));

        // A session is found on whichever team it is in, and the message is sent by that team's gateway.
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {web_tail}\n\nhello"));
        let (status, said) = asked_of(&both, &hello).await;
        assert_eq!((status, &said["team"]), (200, &json!("web")), "{said}");
        assert!(ops.inbox().await.is_empty());
        let inbox = web.inbox().await;
        assert_eq!(inbox.len(), 1);
        let web_gateway = web
            .roster()
            .await
            .into_iter()
            .find(|row| row["harness"] == json!("gateway-slack"))
            .unwrap();
        assert_eq!(inbox[0]["sender"]["session_id"], web_gateway["session_id"]);
        assert_eq!(
            both.store
                .lock()
                .await
                .thread(said["message_id"].as_str().unwrap())
                .unwrap()
                .team_ref,
            web.team_ref
        );

        // The roster a person is shown is grouped by team, and a confirmation names the team.
        let form = |text: &str| {
            signed(
                "application/x-www-form-urlencoded",
                &format!("user_id=U0PERSON01&channel_id=D0PERSON01&channel_name=directmessage&text={text}"),
            )
        };
        let (_, said) = asked_of(&both, &form("sessions")).await;
        assert_eq!(
            said["text"],
            json!(format!(
                "Sessions on the 2 Brigade teams this gateway serves right now (names and labels are their owners' own words):\n\n*ops*\n• `{ops_tail}`  payments-api  _idle_ · alice@example.com\n\n*web*\n• `{web_tail}`  storefront  _idle_ · bob@example.com\n\nTo write to one: message @brigade with a first line `to: <the five characters, or a member's label>`, or `/brigade send <five characters or label> <text>`."
            ))
        );
        let (_, said) = asked_of(&both, &form(&format!("send+{ops_tail}+ship+it"))).await;
        assert_eq!(
            said["text"],
            json!(format!(
                "Sent to *payments-api* (`…{ops_tail}`) on team *ops*. Accepted means stored for that session, not read; its reply will come to you here."
            ))
        );
        assert_eq!(ops.inbox().await.len(), 1);
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn a_team_whose_gateway_was_revoked_is_left_out_and_the_other_teams_are_served() {
        let slack = StandIn::start(standin::slack);
        let data = scratch("slack-in-left-out");
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        let web = team_named(&engine, "web", "bob@example.com", "storefront").await;
        // The web team's join secret is in a file, as the configuration names one: the file is what the
        // operator replaces when the secret was rotated.
        let web_secret = data.join("web.secret");
        std::fs::write(&web_secret, format!("{}\n", web.join_secret)).unwrap();
        let mut app = testing::slack(&format!("{}/api", slack.url), &ops.join_secret);
        app.teams.push(Secret::kept_in(&web_secret));
        let both = gateways(
            &data,
            &engine,
            config::Gateways {
                slack: Some(app),
                mail: None,
            },
        );
        let (_stop, stopping) = watch::channel(false);
        let mut turns = Turns::new(&both);
        turns.pass(&both, &stopping).await;
        let tail = |session: &str| session[session.len() - 5..].to_owned();
        let (ops_tail, web_tail) = (tail(&ops.session), tail(&web.session));
        let form = |text: &str| {
            signed(
                "application/x-www-form-urlencoded",
                &format!("user_id=U0PERSON01&channel_id=D0PERSON01&channel_name=directmessage&text={text}"),
            )
        };
        let how = "To write to one: message @brigade with a first line `to: <the five characters, or a member's label>`, or `/brigade send <five characters or label> <text>`.";
        let ops_row = format!("• `{ops_tail}`  payments-api  _idle_ · alice@example.com");
        let web_row = format!("• `{web_tail}`  storefront  _idle_ · bob@example.com");
        let both_teams = format!(
            "Sessions on the 2 Brigade teams this gateway serves right now (names and labels are their owners' own words):\n\n*ops*\n{ops_row}\n\n*web*\n{web_row}\n\n{how}"
        );
        let (_, said) = asked_of(&both, &form("sessions")).await;
        assert_eq!(said["text"], json!(both_teams));

        // The web team's owner rotates its join secret and revokes everyone who joined with the old one,
        // which its gateway did. Nobody of the ops team had a hand in it.
        let rotated = web
            .administer("/v1/team/rotate-secret", &json!({"team_ref": web.team_ref}))
            .await;
        let swept = web
            .administer(
                "/v1/team/revoke-member",
                &json!({"team_ref": web.team_ref, "joined_secret_version_lte": 1}),
            )
            .await;
        assert_eq!(swept["revoked"], json!(1));

        // A message to a session of the ops team is carried as before, by the ops team's gateway. The
        // person named the session by the end of its id, which with every team reached is confirmed by the
        // reaction alone. With a team left out they are told where the message went and that some sessions
        // could not be reached: in the direct message, in the channel they mentioned the bot in, and in a
        // command's own answer.
        let one_out = "One team's gateway is not connected at the moment, so its sessions cannot be reached.";
        let went_to_ops = format!(
            "Sent to *payments-api* (`…{ops_tail}`) on team *ops*. Accepted means stored for that session, not read; its reply will come to you here.\n\n{one_out}"
        );
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {ops_tail}\n\nhello"));
        let (status, said) = asked_of(&both, &hello).await;
        assert_eq!((status, &said["team"]), (200, &json!("ops")), "{said}");
        let told = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(told.len(), 1);
        assert_eq!(
            (&told[0].json()["text"], &told[0].json()["thread_ts"]),
            (&json!(went_to_ops), &json!("1700000001.000100"))
        );
        let mention = json!({
            "type": "app_mention", "channel": "C0GENERAL1", "user": "U0PERSON01",
            "text": format!("<@U0BOT00001> to: {ops_tail}\n\nhello from the channel"),
            "ts": "1700000001.000200",
        });
        let (status, said) = asked_of(&both, &event("Ev1b", &mention)).await;
        assert_eq!((status, &said["team"]), (200, &json!("ops")), "{said}");
        let ephemeral = named(&slack, "POST /api/chat.postEphemeral");
        assert_eq!(ephemeral.len(), 1);
        assert_eq!(
            (&ephemeral[0].json()["text"], &ephemeral[0].json()["user"]),
            (&json!(went_to_ops), &json!("U0PERSON01"))
        );
        let (status, said) = asked_of(&both, &form(&format!("send+{ops_tail}+ship+it"))).await;
        assert_eq!(status, 200);
        assert_eq!(said["text"], json!(went_to_ops));
        assert_eq!(ops.inbox().await.len(), 3);

        // The roster lists the team that answered, and says under it that not every session can be
        // reached. It does not name the team that is left out.
        let ops_alone = format!(
            "Sessions on Brigade team *ops* right now (names and labels are their owners' own words):\n\n{ops_row}\n\n{how}\n\n{one_out}"
        );
        let (status, said) = asked_of(&both, &form("sessions")).await;
        assert_eq!(status, 200);
        assert_eq!(said["text"], json!(ops_alone));
        assert!(!ops_alone.contains("web") && !ops_alone.contains("storefront"));

        // A message to a session of the web team finds nobody of that name, and the person is told so in
        // the words they always were, with the same roster and its sentence: in the command's own answer,
        // and in the direct message.
        let not_sent = format!(
            "The message was not sent: no session ends with \"{web_tail}\", and no member has that label.\n\n{ops_alone}"
        );
        let (status, said) = asked_of(&both, &form(&format!("send+{web_tail}+is+anyone+there"))).await;
        assert_eq!(status, 200);
        assert_eq!(said["text"], json!(not_sent));
        let lost = dm("Ev2", "1700000002.000100", &format!("to: {web_tail}\n\nhello"));
        assert_eq!(asked_of(&both, &lost).await, (200, json!({"told": true})));
        let told = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(told.last().unwrap().json()["text"], json!(not_sent));
        assert!(web.inbox().await.is_empty());

        // The operator replaces the file with the secret the team has now. At its next turn the gateway
        // reads it, joins again, and both teams are found again: the roster is what it was, with no
        // sentence under it, and a message to the web team's session is carried by its gateway.
        let new_secret = rotated["join_secret"].as_str().unwrap();
        std::fs::write(&web_secret, format!("{new_secret}\n")).unwrap();
        turns.pass(&both, &stopping).await;
        let (_, said) = asked_of(&both, &form("sessions")).await;
        assert_eq!(said["text"], json!(both_teams));
        let (_, said) = asked_of(&both, &form(&format!("send+{web_tail}+ship+it"))).await;
        assert_eq!(
            said["text"],
            json!(format!(
                "Sent to *storefront* (`…{web_tail}`) on team *web*. Accepted means stored for that session, not read; its reply will come to you here."
            ))
        );
        assert_eq!(web.inbox().await.len(), 1);
        // With every team reached again, a session named by the end of its id in a direct message is
        // confirmed by the reaction alone, as it always was: nothing is posted to the person.
        let posted = named(&slack, "POST /api/chat.postMessage").len();
        let again = dm(
            "Ev3",
            "1700000003.000100",
            &format!("to: {ops_tail}\n\nhello again"),
        );
        let (status, said) = asked_of(&both, &again).await;
        assert_eq!((status, &said["team"]), (200, &json!("ops")), "{said}");
        assert_eq!(named(&slack, "POST /api/chat.postMessage").len(), posted);
        assert_eq!(named(&slack, "POST /api/reactions.add").len(), 3);
        assert_eq!(ops.inbox().await.len(), 4);
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn a_name_two_teams_share_is_carried_with_a_word_while_one_of_them_is_left_out() {
        let slack = StandIn::start(standin::slack);
        let data = scratch("slack-in-shared-name");
        let engine = engine_on(&data);
        // Both teams have a member labelled alike: the same person, a principal in each.
        let ops = team(&engine).await;
        let web = team_named(&engine, "web", "alice@example.com", "storefront").await;
        let mut app = testing::slack(&format!("{}/api", slack.url), &ops.join_secret);
        app.teams.push(Secret::of(&web.join_secret));
        let both = gateways(
            &data,
            &engine,
            config::Gateways {
                slack: Some(app),
                mail: None,
            },
        );
        let (_stop, stopping) = watch::channel(false);
        Turns::new(&both).pass(&both, &stopping).await;
        let ops_tail = ops.session[ops.session.len() - 5..].to_owned();
        let form = |text: &str| {
            signed(
                "application/x-www-form-urlencoded",
                &format!("user_id=U0PERSON01&channel_id=D0PERSON01&channel_name=directmessage&text={text}"),
            )
        };

        // With both teams reached the label names a member of each, and the gateway picks no team for the
        // person: the message is not sent.
        let (status, said) = asked_of(&both, &form("send+alice@example.com+hello")).await;
        assert_eq!(status, 200);
        assert!(
            said["text"]
                .as_str()
                .unwrap()
                .starts_with("The message was not sent: "),
            "{said}"
        );
        assert!(ops.inbox().await.is_empty() && web.inbox().await.is_empty());

        // The web team's owner revokes its gateway, which stays out for now. The label then names one
        // member, and the message goes to the ops team's session. The person is told where it went, on
        // which team, and that the sessions of one team could not be reached: the one they meant may be
        // among those.
        let body = json!({"team_ref": web.team_ref, "joined_secret_version_lte": 1});
        assert_eq!(
            web.administer("/v1/team/revoke-member", &body).await["revoked"],
            json!(1)
        );
        let sent = format!(
            "Sent to *payments-api* (`…{ops_tail}`) on team *ops*: alice@example.com's the idle session of theirs seen most recently. Accepted means stored for that session, not read; its reply will come to you here.\n\nOne team's gateway is not connected at the moment, so its sessions cannot be reached."
        );
        let (status, said) = asked_of(&both, &form("send+alice@example.com+hello")).await;
        assert_eq!(status, 200);
        assert_eq!(said["text"], json!(sent));
        // The same in a direct message, where a person who names a member is told by a post.
        let labelled = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello again");
        let (status, said) = asked_of(&both, &labelled).await;
        assert_eq!((status, &said["team"]), (200, &json!("ops")), "{said}");
        let told = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(told.last().unwrap().json()["text"], json!(sent));
        assert_eq!(ops.inbox().await.len(), 2);
        assert!(web.inbox().await.is_empty());
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn a_request_fails_while_no_team_can_be_reached_and_is_carried_when_slack_delivers_it_again() {
        let slack = StandIn::start(standin::slack);
        let mut world = World::with_slack("slack-in-all-out", &slack).await;
        let tail = world.ops.session[world.ops.session.len() - 5..].to_owned();
        // The owner of the one team the gateway serves revokes everyone who joined with the team's secret:
        // the gateway. The secret was not rotated, so the gateway joins again at its next turn.
        let body = json!({"team_ref": world.ops.team_ref, "joined_secret_version_lte": 1});
        let swept = world.ops.administer("/v1/team/revoke-member", &body).await;
        assert_eq!(swept["revoked"], json!(1));

        // Until then the gateway reaches no team. The request fails as it does for a gateway that serves
        // none: Slack is told the fixed text and delivers again, the person is told nothing, and nothing
        // says the request came.
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {tail}\n\nhello"));
        assert_eq!(asked(&world, &hello).await, (500, json!({"error": FAILED})));
        assert!(named(&slack, "POST /api/chat.postMessage").is_empty());
        assert!(world.ops.inbox().await.is_empty());

        // The gateway joins again, and the delivery Slack makes again is carried.
        world.pass().await;
        let (status, said) = asked(&world, &hello).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["message_id"], said["message_id"]);
    }

    #[tokio::test]
    async fn only_a_signed_post_is_read_and_the_handshake_needs_no_signature() {
        let slack = StandIn::start(standin::slack);
        let world = World::with_slack("slack-in-signed", &slack).await;
        let before = slack.asked().len();
        let hello = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");

        // The handshake is answered as it came, signed or not.
        let mut handshake = signed(
            "application/json",
            r#"{"type":"url_verification","challenge":"c-1"}"#,
        );
        handshake.headers.truncate(1);
        assert_eq!(
            asked(&world, &handshake).await,
            (200, json!({"challenge": "c-1"}))
        );
        // Anything else without Slack's signature, with another's, or with an old one, is refused.
        let mut unsigned = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        unsigned.headers.truncate(1);
        let mut forged = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        forged.headers[2].1 = sign_slack("another-secret", &forged.headers[1].1, &forged.body).unwrap();
        let mut changed = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        changed.body = changed.body.replace("hello", "goodbye");
        let mut old = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        let stale = (note::now().0 / 1_000_000_000 - 301).to_string();
        old.headers[2].1 = sign_slack(SIGNING_SECRET, &stale, &old.body).unwrap();
        old.headers[1].1 = stale;
        for refused in [&unsigned, &forged, &changed, &old] {
            assert_eq!(
                asked(&world, refused).await,
                (
                    401,
                    json!({"error": "the request is not signed by this gateway's Slack app"})
                )
            );
        }
        let mut get = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        get.method = "GET".to_owned();
        assert_eq!(
            asked(&world, &get).await,
            (405, json!({"error": "method not allowed"}))
        );
        // None of them reached a session, was recorded, or made the gateway ask Slack anything.
        assert!(world.ops.inbox().await.is_empty());
        assert_eq!(slack.asked().len(), before);
        // The same message, signed, is carried.
        assert_eq!(asked(&world, &hello).await.0, 200);
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn an_event_that_comes_before_the_gateway_has_a_session_is_carried_when_slack_delivers_it_again() {
        let slack = StandIn::start(standin::slack);
        // No gateway has taken its place: none is a member of any team yet.
        let api = format!("{}/api", slack.url);
        let mut world = World::new("slack-in-early", |join_secret| config::Gateways {
            slack: Some(testing::slack(&api, join_secret)),
            mail: None,
        })
        .await;
        let hello = dm("Ev1", "1700000001.000100", "to: alice@example.com\n\nhello");
        // Slack is told a fixed text; why the request failed is the server's to say, in its own lines.
        assert_eq!(asked(&world, &hello).await, (500, json!({"error": FAILED})));
        // Slack was answered a failure and delivers again. By then the gateway has its session, and the
        // event, which the functions would have called a duplicate, is carried.
        world.pass().await;
        let (status, said) = asked(&world, &hello).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["message_id"], said["message_id"]);
        // What was carried is a duplicate when it comes once more.
        assert_eq!(asked(&world, &hello).await, (200, json!({"duplicate": true})));
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn a_carry_that_failed_after_the_engine_accepted_reaches_the_session_once_when_it_comes_again() {
        // Slack's answer about the person comes between the mark that the event came and the thread row of
        // the message it became. That is where the test makes the store unwritable, and where it has Slack
        // say something else of the person the second time.
        let in_the_way: Arc<Mutex<Option<PathBuf>>> = Arc::new(Mutex::new(None));
        let nameless = Arc::new(AtomicBool::new(false));
        let (blocked, unnamed) = (Arc::clone(&in_the_way), Arc::clone(&nameless));
        let slack = StandIn::start(move |asked| {
            if asked.named() == "GET /api/users.info" {
                if let Some(beside) = blocked.lock().unwrap().as_ref() {
                    std::fs::create_dir_all(beside.join("in-the-way")).unwrap();
                }
                if unnamed.load(Ordering::SeqCst) {
                    return Said::json(200, &json!({"ok": false, "error": "user_not_found"}));
                }
            }
            standin::slack(asked)
        });
        let world = World::with_slack("slack-in-unwritten", &slack).await;
        let tail = &world.ops.session[world.ops.session.len() - 5..];
        let beside = world.data.join("gateway").join("state.json.new");
        let block = |on: bool| {
            *in_the_way.lock().unwrap() = on.then(|| beside.clone());
            if !on {
                std::fs::remove_dir_all(&beside).unwrap();
            }
        };

        // The engine accepts the message, and the thread row cannot be written: Slack is answered a failure.
        block(true);
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {tail}\n\nhello"));
        // Slack is told a fixed text, with nothing in it of where the server keeps its state.
        assert_eq!(asked(&world, &hello).await, (500, json!({"error": FAILED})));
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert!(named(&slack, "POST /api/reactions.add").is_empty());

        // Slack delivers again. The event is carried again, the engine answers the message it holds, and
        // the session has it once; the row is there, and now the person sees that it was sent on.
        block(false);
        let (status, said) = asked(&world, &hello).await;
        assert_eq!(status, 200, "{said}");
        assert_eq!(
            (&said["message_id"], &said["to"], &said["duplicate"]),
            (&inbox[0]["message_id"], &json!(world.ops.session), &json!(true))
        );
        assert_eq!(world.ops.inbox().await.len(), 1);
        let message_id = said["message_id"].as_str().unwrap().to_owned();
        let reopened = Store::open(&world.data).unwrap();
        assert_eq!(reopened.thread(&message_id).unwrap().address, "D0PERSON01");
        assert_eq!(named(&slack, "POST /api/reactions.add").len(), 1);
        assert_eq!(asked(&world, &hello).await, (200, json!({"duplicate": true})));

        // The same with a second delivery that reads differently, because Slack no longer says the
        // person's name: the engine refuses the event's key for other words, the row says which message
        // it holds, and the person is not told that their message was refused.
        block(true);
        let again = dm("Ev2", "1700000002.000100", &format!("to: {tail}\n\nonce more"));
        assert_eq!(asked(&world, &again).await.0, 500);
        block(false);
        nameless.store(true, Ordering::SeqCst);
        let (status, said) = asked(&world, &again).await;
        assert_eq!(status, 200, "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 2);
        assert_eq!(
            (&said["message_id"], &said["duplicate"]),
            (&inbox[1]["message_id"], &json!(true))
        );
        assert!(
            inbox[1]["body"]
                .as_str()
                .unwrap()
                .starts_with("Slack message from @Pat (U0PERSON01, unverified name) in a direct message:")
        );
        assert!(named(&slack, "POST /api/chat.postMessage").is_empty());
        assert_eq!(named(&slack, "POST /api/reactions.add").len(), 2);
    }

    #[tokio::test]
    async fn nothing_of_an_unsigned_request_is_read_but_a_handshake_small_enough_to_be_one() {
        let slack = StandIn::start(standin::slack);
        let world = World::with_slack("slack-in-unsigned", &slack).await;
        let before = slack.asked().len();
        let unsigned = |content_type: &str, body: &str| {
            let mut request = signed(content_type, body);
            request.headers.truncate(1);
            request
        };
        let refused = (
            401,
            json!({"error": "the request is not signed by this gateway's Slack app"}),
        );

        // A megabyte of names, each once: what kept the functions' parser busy for seconds, and a core
        // of the server's with it. Unsigned, or signed by another, it is refused without being read.
        let mut flood = String::from("{");
        for name in 0.. {
            if flood.len() > (1 << 20) - 64 {
                break;
            }
            flood.push_str(&format!("\"{name}\":0,"));
        }
        flood.push_str("\"type\":\"url_verification\",\"challenge\":\"c\"}");
        let mut forged = signed("application/json", &flood);
        forged.headers[2].1 = sign_slack("another-apps-secret", &forged.headers[1].1, &flood).unwrap();
        let began = Instant::now();
        assert_eq!(
            asked(&world, &unsigned("application/json", &flood)).await,
            refused
        );
        assert_eq!(asked(&world, &forged).await, refused);
        assert!(
            began.elapsed() < Duration::from_millis(500),
            "{:?}",
            began.elapsed()
        );

        // A handshake is a few hundred bytes. One that is longer than a handshake is, or that does not
        // say it is JSON, is a request like any other: unsigned, it is refused.
        let long = json!({"type": "url_verification", "challenge": "c".repeat(HANDSHAKE_BYTES)}).to_string();
        assert_eq!(asked(&world, &unsigned("application/json", &long)).await, refused);
        let small = r#"{"type":"url_verification","challenge":"c-1"}"#;
        assert_eq!(asked(&world, &unsigned("text/plain", small)).await, refused);
        assert_eq!(
            asked(&world, &unsigned("application/json", small)).await,
            (200, json!({"challenge": "c-1"}))
        );
        // Signed by Slack, the long one is read, and is the handshake it says it is.
        let (status, said) = asked(&world, &signed("application/json", &long)).await;
        assert_eq!(
            (status, said["challenge"].as_str().map(str::len)),
            (200, Some(HANDSHAKE_BYTES))
        );
        // For none of the unsigned ones was Slack asked anything.
        assert_eq!(
            slack
                .asked()
                .iter()
                .skip(before)
                .map(standin::Asked::named)
                .collect::<Vec<_>>(),
            ["POST /api/auth.test"]
        );
    }

    #[tokio::test]
    async fn a_delivery_of_a_request_that_is_being_carried_is_told_to_come_again() {
        let slack = StandIn::start(standin::slack);
        let world = World::with_slack("slack-in-carrying", &slack).await;
        let tail = &world.ops.session[world.ops.session.len() - 5..];
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {tail}\n\nhello"));
        // The first delivery is being carried: it waits for Slack, say.
        let first = world.gateways.carry("slack:Ev1").expect("nothing carries it yet");
        assert!(world.gateways.carry("slack:Ev1").is_none());
        // Slack delivers again meanwhile. It is told to come again, and nothing is done for it.
        assert_eq!(
            asked(&world, &hello).await,
            (500, json!({"error": BEING_CARRIED}))
        );
        assert!(world.ops.inbox().await.is_empty());
        assert!(named(&slack, "GET /api/users.info").is_empty());
        // Whatever became of the first, the next delivery finds out: here it was cut short, and the
        // request is carried; done with, it is a duplicate.
        drop(first);
        let (status, said) = asked(&world, &hello).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        assert_eq!(asked(&world, &hello).await, (200, json!({"duplicate": true})));
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn a_crash_after_the_send_and_before_the_row_leaves_the_row_to_the_next_delivery() {
        let slack = StandIn::start(standin::slack);
        let mut world = World::with_slack("slack-in-crash", &slack).await;
        let tail = world.ops.session[world.ops.session.len() - 5..].to_owned();
        let hello = dm("Ev1", "1700000001.000100", &format!("to: {tail}\n\nhello"));
        let (status, first) = asked(&world, &hello).await;
        assert_eq!((status, &first["duplicate"]), (200, &json!(false)), "{first}");

        // The server ends as if it had got no further than the send: the engine holds the message, and
        // the state's file holds neither its row nor a word of the request.
        world.crash(|state| {
            state["threads"] = json!([]);
            state["received"] = json!([]);
        });
        assert!(!world.gateways.is_done("slack:Ev1").await);
        // Slack delivers again. The request is gone through in full, the engine answers the message it
        // holds, and the row is recorded: the session has the message once, and its answer will find
        // the person's thread.
        let (status, again) = asked(&world, &hello).await;
        assert_eq!(status, 200, "{again}");
        assert_eq!(
            (&again["message_id"], &again["to"], &again["duplicate"]),
            (&first["message_id"], &json!(world.ops.session), &json!(true))
        );
        assert_eq!(world.ops.inbox().await.len(), 1);
        let reopened = Store::open(&world.data).unwrap();
        let row = reopened.thread(first["message_id"].as_str().unwrap()).unwrap();
        assert_eq!(
            (row.address.as_str(), row.thread_id.as_deref()),
            ("D0PERSON01", Some("1700000001.000100"))
        );
        assert!(reopened.delivered("slack:Ev1"));
        assert_eq!(asked(&world, &hello).await, (200, json!({"duplicate": true})));

        // A crash before the send leaves nothing at all, and the next delivery carries the request.
        let later = dm("Ev2", "1700000002.000100", &format!("to: {tail}\n\nonce more"));
        world.crash(|_| {});
        let (status, carried) = asked(&world, &later).await;
        assert_eq!((status, &carried["duplicate"]), (200, &json!(false)), "{carried}");
        assert_eq!(world.ops.inbox().await.len(), 2);
    }
}
