//! Slack, outbound: one turn of one team's gateway (`supabase/functions/slack-out/index.ts`).
//!
//! A turn takes what waits in the gateway session's inbox, oldest first, and for each message finds the
//! channel, resolves the handles in its text, posts it, records the thread and acknowledges it. A message
//! that cannot be delivered is acknowledged too, once its sender has been told; one Slack says a later
//! attempt can deliver stays in the inbox for the next turn. The steps and their order are the Edge
//! Function's, and what each says and decides is `brigade-gateway`'s.
//!
//! What the function did and this keeps: a request to Slack that got no answer ends the delivery like any
//! refusal Slack did not call passing, and a post whose thread row could not be written is posted again
//! once the row is gone, which here it is only after a restart.

use std::collections::BTreeMap;

use brigade_gateway::notes::{self, slack as ns};
use brigade_gateway::providers::Slack;
use brigade_gateway::providers::slack::{
    ChannelSearch, PostMessage, Posted, SlackChannel, SlackUser, UserSearch,
};
use brigade_gateway::{
    Envelope, HttpRequest, Mentions, SlackError, SlackRenderContext, SlackTarget, ThreadLookup,
    is_slack_user_id, mention_names, render_slack_text, resolve_slack_outbound,
};
use tokio::sync::watch;

use super::client::Reply;
use super::presence::Failure;
use super::served::Served;
use super::turns::{Tally, tell_sender};
use super::{Gateways, SlackSide, threads};
use crate::note;

/// What one pass of the loop learns from Slack once and every gateway of the pass shares: the user behind
/// each handle it looked up, and whether the app hears a channel thread's replies without a mention.
#[derive(Default)]
pub struct Pass {
    names: BTreeMap<String, Option<String>>,
    hears: Option<bool>,
}

/// One call to Slack: its answer, or the failure of a request that got none.
async fn call(gateways: &Gateways, request: &HttpRequest) -> Result<Reply, SlackError> {
    gateways
        .asked(request)
        .await
        .map_err(|why| SlackError::unanswered(&why))
}

async fn find_user(
    gateways: &Gateways,
    slack: &SlackSide,
    name: &str,
) -> Result<Option<SlackUser>, SlackError> {
    let mut search = UserSearch::new(name);
    while let Some(request) = search.request(&slack.api) {
        let reply = call(gateways, &request).await?;
        if let Some(user) = search.answer(reply.status, &reply.body)? {
            return Ok(Some(user));
        }
    }
    Ok(None)
}

async fn find_channel(
    gateways: &Gateways,
    slack: &SlackSide,
    name: &str,
) -> Result<Option<SlackChannel>, SlackError> {
    let mut search = ChannelSearch::new(name);
    while let Some(request) = search.request(&slack.api) {
        let reply = call(gateways, &request).await?;
        if let Some(channel) = search.answer(reply.status, &reply.body)? {
            return Ok(Some(channel));
        }
    }
    Ok(None)
}

async fn open_dm(gateways: &Gateways, slack: &SlackSide, user: &str) -> Result<String, SlackError> {
    let reply = call(gateways, &slack.api.open_dm_request(user)).await?;
    Slack::open_dm_answer(reply.status, &reply.body)
}

/// The channel a target is posted in: a person's direct message, or a channel by id or name. `None` when
/// Slack knows nobody and nothing by the name.
async fn resolve_channel(
    gateways: &Gateways,
    slack: &SlackSide,
    target: &SlackTarget,
) -> Result<Option<String>, SlackError> {
    Ok(match target {
        SlackTarget::User { id } => Some(open_dm(gateways, slack, id).await?),
        SlackTarget::UserName { name } => match find_user(gateways, slack, name).await? {
            Some(user) => Some(open_dm(gateways, slack, &user.id).await?),
            None => None,
        },
        SlackTarget::UserEmail { email } => {
            let reply = call(gateways, &slack.api.lookup_by_email_request(email)).await?;
            match Slack::lookup_by_email_answer(email, reply.status, &reply.body)? {
                Some(user) => Some(open_dm(gateways, slack, &user.id).await?),
                None => None,
            }
        }
        SlackTarget::Channel { id } => Some(id.clone()),
        SlackTarget::ChannelName { name } => find_channel(gateways, slack, name)
            .await?
            .map(|channel| channel.id),
    })
}

/// The Slack user behind each handle of a text. A handle is looked up once a pass, whatever its case.
async fn resolve_mentions(
    gateways: &Gateways,
    slack: &SlackSide,
    text: &str,
    known: &mut BTreeMap<String, Option<String>>,
) -> Result<Mentions, SlackError> {
    let mut mentions = Mentions::new();
    for name in mention_names(text) {
        let id = if is_slack_user_id(&name) {
            Some(name.clone())
        } else {
            let key = ns::mention_key(&name);
            if !known.contains_key(&key) {
                let found = find_user(gateways, slack, &name).await?;
                known.insert(key.clone(), found.map(|user| user.id));
            }
            known.get(&key).cloned().flatten()
        };
        if let Some(id) = id.filter(|id| !id.is_empty()) {
            mentions.insert(name, id);
        }
    }
    Ok(mentions)
}

async fn post_once(gateways: &Gateways, slack: &SlackSide, post: &PostMessage) -> Result<Posted, SlackError> {
    let reply = call(gateways, &slack.api.post_message_request(post)).await?;
    Slack::post_message_answer(reply.status, &reply.body)
}

/// Posts a message, joining the channel first where Slack says the bot is not in it.
async fn post_with_join(
    gateways: &Gateways,
    slack: &SlackSide,
    post: &PostMessage,
) -> Result<Posted, SlackError> {
    match post_once(gateways, slack, post).await {
        Err(error) if ns::joins_first(&error) => {
            let reply = call(gateways, &slack.api.join_channel_request(&post.channel)).await?;
            Slack::join_channel_answer(reply.status, &reply.body)?;
            post_once(gateways, slack, post).await
        }
        other => other,
    }
}

/// One turn of one team's Slack gateway: what waits in its inbox is delivered, or its sender told.
///
/// # Errors
/// The engine refused the receive or an acknowledgement, or a thread row could not be written. What the
/// turn had not reached waits for the next one.
pub async fn turn(
    gateways: &Gateways,
    slack: &SlackSide,
    gateway: &Served,
    pass: &mut Pass,
    tally: &mut Tally,
    stopping: &watch::Receiver<bool>,
) -> Result<(), Failure> {
    let inbox = gateway.inbox().await?;
    if !inbox.is_empty() && pass.hears.is_none() {
        pass.hears = Some(match gateways.asked(&slack.api.auth_test_request()).await {
            Ok(reply) => ns::hears_channel_threads(
                &Slack::auth_test_answer(reply.status, &reply.body),
                reply.header("x-oauth-scopes"),
            ),
            Err(_) => false,
        });
    }
    let hears_channel_threads = pass.hears.unwrap_or(false);
    for m in &inbox {
        // A server that is stopping starts no delivery: what is left waits for the one that starts.
        if *stopping.borrow() {
            break;
        }
        deliver(gateways, slack, gateway, m, hears_channel_threads, pass, tally).await?;
    }
    Ok(())
}

/// Delivers one message of the inbox, or tells its sender why not.
async fn deliver(
    gateways: &Gateways,
    slack: &SlackSide,
    gateway: &Served,
    m: &Envelope,
    hears_channel_threads: bool,
    pass: &mut Pass,
    tally: &mut Tally,
) -> Result<(), Failure> {
    let carried = gateways.store.lock().await.by_brigade_id(&m.message_id);
    if notes::already_carried(carried.as_ref()) {
        gateway.acknowledge(&m.message_id).await?;
        return Ok(());
    }
    let resolved = {
        let store = gateways.store.lock().await;
        resolve_slack_outbound(m, &*store)
    };
    let out = match resolved {
        Ok(out) => out,
        Err(reason) => {
            tell_sender(gateway, ns::undeliverable(m, &reason), tally).await;
            gateway.acknowledge(&m.message_id).await?;
            tally.undeliverable += 1;
            return Ok(());
        }
    };
    let channel = match resolve_channel(gateways, slack, &out.target).await {
        Ok(Some(channel)) => channel,
        Ok(None) => {
            tell_sender(gateway, ns::no_match(m, &out.target), tally).await;
            // The function acknowledged here inside the `try` of the lookup, so an acknowledgement that
            // fails is taken for a failure of the lookup, and a final one: the sender is told again, with
            // the acknowledgement's error, and the message is acknowledged once more.
            if let Err(refusal) = gateway.acknowledge(&m.message_id).await {
                tell_sender(gateway, ns::undeliverable(m, &refusal.message), tally).await;
                gateway.acknowledge(&m.message_id).await?;
            }
            tally.undeliverable += 1;
            return Ok(());
        }
        Err(error) if !ns::is_final(&error) => {
            tally.again(error.code);
            return Ok(());
        }
        Err(error) => {
            tell_sender(gateway, ns::undeliverable(m, &error.message), tally).await;
            gateway.acknowledge(&m.message_id).await?;
            tally.undeliverable += 1;
            return Ok(());
        }
    };
    let mentions = match resolve_mentions(gateways, slack, &out.text, &mut pass.names).await {
        Ok(mentions) => mentions,
        Err(error) if !ns::is_final(&error) => {
            tally.again(error.code);
            return Ok(());
        }
        // A handle Slack would not look up stays text.
        Err(_) => Mentions::new(),
    };
    let text = render_slack_text(
        m,
        &out,
        &SlackRenderContext {
            team_name: gateway.team_name.clone(),
            bot_handle: slack.bot_handle.clone(),
            hears_channel_threads,
            mentions,
        },
    );
    let post = ns::post(m, &out, &channel, &text, &gateway.team_name);
    let posted = match post_with_join(gateways, slack, &post).await {
        Ok(posted) => posted,
        Err(error) if !ns::is_final(&error) => {
            tally.again(error.code);
            return Ok(());
        }
        Err(error) => {
            tell_sender(gateway, ns::not_posted(m, &channel, &error.message), tally).await;
            gateway.acknowledge(&m.message_id).await?;
            tally.undeliverable += 1;
            return Ok(());
        }
    };
    let row = ns::thread_row_out(m, &out, &posted);
    gateways
        .store
        .lock()
        .await
        .record(threads::thread(&gateway.team_ref, &row, note::now()))
        .map_err(Failure::Broken)?;
    gateway.acknowledge(&m.message_id).await?;
    tally.delivered += 1;
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;
    use std::sync::atomic::{AtomicU32, Ordering};

    use brigade_core::Timestamp;
    use brigade_gateway::{Direction, ThreadKind, ThreadRow};
    use serde_json::json;

    use super::*;
    use crate::gateway::standin::{self, Said, StandIn};
    use crate::gateway::state::Kind;
    use crate::gateway::testing::{BOT_TOKEN, World};

    const FOOTER: &str = "\n\nReply in this thread to answer.\n_To write to another session, message @brigade with a first line \"to: <session or member>\"._";

    fn named(slack: &StandIn, name: &str) -> Vec<standin::Asked> {
        slack
            .asked()
            .into_iter()
            .filter(|asked| asked.named() == name)
            .collect()
    }

    #[tokio::test]
    async fn a_message_slack_says_to_send_later_waits_and_is_then_posted_once() {
        // The first post is turned away as Slack turns one away that a later attempt can make.
        let posts = Arc::new(AtomicU32::new(0));
        let counted = Arc::clone(&posts);
        let slack = StandIn::start(move |asked| {
            if asked.named() == "POST /api/chat.postMessage" && counted.fetch_add(1, Ordering::SeqCst) == 0 {
                return Said::json(200, &json!({"ok": false, "error": "ratelimited"}));
            }
            standin::slack(asked)
        });
        let mut world = World::with_slack("slack-out-later", &slack).await;
        let gateway = world.gateway(Kind::Slack).await;
        let body = "to: #general\n\nthe migration has landed, @pat";
        let sent = world.ops.send(&gateway.session_id, body, "migration", None).await;

        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (0, 0, 1));
        assert_eq!(did[0].codes, ["ratelimited"]);
        assert_eq!(world.waiting(Kind::Slack).await, 1);
        assert!(world.gateways.store.lock().await.thread(&sent).is_none());

        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (1, 0, 0));
        assert_eq!(world.waiting(Kind::Slack).await, 0);
        let asked = named(&slack, "POST /api/chat.postMessage");
        assert_eq!(asked.len(), 2);
        assert_eq!(
            asked[1].header("authorization"),
            Some(format!("Bearer {BOT_TOKEN}").as_str())
        );
        // The post is the rules' own: who wrote, the text with the handle made a mention, how to answer.
        assert_eq!(
            asked[1].json(),
            json!({
                "channel": "C0GENERAL1",
                "text": format!(
                    "*payments-api* · alice@example.com\n\nthe migration has landed, <@U0PERSON01>{FOOTER}"
                ),
                "unfurl_links": false,
                "unfurl_media": false,
                "metadata": {
                    "event_type": "brigade_message",
                    "event_payload": {"message_id": sent, "from_session": world.ops.session, "team": "ops"},
                },
            })
        );
        // The row that makes a person's reply in the thread routable.
        let row = world.gateways.store.lock().await.by_brigade_id(&sent).unwrap();
        assert_eq!(
            row,
            ThreadRow {
                brigade_message_id: sent.clone(),
                direction: Direction::Out,
                session_id: world.ops.session.clone(),
                address: "C0GENERAL1".to_owned(),
                mail_message_id: Some("1700000000.000100".to_owned()),
                provider_mail_id: Some("1700000000.000100".to_owned()),
                subject: Some("migration".to_owned()),
                kind: Some(ThreadKind::Slack),
                actor: None,
            }
        );
        // The handle was looked up once in each pass, and what the token hears was asked once in each.
        assert_eq!(named(&slack, "GET /api/users.list").len(), 2);
        assert_eq!(
            named(&slack, "GET /api/users.list")[0].target(),
            "/api/users.list?limit=200"
        );
        assert_eq!(named(&slack, "POST /api/auth.test").len(), 2);

        // A turn that finds nothing asks Slack nothing.
        let before = slack.asked().len();
        let did = world.pass().await;
        assert_eq!(did[0], Tally::default());
        assert_eq!(slack.asked().len(), before);
    }

    #[tokio::test]
    async fn the_bot_joins_a_channel_it_is_not_in_and_a_post_refused_for_good_is_its_senders_to_know() {
        let joined = Arc::new(AtomicU32::new(0));
        let seen = Arc::clone(&joined);
        let slack = StandIn::start(move |asked| {
            let refuse = |error: &str| Said::json(200, &json!({"ok": false, "error": error}));
            match (asked.named().as_str(), asked.json()["channel"].as_str()) {
                ("POST /api/conversations.join", _) => {
                    seen.store(1, Ordering::SeqCst);
                    standin::slack(asked)
                }
                ("POST /api/chat.postMessage", Some("C0GENERAL1")) if seen.load(Ordering::SeqCst) == 0 => {
                    refuse("not_in_channel")
                }
                ("POST /api/chat.postMessage", Some("C0ARCHIVED1")) => refuse("is_archived"),
                _ => standin::slack(asked),
            }
        });
        let mut world = World::with_slack("slack-out-join", &slack).await;
        let gateway = world.gateway(Kind::Slack).await;
        let to = gateway.session_id.as_str();
        let first = world.ops.send(to, "to: C0GENERAL1\n\none", "", None).await;
        let second = world.ops.send(to, "to: C0ARCHIVED1\n\ntwo", "", None).await;
        let third = world.ops.send(to, "to: nobody at all\n\nthree", "", None).await;
        let fourth = world.ops.send(to, "to: #nowhere\n\nfour", "", None).await;

        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (1, 3, 0));
        assert!(did[0].untold.is_empty());
        assert_eq!(world.waiting(Kind::Slack).await, 0);
        // The first was posted twice, around the join; the second once; the others never.
        let channels: Vec<String> = named(&slack, "POST /api/chat.postMessage")
            .iter()
            .map(|asked| asked.json()["channel"].as_str().unwrap().to_owned())
            .collect();
        assert_eq!(channels, ["C0GENERAL1", "C0GENERAL1", "C0ARCHIVED1"]);
        assert_eq!(
            named(&slack, "POST /api/conversations.join")[0].json(),
            json!({"channel": "C0GENERAL1"})
        );
        assert!(world.gateways.store.lock().await.thread(&first).is_some());
        // Each sender is told once, in answer to the message that was not delivered.
        let told = world.ops.inbox().await;
        let notes: Vec<(&str, &str, &str)> = told
            .iter()
            .map(|note| {
                (
                    note["body"].as_str().unwrap(),
                    note["summary"].as_str().unwrap(),
                    note["reply_to"].as_str().unwrap(),
                )
            })
            .collect();
        assert_eq!(
            notes,
            [
                (
                    format!(
                        "The Slack gateway could not post your message {second} to C0ARCHIVED1: slack: chat.postMessage: is_archived"
                    )
                    .as_str(),
                    "slack gateway: not delivered",
                    second.as_str()
                ),
                (
                    format!(
                        "The Slack gateway could not deliver your message {third}: \"to: nobody at all\" is not @name, #channel, an email address or a Slack id"
                    )
                    .as_str(),
                    "slack gateway: not delivered",
                    third.as_str()
                ),
                (
                    format!(
                        "The Slack gateway could not deliver your message {fourth}: no Slack user or channel matches \"#nowhere\""
                    )
                    .as_str(),
                    "slack gateway: not delivered",
                    fourth.as_str()
                ),
            ]
        );
    }

    #[tokio::test]
    async fn a_request_slack_does_not_answer_ends_the_delivery_as_it_did_for_the_functions() {
        // Nothing listens where this gateway's Slack is said to be.
        let closed = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let api = format!("http://{}/api", closed.local_addr().unwrap());
        drop(closed);
        let mut world = World::new("slack-out-silent", |join_secret| {
            crate::gateway::config::Gateways {
                slack: Some(crate::gateway::testing::slack(&api, join_secret)),
                mail: None,
            }
        })
        .await;
        world.pass().await;
        let gateway = world.gateway(Kind::Slack).await;
        let sent = world
            .ops
            .send(&gateway.session_id, "to: @pat\n\nhello", "", None)
            .await;
        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (0, 1, 0));
        assert_eq!(world.waiting(Kind::Slack).await, 0);
        let told = world.ops.inbox().await;
        let body = told[0]["body"].as_str().unwrap();
        assert!(
            body.starts_with(&format!(
                "The Slack gateway could not deliver your message {sent}: 127.0.0.1 could not be connected to"
            )),
            "{body}"
        );
        assert!(!body.contains(BOT_TOKEN));
    }

    #[tokio::test]
    async fn a_message_posted_on_an_earlier_turn_is_acknowledged_and_never_posted_twice() {
        let slack = StandIn::start(standin::slack);
        let mut world = World::with_slack("slack-out-twice", &slack).await;
        let gateway = world.gateway(Kind::Slack).await;
        let sent = world
            .ops
            .send(&gateway.session_id, "to: #general\n\nonce", "", None)
            .await;
        // The turn that posted it ended before the acknowledgement: its row is there, and says `out`.
        let row = ThreadRow {
            brigade_message_id: sent,
            direction: Direction::Out,
            session_id: world.ops.session.clone(),
            address: "C0GENERAL1".to_owned(),
            mail_message_id: Some("1700000000.000100".to_owned()),
            provider_mail_id: Some("1700000000.000100".to_owned()),
            subject: None,
            kind: Some(ThreadKind::Slack),
            actor: None,
        };
        world
            .gateways
            .store
            .lock()
            .await
            .record(threads::thread(&world.ops.team_ref, &row, Timestamp(1)))
            .unwrap();
        let did = world.pass().await;
        assert_eq!(did[0], Tally::default());
        assert_eq!(world.waiting(Kind::Slack).await, 0);
        assert!(named(&slack, "POST /api/chat.postMessage").is_empty());
    }
}
