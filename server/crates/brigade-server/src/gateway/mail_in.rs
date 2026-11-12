//! Mail, inbound: the provider's webhook (`supabase/functions/mail-in/index.ts`, and the connector in
//! front of it, `_shared/connector_serve.ts`).
//!
//! The Edge Functions took a mail in two hops: a connector checked the provider's webhook, fetched the mail
//! where the webhook carried only its id, and handed the mail to the core in the contract's shape; the core
//! decided where it goes. Here there is one hop and no connector secret, and the two decide as they did:
//! the provider's own rule checks the webhook, the mail is read through the contract's shape, which is where
//! a mail without a sender's address was refused, the core's steps follow in their order, and the provider
//! is answered what the connector made of the core's answer, so it delivers again exactly when it did.
//!
//! Three things are not the functions'.
//!
//! No mail is lost between its coming and its going on. The functions wrote a mail down as received before
//! they carried it, and answered every later delivery of it as a duplicate; here it is written down once
//! the gateway is done with it ([`Gateways::carry`]), and a mail that is dropped or is nobody's is never
//! written down at all.
//!
//! A webhook is refused as early as it can be: Postmark's secret comes before the body, so a webhook
//! without it is answered before any of its body is read.
//!
//! And the gateway never answers itself. The functions kept a bounce from going to the inbox; here nothing
//! is mailed to any address that is the gateway's own ([`own_address`]).

use brigade_core::Timestamp;
use brigade_gateway::connector::{self, Provider, Request, WebhookRejected};
use brigade_gateway::notes::{self, mail as nm};
use brigade_gateway::resolve::{addressed_to_inbox, gateway_for_mail, sender_allowed};
use brigade_gateway::{
    Answer, Inbound, InboundMail, OutboundMail, brigade_body, from_inbound_wire, parse_address,
    resolve_inbound, resolve_recipient, roster_text, to_inbound_wire,
};

use super::endpoint::{Received, as_connector_path};
use super::served::{Sent, Served, carried_before, detail};
use super::state::Kind;
use super::{Gateways, MailSide, plain, presence, threads};

/// Why the core fails on a mail for a team whose gateway is configured and has no session yet: the gateway
/// has not joined, or is joining again. The provider delivers the mail again, and by then it may have one.
const NOT_READY: &str = "a mail came for a team whose mail gateway has no session yet";

/// What the core answers for a mail whose earlier delivery is still being carried: a failure, which the
/// provider is told as one, so that it delivers again when the first has decided what the mail became.
const BEING_CARRIED: &str = "an earlier delivery of the mail is being carried";

/// The answer to a webhook that is not the provider's, which the server says sparingly: by the status alone.
fn refused(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    rejected: &WebhookRejected,
    now: Timestamp,
) -> Answer {
    gateways.turned_away(
        &format!(
            "a webhook of {} was refused: {}",
            provider.name(),
            rejected.status
        ),
        now,
    );
    connector::answer_rejected(rejected)
}

/// The answer to a webhook that what came before its body already shows is not the provider's, for a
/// provider whose secret comes there. `None` is a webhook whose body is to be read: one that carries the
/// secret, or one of a provider that signs the body.
#[must_use]
pub fn refused_before_body(
    gateways: &Gateways,
    side: &MailSide,
    received: &Received,
    now: Timestamp,
) -> Option<Answer> {
    if !side.checked_before_body() {
        return None;
    }
    let provider = &*side.provider;
    let headers = received.headers();
    let path = as_connector_path(received.after_provider(provider.name()));
    let request = Request {
        path: &path,
        headers: &headers,
    };
    // The provider's rule looks for its secret before it looks at a body, so it is asked with none: a
    // webhook without the secret is refused here, and one with it is no mail yet.
    match provider.webhook(&request, "", now) {
        Err(rejected) => Some(refused(gateways, provider, &rejected, now)),
        Ok(_) => None,
    }
}

/// Answers one request to the mail provider's endpoint.
pub async fn answer(gateways: &Gateways, side: &MailSide, received: &Received, now: Timestamp) -> Answer {
    let provider = &*side.provider;
    if received.method != "POST" {
        return notes::answer_method_not_allowed();
    }
    // The provider's own check of its webhook.
    let headers = received.headers();
    let path = as_connector_path(received.after_provider(provider.name()));
    let request = Request {
        path: &path,
        headers: &headers,
    };
    let hook = match provider.webhook(&request, &received.body, now) {
        Ok(Some(hook)) => hook,
        Ok(None) => return connector::answer_not_a_mail(),
        Err(rejected) => return refused(gateways, provider, &rejected, now),
    };
    // The mail, fetched where the webhook carried only its id.
    let mail = match hook.mail {
        Some(mail) => mail,
        None => {
            let fetched = match gateways.asked(&provider.fetch_request(&hook.provider_id)).await {
                Ok(reply) => provider.fetched(&hook.provider_id, reply.status, &reply.body, now),
                Err(why) => Err(why),
            };
            match fetched {
                Ok(mail) => mail,
                Err(why) => {
                    // The sentence is the provider rule's or the way out's: a status or a host, no mail.
                    gateways.turned_away(
                        &format!("a received mail could not be read: {}", plain(&why)),
                        now,
                    );
                    return connector::answer_unreadable();
                }
            }
        }
    };
    // What the connector handed the core is the contract's shape, and the core read it back by its rules.
    let core = match from_inbound_wire(&to_inbound_wire(&mail), now) {
        Ok(mail) => match carry(gateways, provider, &mail, now).await {
            Ok(answer) => answer,
            Err(error) => {
                // The provider is answered a failure and delivers the mail again, and that delivery is
                // gone through in full: nothing says the mail came. The sentence is the engine's, the
                // store's or this module's own, nothing the mail carried; the provider is told none of it.
                gateways.turned_away(&format!("the mail endpoint failed: {}", plain(&error)), now);
                notes::answer_failed(&error)
            }
        },
        Err(reason) => nm::answer_malformed(&reason),
    };
    connector::answer_forwarded(Some((core.status, &core.body)))
}

/// Whether an address is one of the gateway's own for a team: the inbox or a tag of it, the address people
/// are told to write to, or the address the gateway's mail is from. Each is compared by its address alone,
/// whatever name is written beside it, and without regard to case.
///
/// The gateway mails nothing to such an address. This is a deliberate departure from the Edge Functions,
/// which kept only a bounce from going to the inbox: where the public address forwards to the inbox, as it
/// does wherever the two differ, a bounce or a command's reply to it comes back as a mail from the gateway,
/// is bounced or answered in turn, and goes round for as long as the provider carries it.
fn own_address(gateway: &Served, address: &str) -> bool {
    let address = parse_address(address).address;
    addressed_to_inbox(&[address.as_str()], &parse_address(&gateway.inbox).address)
        || address == parse_address(&gateway.public_address).address
        || address == parse_address(&gateway.from).address
}

/// Mails the gateway's answer to a mail: a bounce, or a command's reply. Nothing is mailed to an address
/// of the gateway's own, and an answer that fails to send changes nothing of what the provider is told.
async fn mail_answer(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    gateway: &Served,
    answer: &OutboundMail,
) {
    if !own_address(gateway, &answer.to) {
        let _ = gateways.send_mail(provider, answer).await;
    }
}

/// Tells a person by mail why theirs was not delivered, where there is somebody to tell.
async fn bounce(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    gateway: &Served,
    mail: &InboundMail,
    why: nm::Undelivered<'_>,
    roster: &str,
) -> Answer {
    if let Some(out) = nm::bounce(mail, &gateway.render_context(), &why, roster) {
        mail_answer(gateways, provider, gateway, &out).await;
    }
    why.answer()
}

/// Carries one mail: the core's steps. An error is what the core answered as a failure, after which the
/// provider delivers the mail again.
async fn carry(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    mail: &InboundMail,
    now: Timestamp,
) -> Result<Answer, String> {
    // Once only. A delivery of a mail that is being carried now is not gone through beside it: the
    // provider is told to come again, and by then the first has decided what the mail became. One the
    // gateway is done with is a duplicate.
    let key = nm::received_key(mail);
    let Some(_carrying) = gateways.carry(key) else {
        return Ok(notes::answer_failed(BEING_CARRIED));
    };
    if gateways.is_done(key).await {
        return Ok(notes::answer_duplicate());
    }
    let served = gateways.served(Kind::Email).await;
    let Some(gateway) = gateway_for_mail(&served, &mail.to, |gateway| &gateway.inbox) else {
        // A mail for the inbox of a team whose gateway has no session yet is the team's all the same. It
        // is not answered as nobody's, which would end the provider's deliveries of it: the core fails, and
        // the delivery that comes again finds the gateway. A mail that is nobody's is not written down:
        // anyone can send one.
        let configured = gateways
            .teams
            .iter()
            .any(|team| team.kind == Kind::Email && addressed_to_inbox(&mail.to, &team.inbox));
        return if configured {
            Err(NOT_READY.to_owned())
        } else {
            Ok(nm::answer_not_ours())
        };
    };
    if !sender_allowed(&mail.from, Some(&gateway.allowed_senders)) {
        // The function logged the sender's domain. A note carries nothing a request held, so this one
        // names the team alone; and anyone can write to the inbox, so it is said sparingly, and the mail
        // is not written down.
        gateways.turned_away(
            &format!(
                "the {} of team {} dropped a mail: its sender is not on the team's allowed-senders list",
                presence::label(Kind::Email),
                plain(&gateway.team_name)
            ),
            now,
        );
        return Ok(nm::answer_sender_not_allowed());
    }
    let resolution = {
        let store = gateways.store.lock().await;
        resolve_inbound(mail, &gateway.inbox, &*store)
    };
    let sessions = gateway.sessions().await.map_err(|refusal| refusal.message)?;
    let roster = roster_text(
        &gateway.team_name,
        &sessions,
        &gateway.session_id,
        &gateway.public_address,
    );
    // What a person is told by mail is their answer, and the gateway is done with the mail once it is
    // decided, whether or not there was anybody to tell.
    let bounced = async |why: nm::Undelivered<'_>| {
        let answer = bounce(gateways, provider, gateway, mail, why, &roster).await;
        gateways.answered(key, now).await;
        answer
    };
    Ok(match resolution {
        Inbound::Invalid { reason } => bounced(nm::Undelivered::Invalid { reason: &reason }).await,
        Inbound::Unaddressed { .. } => bounced(nm::Undelivered::Unaddressed).await,
        Inbound::Command { command, .. } => {
            let reply = nm::command_reply(mail, &gateway.render_context(), &command, &roster);
            mail_answer(gateways, provider, gateway, &reply).await;
            gateways.answered(key, now).await;
            nm::answer_command(&command)
        }
        Inbound::Message {
            target,
            reply_to,
            summary,
            text,
            source,
        } => {
            let others = sessions
                .iter()
                .filter(|session| session.session_id != gateway.session_id);
            let recipient = match resolve_recipient(&target, others) {
                Ok(recipient) => recipient,
                Err(error) => return Ok(bounced(nm::Undelivered::NotSent { error: &error }).await),
            };
            let to = recipient.session.session_id.as_str();
            let carried = brigade_body(mail, &text);
            let send = nm::send(mail, to, &carried, summary.as_deref(), reply_to.as_deref());
            let sent = match gateway.send(&send).await {
                Ok(sent) => sent,
                Err(refusal) if send.sends_again_unthreaded(refusal.code.as_str()) => gateway
                    .send(&send.unthreaded())
                    .await
                    .map_err(|refusal| refusal.message)?,
                // A mail that is delivered again after the core failed on it, or was cut short, is sent
                // again, and the engine may hold its message already. Sent with the same words the engine
                // answers that message, as a duplicate. Sent with other words it refuses the key, and the
                // row the earlier delivery left says which message it is: the mail is carried, and nobody
                // is told that it was refused. With no such row the engine holds the message all the same,
                // and the mail is one that came before.
                Err(refusal) if carried_before(&refusal) => {
                    let known = gateways
                        .store
                        .lock()
                        .await
                        .carried_in(Kind::Email, &mail.provider_id)
                        .map(|row| row.brigade_message_id.clone());
                    match known {
                        Some(message_id) => Sent {
                            message_id,
                            duplicate: true,
                        },
                        None => {
                            gateways.answered(key, now).await;
                            return Ok(notes::answer_duplicate());
                        }
                    }
                }
                Err(refusal) => {
                    let why = nm::Undelivered::Refused {
                        code: refusal.code.as_str(),
                        detail: detail(&refusal),
                    };
                    return Ok(bounced(why).await);
                }
            };
            // The thread row, and with it the mark that the gateway is done with the mail: the engine has
            // the message, and an answer to it finds the person. A stop or a crash before this leaves no
            // mark, and the delivery the provider makes again records the row.
            let row = nm::thread_row_in(&sent.message_id, to, mail);
            gateways
                .store
                .lock()
                .await
                .record(threads::thread(&gateway.team_ref, &row, now))?;
            gateways.done(key, now).await?;
            nm::answer_carried(&sent.message_id, to, source, sent.duplicate)
        }
    })
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use brigade_gateway::webhook_signature::sign;
    use brigade_gateway::{Sending, ThreadLookup};
    use serde_json::{Value, json};

    use super::*;
    use crate::gateway::config::{self, MailProvider};
    use crate::gateway::endpoint::{self, Route};
    use crate::gateway::standin::{self, Said, StandIn};
    use crate::gateway::testing::{self, API_KEY, WEBHOOK_SECRET, World};
    use crate::note;

    /// Resend's webhook for a received mail, signed as Resend signs one now.
    fn webhook(body: &Value) -> Received {
        let body = body.to_string();
        let timestamp = (note::now().0 / 1_000_000_000).to_string();
        let signature = sign(WEBHOOK_SECRET, "msg_1", &timestamp, &body).unwrap();
        Received {
            method: "POST".to_owned(),
            path: "/gateway/mail/resend".to_owned(),
            headers: vec![
                ("content-type".to_owned(), "application/json".to_owned()),
                ("svix-id".to_owned(), "msg_1".to_owned()),
                ("svix-timestamp".to_owned(), timestamp),
                ("svix-signature".to_owned(), signature),
            ],
            body,
        }
    }

    fn received(id: &str) -> Received {
        webhook(&json!({"type": "email.received", "data": {"email_id": id}}))
    }

    async fn asked(world: &World, received: &Received) -> (u16, Value) {
        let answer = endpoint::answer(&world.gateways, Route::Mail, received).await;
        (
            answer.status,
            serde_json::from_str(&answer.body).unwrap_or(Value::Null),
        )
    }

    /// A stand-in for Resend that holds the received mails a test puts in it, by their ids.
    fn resend_holding(mails: &Arc<Mutex<Vec<(String, Value)>>>) -> StandIn {
        let held = Arc::clone(mails);
        StandIn::start(move |asked| {
            let name = asked.named();
            let held = held.lock().unwrap();
            let mail = name
                .strip_prefix("GET /emails/receiving/")
                .and_then(|id| held.iter().find(|(held, _)| held == id));
            match mail {
                Some((_, mail)) => Said::json(200, mail),
                None => standin::resend(asked),
            }
        })
    }

    #[tokio::test]
    async fn a_resend_webhook_is_checked_by_its_signature_and_its_mail_is_fetched() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        let world = World::with_mail("mail-in-resend", MailProvider::Resend, &resend).await;
        let tail = &world.ops.session[world.ops.session.len() - 5..];
        let put = |id: &str, from: &str, text: &str| {
            let mail = json!({
                "id": id, "from": from, "to": ["Ops@In.Example.com"], "subject": "deploy?",
                "text": text, "message_id": "<m1@example.com>", "headers": {},
                "created_at": "2026-11-12T10:00:00.000Z",
            });
            mails.lock().unwrap().push((id.to_owned(), mail));
        };
        put(
            "re-in-1",
            "Pat Example <Pat@Example.com>",
            &format!("to: {tail}\n\nshall we deploy today?"),
        );
        put("re-in-2", "nobody", &format!("to: {tail}\n\nhello"));
        put("re-in-3", "pat@example.com", "sessions");

        // A webhook with another's signature, or none, is refused before anything is fetched.
        let mut forged = received("re-in-1");
        forged.headers[3].1 = sign(
            "whsec_YW5vdGhlci1zZWNyZXQ=",
            "msg_1",
            &forged.headers[2].1,
            &forged.body,
        )
        .unwrap();
        assert_eq!(
            asked(&world, &forged).await,
            (401, json!({"error": "the webhook signature does not verify"}))
        );
        let mut unsigned = received("re-in-1");
        unsigned.headers.truncate(1);
        assert_eq!(asked(&world, &unsigned).await.0, 401);
        assert!(resend.asked().is_empty());
        // An event that is no received mail is nothing to carry.
        let other = webhook(&json!({"type": "email.delivered", "data": {"email_id": "re-out-9"}}));
        assert_eq!(asked(&world, &other).await, (200, json!({"ignored": true})));

        // The mail is fetched with the provider's key and reaches the session its first line names.
        let (status, said) = asked(&world, &received("re-in-1")).await;
        assert_eq!(status, 200, "{said}");
        assert_eq!(
            (&said["to"], &said["source"], &said["duplicate"]),
            (&json!(world.ops.session), &json!("block"), &json!(false))
        );
        let fetched = resend.asked();
        assert_eq!(fetched[0].named(), "GET /emails/receiving/re-in-1");
        assert_eq!(
            fetched[0].header("authorization"),
            Some(format!("Bearer {API_KEY}").as_str())
        );
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(
            inbox[0]["body"],
            json!(
                "Email from Pat Example <pat@example.com> (unverified), subject \"deploy?\":\n\nshall we deploy today?\n\n[brigade]\nvia: email\nfrom: pat@example.com\nsubject: deploy?\nmail-id: <m1@example.com>\nreceived: 2026-11-12T10:00:00.000Z\n[/brigade]"
            )
        );
        assert_eq!(inbox[0]["summary"], json!("deploy?"));
        assert_eq!(inbox[0]["sender"]["session_name"], json!("mail-gateway"));
        assert_eq!(inbox[0]["sender"]["human_label"], json!("mail gateway"));
        let row = world
            .gateways
            .store
            .lock()
            .await
            .by_brigade_id(said["message_id"].as_str().unwrap())
            .unwrap();
        assert_eq!(
            (
                row.address.as_str(),
                row.mail_message_id.as_deref(),
                row.subject.as_deref()
            ),
            ("pat@example.com", Some("<m1@example.com>"), Some("deploy?"))
        );

        // The same mail delivered again is carried once.
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (200, json!({"duplicate": true}))
        );
        // A mail the provider does not hold is the provider's to deliver again.
        assert_eq!(
            asked(&world, &received("re-in-9")).await,
            (
                502,
                json!({"error": "the mail could not be read from the provider"})
            )
        );
        // A mail whose sender is no address was refused by the core for good, and is dropped.
        assert_eq!(
            asked(&world, &received("re-in-2")).await,
            (200, json!({"dropped": 400}))
        );
        // A command is answered by mail, with the roster.
        assert_eq!(
            asked(&world, &received("re-in-3")).await,
            (200, json!({"command": "sessions"}))
        );
        let sent = resend.asked().pop().unwrap();
        assert_eq!(sent.named(), "POST /emails");
        let reply = sent.json();
        assert_eq!(
            (
                &reply["from"],
                &reply["to"],
                &reply["subject"],
                &reply["reply_to"]
            ),
            (
                &json!("Ops <ops@example.com>"),
                &json!(["pat@example.com"]),
                &json!("[brigade/ops] sessions"),
                &json!(["ops@in.example.com"])
            )
        );
        assert_eq!(reply["headers"], json!({"Auto-Submitted": "auto-replied"}));
        assert_eq!(
            reply["text"],
            json!(format!(
                "Sessions on Brigade team ops right now (names and labels are their owners' own words):\n\n  {tail}  payments-api  idle  alice@example.com\n\nTo write to one, send an email to ops@example.com whose first line is:\n\n  to: <the five characters in the first column, or a member's label>\n"
            ))
        );
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn a_reply_that_quotes_the_gateways_mail_goes_back_to_the_session_as_an_answer() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        let mut world = World::with_mail("mail-in-reply", MailProvider::Resend, &resend).await;
        let gateway = world.gateway(Kind::Email).await;
        let question = world
            .ops
            .send(
                &gateway.session_id,
                "to: pat@example.com\n\nshall I deploy?",
                "",
                None,
            )
            .await;
        world.pass().await;
        let sent = resend.asked().pop().unwrap().json();
        assert_eq!(sent["subject"], json!("[brigade/ops] shall I deploy?"));
        assert_eq!(
            sent["reply_to"],
            json!([format!("ops+{question}@in.example.com")])
        );

        // The person answers from their mail client, which quotes what it answers under their words.
        let quoted: Vec<String> = sent["text"]
            .as_str()
            .unwrap()
            .lines()
            .map(|line| format!("> {line}"))
            .collect();
        let answer = json!({
            "id": "re-in-1", "from": "pat@example.com", "to": sent["reply_to"],
            "subject": "Re: [brigade/ops] shall I deploy?",
            "text": format!(
                "yes, go ahead\n\nOn Thu, 12 Nov 2026 at 10:00, Ops <ops@example.com> wrote:\n{}\n",
                quoted.join("\n")
            ),
            "message_id": "<m2@example.com>", "headers": {},
            "created_at": "2026-11-12T10:05:00.000Z",
        });
        mails.lock().unwrap().push(("re-in-1".to_owned(), answer));
        let (status, said) = asked(&world, &received("re-in-1")).await;
        assert_eq!(status, 200, "{said}");
        assert_eq!(said["to"], json!(world.ops.session));
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["reply_to"], json!(question));
        assert!(
            inbox[0]["body"]
                .as_str()
                .unwrap()
                .starts_with("Email from pat@example.com (unverified), subject \"Re: [brigade/ops] shall I deploy?\":\n\nyes, go ahead\n\n[brigade]\n")
        );

        // The session's answer to that goes out in the person's thread.
        let reply = world
            .ops
            .send(&gateway.session_id, "deployed", "", said["message_id"].as_str())
            .await;
        world.pass().await;
        let sent = resend.asked().pop().unwrap().json();
        assert_eq!(
            (&sent["to"], &sent["subject"]),
            (
                &json!(["pat@example.com"]),
                &json!("Re: [brigade/ops] shall I deploy?")
            )
        );
        assert_eq!(
            sent["headers"],
            json!({
                "Auto-Submitted": "auto-generated",
                "In-Reply-To": "<m2@example.com>",
                "References": "<m2@example.com>",
            })
        );
        assert_eq!(sent["reply_to"], json!([format!("ops+{reply}@in.example.com")]));
    }

    #[tokio::test]
    async fn a_mail_that_comes_before_the_gateway_has_a_session_is_carried_when_it_is_delivered_again() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        // No gateway has taken its place: the team's inbox is configured, and nobody answers for it yet.
        let mut world = World::new("mail-in-early", |join_secret| config::Gateways {
            slack: None,
            mail: Some(testing::mail(MailProvider::Resend, &resend.url, join_secret)),
        })
        .await;
        let put = |id: &str, to: &str| {
            let mail = json!({
                "id": id, "from": "pat@example.com", "to": [to], "subject": "deploy?",
                "text": "to: alice@example.com\n\nshall we deploy today?", "headers": {},
                "created_at": "2026-11-12T10:00:00.000Z",
            });
            mails.lock().unwrap().push((id.to_owned(), mail));
        };
        put("re-in-1", "ops@in.example.com");
        put("re-in-2", "someone@elsewhere.example.com");
        // The provider is answered what makes it deliver again, where the functions would have called the
        // mail nobody's and ended its deliveries.
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (502, json!({"error": "the gateway core answered 500"}))
        );
        // A mail for no inbox of the server's is nobody's, whoever has a session.
        assert_eq!(
            asked(&world, &received("re-in-2")).await,
            (
                200,
                json!({"ignored": "not addressed to a gateway inbox of this project"})
            )
        );

        // The gateway takes its place, the provider delivers again, and the mail is carried.
        world.pass().await;
        let (status, said) = asked(&world, &received("re-in-1")).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        let inbox = world.ops.inbox().await;
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["message_id"], said["message_id"]);
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (200, json!({"duplicate": true}))
        );
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn a_mail_the_engine_already_holds_is_carried_as_a_duplicate_and_nobody_is_told_it_was_refused() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        let mut world = World::with_mail("mail-in-held", MailProvider::Resend, &resend).await;
        let put = |id: &str, received_at: &str| {
            let mail = json!({
                "id": id, "from": "pat@example.com", "to": ["ops@in.example.com"], "subject": "deploy?",
                "text": "to: alice@example.com\n\nshall we deploy today?", "headers": {},
                "created_at": received_at,
            });
            let mut held = mails.lock().unwrap();
            held.retain(|(held, _)| held != id);
            held.push((id.to_owned(), mail));
        };

        // A mail is carried, and the server ends as if it had got no further than the send: the engine
        // holds the message, and the state's file neither its row nor a word of the mail. The provider
        // delivers again, the mail is gone through in full, the engine answers the message it holds, and
        // the row is recorded.
        put("re-in-1", "2026-11-12T10:00:00.000Z");
        let (_, first) = asked(&world, &received("re-in-1")).await;
        assert_eq!(first["duplicate"], json!(false), "{first}");
        world.crash(|state| {
            state["threads"] = json!([]);
            state["received"] = json!([]);
        });
        let (status, again) = asked(&world, &received("re-in-1")).await;
        assert_eq!(status, 200);
        assert_eq!(
            (&again["message_id"], &again["to"], &again["duplicate"]),
            (&first["message_id"], &first["to"], &json!(true))
        );
        let message_id = first["message_id"].as_str().unwrap();
        assert!(
            world
                .gateways
                .store
                .lock()
                .await
                .by_brigade_id(message_id)
                .is_some()
        );
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (200, json!({"duplicate": true}))
        );

        // The same when the row was written and the mark was not, and the second delivery reads
        // differently, here by the time the provider gives it: the engine refuses the mail's key for other
        // words, and the row of the first delivery says which message it holds.
        world.crash(|state| state["received"] = json!([]));
        put("re-in-1", "2026-11-12T10:00:01.000Z");
        let (status, again) = asked(&world, &received("re-in-1")).await;
        assert_eq!(status, 200);
        assert_eq!(
            (&again["message_id"], &again["duplicate"]),
            (&first["message_id"], &json!(true))
        );

        // And when the engine holds a message for a mail of which the server holds no row, the mail is one
        // that came before, and that is all there is to say of it, now and when it comes again.
        let gateway = world.gateway(Kind::Email).await;
        let earlier = Sending {
            recipient_session_id: world.ops.session.clone(),
            body: "an earlier rendering of the mail".to_owned(),
            summary: "deploy?".to_owned(),
            reply_to: None,
            idempotency_key: "mail:re-in-2".to_owned(),
        };
        gateway.send(&earlier).await.unwrap();
        put("re-in-2", "2026-11-12T10:00:00.000Z");
        assert_eq!(
            asked(&world, &received("re-in-2")).await,
            (200, json!({"duplicate": true}))
        );
        assert!(world.gateways.is_done("re-in-2").await);

        // Two mails reached the session, each once, and the person was never written to: no bounce said
        // that Brigade refused a mail it had carried.
        assert_eq!(world.ops.inbox().await.len(), 2);
        assert!(resend.asked().iter().all(|asked| asked.named() != "POST /emails"));
    }

    #[tokio::test]
    async fn a_delivery_of_a_mail_that_is_being_carried_is_told_to_come_again() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        let world = World::with_mail("mail-in-carrying", MailProvider::Resend, &resend).await;
        let mail = json!({
            "id": "re-in-1", "from": "pat@example.com", "to": ["ops@in.example.com"], "subject": "deploy?",
            "text": "to: alice@example.com\n\nshall we deploy today?", "headers": {},
            "created_at": "2026-11-12T10:00:00.000Z",
        });
        mails.lock().unwrap().push(("re-in-1".to_owned(), mail));
        // The first delivery is being carried when the provider delivers again: the second is answered
        // what makes the provider come once more, and nothing is done for it.
        let first = world.gateways.carry("re-in-1").expect("nothing carries it yet");
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (502, json!({"error": "the gateway core answered 500"}))
        );
        assert!(world.ops.inbox().await.is_empty());
        drop(first);
        let (status, said) = asked(&world, &received("re-in-1")).await;
        assert_eq!((status, &said["duplicate"]), (200, &json!(false)), "{said}");
        assert_eq!(
            asked(&world, &received("re-in-1")).await,
            (200, json!({"duplicate": true}))
        );
        assert_eq!(world.ops.inbox().await.len(), 1);
    }

    #[tokio::test]
    async fn a_mail_that_is_dropped_or_nobodys_is_never_written_down() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        let mut world = World::new("mail-in-dropped", |join_secret| {
            let mut mail = testing::mail(MailProvider::Resend, &resend.url, join_secret);
            mail.teams[0].allowed_senders = vec!["@example.com".to_owned()];
            config::Gateways {
                slack: None,
                mail: Some(mail),
            }
        })
        .await;
        world.pass().await;
        let file = world.data.join("gateway").join("state.json");
        let before = std::fs::read(&file).unwrap();
        // Strangers write to the inbox, and to addresses that are no inbox of the server's, as often as
        // they like.
        for at in 0..50 {
            let (id, to) = (
                format!("re-in-{at}"),
                ["ops@in.example.com", "x@elsewhere.example.com"][at % 2],
            );
            let mail = json!({
                "id": id, "from": "mallory@elsewhere.test", "to": [to], "subject": "hello",
                "text": "to: alice@example.com\n\nlet me in", "headers": {},
                "created_at": "2026-11-12T10:00:00.000Z",
            });
            mails.lock().unwrap().push((id.clone(), mail));
            let (status, said) = asked(&world, &received(&id)).await;
            assert_eq!(status, 200, "{said}");
            assert!(said["ignored"].is_string(), "{said}");
            // Delivered again, it is dropped again, and not a duplicate of anything.
            assert!(asked(&world, &received(&id)).await.1["ignored"].is_string());
        }
        // The state's file is what it was, to the byte, and nobody was written to.
        assert_eq!(std::fs::read(&file).unwrap(), before);
        assert!(world.ops.inbox().await.is_empty());
        assert!(resend.asked().iter().all(|asked| asked.named() != "POST /emails"));
    }

    #[tokio::test]
    async fn the_gateway_never_answers_an_address_of_its_own() {
        let mails = Arc::new(Mutex::new(Vec::new()));
        let resend = resend_holding(&mails);
        // The address people write to and the address the gateway's mail is from are not the inbox, and
        // are written with capitals and a name, as an operator writes them.
        let mut world = World::new("mail-in-own", |join_secret| {
            let mut mail = testing::mail(MailProvider::Resend, &resend.url, join_secret);
            mail.teams[0].public_address = "Help@Example.com".to_owned();
            mail.teams[0].from = "Ops Team <NoReply@Example.com>".to_owned();
            config::Gateways {
                slack: None,
                mail: Some(mail),
            }
        })
        .await;
        world.pass().await;
        let sent = || {
            resend
                .asked()
                .iter()
                .filter(|asked| asked.named() == "POST /emails")
                .count()
        };
        let mut delivered = 0;
        let mut deliver = |from: &str, text: &str| {
            delivered += 1;
            let id = format!("re-in-{delivered}");
            let mail = json!({
                "id": id, "from": from, "to": ["ops@in.example.com"], "subject": "hello",
                "text": text, "headers": {}, "created_at": "2026-11-12T10:00:00.000Z",
            });
            mails.lock().unwrap().push((id.clone(), mail));
            received(&id)
        };

        // A mail that names nobody would be bounced, and a command answered with the roster. From the
        // inbox, from a tag of it, from the public address, which may forward to the inbox, and from the
        // address the gateway writes from, neither is: the answer would come back as a mail of its own.
        for own in [
            "ops@in.example.com",
            "Ops <OPS+0123456789abcdef0123456789abcdef@In.Example.com>",
            "Help Desk <HELP@example.com>",
            "noreply@EXAMPLE.com",
        ] {
            assert_eq!(
                asked(&world, &deliver(own, "is anybody there?")).await,
                (200, json!({"bounced": "unaddressed"})),
                "{own}"
            );
            assert_eq!(
                asked(&world, &deliver(own, "sessions")).await,
                (200, json!({"command": "sessions"})),
                "{own}"
            );
            assert_eq!(
                asked(&world, &deliver(own, "to: nobody-here\n\nhello")).await,
                (200, json!({"bounced": "not_found"})),
                "{own}"
            );
            assert_eq!(sent(), 0, "{own}");
        }
        // Anybody else is answered, as before.
        assert_eq!(
            asked(&world, &deliver("pat@example.com", "is anybody there?")).await,
            (200, json!({"bounced": "unaddressed"}))
        );
        assert_eq!(sent(), 1);
        assert_eq!(
            asked(&world, &deliver("Pat <pat@example.com>", "sessions")).await,
            (200, json!({"command": "sessions"}))
        );
        assert_eq!(sent(), 2);
        // An address that only looks like one of the gateway's is somebody else's.
        assert_eq!(
            asked(&world, &deliver("help@example.com.elsewhere.test", "sessions")).await,
            (200, json!({"command": "sessions"}))
        );
        assert_eq!(sent(), 3);
    }
}
