//! The Resend provider (`supabase/functions/_shared/providers/resend.ts`): the inbound webhook, which is
//! signed the Standard Webhooks way and carries only ids, the fetch of a received mail's body and headers,
//! and the send.

use brigade_core::Timestamp;
use serde::Serialize;

use crate::connector::{HeadersObject, Hook, HttpRequest, Provider, Request, SendError, WebhookRejected};
use crate::connector::{is_ok, stringify};
use crate::js;
use crate::json::{Json, text_of};
use crate::mail::{InboundMail, OutboundMail, header_value, html_to_text, parse_address, parse_message_ids};
use crate::time;
use crate::webhook_signature::{TOLERANCE_SECONDS, verify};

/// Where Resend's API is.
pub const API: &str = "https://api.resend.com";

/// One Resend account, as a connector holds it. It has no `Debug`: it holds the account's key.
#[derive(Clone)]
pub struct Resend {
    /// The API key.
    pub api_key: String,
    /// The webhook signing secret (`whsec_…`). Without it every webhook is rejected: an unsigned inbound
    /// path would let anyone on the internet put text in front of the team's sessions.
    pub webhook_secret: Option<String>,
    /// The API's base URL: [`API`], or a stand-in's.
    pub base_url: String,
}

impl Resend {
    /// An account at Resend's own API.
    #[must_use]
    pub fn new(api_key: impl Into<String>, webhook_secret: Option<String>) -> Self {
        Self {
            api_key: api_key.into(),
            webhook_secret,
            base_url: API.to_owned(),
        }
    }

    fn authorization(&self) -> (&'static str, String) {
        ("Authorization", format!("Bearer {}", self.api_key))
    }
}

/// The body of a send, as Resend's API takes it.
#[derive(Serialize)]
struct SendBody<'a> {
    from: &'a str,
    to: [&'a str; 1],
    subject: &'a str,
    text: &'a str,
    headers: HeadersObject<'a>,
    #[serde(skip_serializing_if = "Option::is_none")]
    reply_to: Option<[&'a str; 1]>,
}

impl Provider for Resend {
    fn name(&self) -> &'static str {
        "resend"
    }

    /// Only an `email.received` event that names a mail is carried. Two signed bodies Resend does not send
    /// are no mail here: JSON `null`, on which the TypeScript failed with an error of the runtime's, and an
    /// `email_id` that is not a string, which the TypeScript carried on as it was to a core that refuses an
    /// id that is not one.
    fn webhook(
        &self,
        request: &Request<'_>,
        raw_body: &str,
        now: Timestamp,
    ) -> Result<Option<Hook>, WebhookRejected> {
        let Some(secret) = self.webhook_secret.as_deref().filter(|secret| !secret.is_empty()) else {
            return Err(WebhookRejected::new(
                "the webhook signing secret is not configured",
            ));
        };
        if !verify(secret, request, raw_body, now, TOLERANCE_SECONDS) {
            return Err(WebhookRejected::new("the webhook signature does not verify"));
        }
        let Some(event) = Json::parse(raw_body) else {
            return Err(WebhookRejected::new("the webhook body is not JSON"));
        };
        if event.get("type").and_then(Json::as_str) != Some("email.received") {
            return Ok(None);
        }
        let email_id = event
            .get("data")
            .and_then(|data| data.get("email_id"))
            .and_then(Json::as_str)
            .filter(|id| !id.is_empty());
        Ok(email_id.map(|id| Hook {
            provider_id: id.to_owned(),
            mail: None,
        }))
    }

    fn fetch_request(&self, provider_id: &str) -> HttpRequest {
        HttpRequest {
            method: "GET",
            url: format!(
                "{}/emails/receiving/{}",
                self.base_url,
                js::encode_uri_component(provider_id)
            ),
            headers: vec![self.authorization()],
            body: None,
        }
    }

    /// The mail's delivery addresses are its `to` and its `received_for` together, each once. Its text is
    /// its text part, or its HTML reduced to text when that part is missing or blank. Its Message-ID is the
    /// one Resend names, or the one in its headers.
    fn fetched(
        &self,
        provider_id: &str,
        status: u16,
        body: &str,
        now: Timestamp,
    ) -> Result<InboundMail, String> {
        if !is_ok(status) {
            return Err(format!("resend: retrieve received email answered {status}"));
        }
        let Some(mail) = Json::parse(body).filter(|mail| *mail != Json::Null) else {
            return Err("resend: the received email could not be read".to_owned());
        };
        let from = parse_address(&text_of(mail.get("from")));
        let headers = || {
            mail.get("headers")
                .map(Json::entries)
                .unwrap_or_default()
                .iter()
                .map(|(name, value)| (name.as_str(), value))
        };
        let mut to: Vec<String> = Vec::new();
        for list in [mail.get("to"), mail.get("received_for")].into_iter().flatten() {
            // `[].concat(value)`: a list is spread and anything else is one element.
            let addresses = match list {
                Json::List(items) => items.as_slice(),
                other => std::slice::from_ref(other),
            };
            for address in addresses {
                let address = parse_address(&address.text()).address;
                if !to.contains(&address) {
                    to.push(address);
                }
            }
        }
        let text = match mail.get("text").and_then(Json::as_str) {
            Some(text) if !js::trim(text).is_empty() => text.to_owned(),
            _ => html_to_text(&text_of(mail.get("html"))),
        };
        let first_id = |header: Option<String>| parse_message_ids(header.as_deref()).into_iter().next();
        Ok(InboundMail {
            provider_id: provider_id.to_owned(),
            from: from.address,
            from_name: from.name,
            to,
            subject: text_of(mail.get("subject")),
            message_id: first_id(
                mail.get("message_id")
                    .map(Json::text)
                    .or_else(|| header_value(headers(), "message-id")),
            ),
            in_reply_to: first_id(header_value(headers(), "in-reply-to")),
            references: parse_message_ids(header_value(headers(), "references").as_deref()),
            text,
            received_at: mail
                .get("created_at")
                .map_or_else(|| time::now_iso(now), Json::text),
        })
    }

    fn send_request(&self, mail: &OutboundMail) -> HttpRequest {
        let body = SendBody {
            from: &mail.from,
            to: [&mail.to],
            subject: &mail.subject,
            text: &mail.text,
            headers: HeadersObject(&mail.headers),
            reply_to: mail
                .reply_to
                .as_deref()
                .filter(|address| !address.is_empty())
                .map(|address| [address]),
        };
        HttpRequest {
            method: "POST",
            url: format!("{}/emails", self.base_url),
            headers: vec![
                self.authorization(),
                ("Content-Type", "application/json".to_owned()),
            ],
            body: Some(stringify(&body)),
        }
    }

    /// A refusal carries Resend's `message`, or its `name`, after the status. An accepted send whose answer
    /// names no id has the id `undefined`, and one whose id is `null` the id `null`: the TypeScript's
    /// `String(d.id)`. One whose answer is not JSON, or is JSON `null`, failed in the TypeScript with an
    /// error of the runtime's, which the connector answered with a 500 that can be tried again; here it is
    /// that failure with a text of this crate's.
    fn sent(&self, status: u16, body: &str) -> Result<String, SendError> {
        let answer = Json::parse(body);
        if !is_ok(status) {
            let detail = answer
                .as_ref()
                .and_then(|answer| answer.get("message").or_else(|| answer.get("name")))
                .map(Json::text)
                .unwrap_or_default();
            let detail = if detail.is_empty() {
                detail
            } else {
                format!(": {detail}")
            };
            return Err(SendError::new(
                status,
                format!("resend: send answered {status}{detail}"),
                None,
            ));
        }
        match answer {
            Some(Json::Null) | None => Err(SendError::new(
                500,
                "resend: the send answer could not be read",
                Some(true),
            )),
            Some(answer) => Ok(answer
                .member("id")
                .map_or_else(|| "undefined".to_owned(), Json::text)),
        }
    }
}
