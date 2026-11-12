//! Mail, outbound: one turn of one team's gateway (`supabase/functions/mail-out/index.ts`).
//!
//! A turn takes what waits in the gateway session's inbox, oldest first, and for each message finds the
//! address, writes the mail, sends it through the provider, records the thread and acknowledges it. A
//! message that cannot be mailed is acknowledged too, once its sender has been told; one a later attempt
//! can send stays in the inbox for the next turn. The steps and their order are the Edge Function's.
//!
//! The function sent through a connector, a second function between it and the provider. Here the provider
//! is asked directly, with the same request and the same reading of its answer.

use brigade_gateway::connector::{Provider, SendError};
use brigade_gateway::notes::{self, mail as nm};
use brigade_gateway::{Envelope, ThreadLookup, render_outbound, resolve_outbound};
use tokio::sync::watch;

use super::presence::Failure;
use super::served::Served;
use super::turns::{Tally, tell_sender};
use super::{Gateways, threads};
use crate::note;

/// What a failed send is noted by: the status the provider answered, or that it did not answer.
fn code(error: &SendError) -> String {
    match error.status {
        0 => "no answer".to_owned(),
        status => status.to_string(),
    }
}

/// One turn of one team's mail gateway: what waits in its inbox is mailed, or its sender told.
///
/// # Errors
/// The engine refused the receive or an acknowledgement, or a thread row could not be written. What the
/// turn had not reached waits for the next one.
pub async fn turn(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    gateway: &Served,
    tally: &mut Tally,
    stopping: &watch::Receiver<bool>,
) -> Result<(), Failure> {
    let inbox = gateway.inbox().await?;
    for m in &inbox {
        // A server that is stopping starts no delivery: what is left waits for the one that starts.
        if *stopping.borrow() {
            break;
        }
        deliver(gateways, provider, gateway, m, tally).await?;
    }
    Ok(())
}

/// Mails one message of the inbox, or tells its sender why not.
async fn deliver(
    gateways: &Gateways,
    provider: &(dyn Provider + Send + Sync),
    gateway: &Served,
    m: &Envelope,
    tally: &mut Tally,
) -> Result<(), Failure> {
    let carried = gateways.store.lock().await.by_brigade_id(&m.message_id);
    if notes::already_carried(carried.as_ref()) {
        gateway.acknowledge(&m.message_id).await?;
        return Ok(());
    }
    let resolved = {
        let store = gateways.store.lock().await;
        resolve_outbound(m, &*store, &gateway.team_name)
    };
    let out = match resolved {
        Ok(out) => out,
        Err(reason) => {
            tell_sender(gateway, nm::undeliverable(m, &reason), tally).await;
            gateway.acknowledge(&m.message_id).await?;
            tally.undeliverable += 1;
            return Ok(());
        }
    };
    let rendered = render_outbound(m, &out, &gateway.render_context());
    let provider_id = match gateways.send_mail(provider, &rendered).await {
        Ok(provider_id) => provider_id,
        Err(error) if nm::is_final(&error) => {
            tell_sender(gateway, nm::not_mailed(m, &out.address, &error.message), tally).await;
            gateway.acknowledge(&m.message_id).await?;
            tally.undeliverable += 1;
            return Ok(());
        }
        Err(error) => {
            tally.again(code(&error));
            return Ok(());
        }
    };
    let row = nm::thread_row_out(m, &out, &provider_id, &rendered);
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

    use serde_json::json;

    use super::*;
    use crate::gateway::config::MailProvider;
    use crate::gateway::standin::{self, Said, StandIn};
    use crate::gateway::state::Kind;
    use crate::gateway::testing::{API_KEY, World};

    #[tokio::test]
    async fn a_mail_the_provider_cannot_take_now_waits_and_one_it_refuses_for_good_is_its_senders_to_know() {
        // The provider is down for the first send, and never takes a mail for one address.
        let sends = Arc::new(AtomicU32::new(0));
        let counted = Arc::clone(&sends);
        let postmark = StandIn::start(move |asked| {
            if asked.named() != "POST /email" {
                return standin::postmark(asked);
            }
            if asked.json()["To"] == json!("gone@example.com") {
                return Said::json(422, &json!({"ErrorCode": 406, "Message": "Inactive recipient"}));
            }
            if counted.fetch_add(1, Ordering::SeqCst) == 0 {
                return Said::json(503, &json!({"ErrorCode": 0, "Message": "try again"}));
            }
            standin::postmark(asked)
        });
        let mut world = World::with_mail("mail-out-later", MailProvider::Postmark, &postmark).await;
        let gateway = world.gateway(Kind::Email).await;
        let to = gateway.session_id.as_str();
        let first = world
            .ops
            .send(
                to,
                "to: Pat@Example.com\nsubject: the migration\n\nit has landed",
                "",
                None,
            )
            .await;
        let second = world
            .ops
            .send(to, "to: gone@example.com\n\nare you there?", "", None)
            .await;
        let third = world.ops.send(to, "it has landed", "", None).await;

        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (0, 2, 1));
        assert_eq!(did[0].codes, ["503"]);
        assert_eq!(world.waiting(Kind::Email).await, 1);
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
                        "The mail gateway could not deliver your message {second} to gone@example.com: postmark: send answered 422 (error 406): Inactive recipient"
                    )
                    .as_str(),
                    "mail gateway: not delivered",
                    second.as_str()
                ),
                (
                    format!(
                        "The mail gateway could not deliver your message {third}: no address: start the body with a line \"to: <email address>\", or reply (--reply-to) to a message that came from an email"
                    )
                    .as_str(),
                    "mail gateway: not delivered",
                    third.as_str()
                ),
            ]
        );

        // The next turn sends what waited, once.
        let did = world.pass().await;
        assert_eq!((did[0].delivered, did[0].undeliverable, did[0].retry), (1, 0, 0));
        assert_eq!(world.waiting(Kind::Email).await, 0);
        let asked: Vec<standin::Asked> = postmark
            .asked()
            .into_iter()
            .filter(|asked| asked.json()["To"] == json!("pat@example.com"))
            .collect();
        assert_eq!(asked.len(), 2);
        assert_eq!(asked[1].header("x-postmark-server-token"), Some(API_KEY));
        let mail = asked[1].json();
        assert_eq!(
            (&mail["From"], &mail["Subject"], &mail["MessageStream"]),
            (
                &json!("Ops <ops@example.com>"),
                &json!("the migration"),
                &json!("outbound")
            )
        );
        assert_eq!(mail["ReplyTo"], json!(format!("ops+{first}@in.example.com")));
        assert_eq!(
            mail["Headers"],
            json!([{"Name": "Auto-Submitted", "Value": "auto-generated"}])
        );
        let row = world.gateways.store.lock().await.by_brigade_id(&first).unwrap();
        assert_eq!(
            (row.address.as_str(), row.subject.as_deref(), row.mail_message_id),
            ("pat@example.com", Some("the migration"), None)
        );
        assert!(row.provider_mail_id.is_some_and(|id| id.starts_with("pm-out-")));
        let before = postmark.asked().len();
        world.pass().await;
        assert_eq!(postmark.asked().len(), before);
    }
}
