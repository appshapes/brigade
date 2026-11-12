//! The Postmark provider (`supabase/functions/_shared/providers/postmark.ts`). Postmark's inbound webhook
//! carries the whole mail, so no fetch follows it; it carries no signature, so the connector authenticates
//! it by the basic-auth password the installer put in the webhook URL (or the same secret as a path
//! segment), and answers 403 to anything else, which is the status that stops Postmark's retries. Sending
//! is one POST to `/email` with the server token.

use std::collections::HashSet;

use brigade_core::Timestamp;
use serde::Serialize;

use crate::connector::{Hook, HttpRequest, Provider, Request, SendError, WebhookRejected, authorized};
use crate::connector::{is_ok, stringify};
use crate::js;
use crate::json::{Json, text_of};
use crate::mail::{InboundMail, OutboundMail, header_value, html_to_text, parse_address, parse_message_ids};
use crate::time;

/// Where Postmark's API is.
pub const API: &str = "https://api.postmarkapp.com";

/// One Postmark server, as a connector holds it. It has no `Debug`: it holds the server's token.
#[derive(Clone)]
pub struct Postmark {
    /// The server token.
    pub server_token: String,
    /// The webhook password (`https://brigade:<password>@…` in the server's `InboundHookUrl`). Without it
    /// every webhook is rejected: an unauthenticated inbound path would let anyone put text in front of the
    /// team.
    pub webhook_secret: Option<String>,
    /// The API's base URL: [`API`], or a stand-in's.
    pub base_url: String,
}

impl Postmark {
    /// A server at Postmark's own API.
    #[must_use]
    pub fn new(server_token: impl Into<String>, webhook_secret: Option<String>) -> Self {
        Self {
            server_token: server_token.into(),
            webhook_secret,
            base_url: API.to_owned(),
        }
    }

    fn headers(&self) -> Vec<(&'static str, String)> {
        vec![
            ("X-Postmark-Server-Token", self.server_token.clone()),
            ("Accept", "application/json".to_owned()),
        ]
    }
}

/// The body of a send, as Postmark's API takes it.
#[derive(Serialize)]
#[serde(rename_all = "PascalCase")]
struct SendBody<'a> {
    from: &'a str,
    to: &'a str,
    subject: &'a str,
    text_body: &'a str,
    message_stream: &'static str,
    headers: Vec<SendHeader<'a>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    reply_to: Option<&'a str>,
}

#[derive(Serialize)]
#[serde(rename_all = "PascalCase")]
struct SendHeader<'a> {
    name: &'a str,
    value: &'a str,
}

/// Maps an inbound webhook payload (or an inbound message's details) to a mail. A recipient with a
/// `MailboxHash` whose `Email` lost the `+tag` gets it back, so the gateway's Reply-To tag resolves. A
/// `Date` that JavaScript's `Date` cannot read is `now`.
///
/// A member of the wrong JSON type is read as the TypeScript's `String()` reads it, with one exception: a
/// list of recipients or of headers that is not a list is read as empty, where the TypeScript failed with
/// an error of the runtime's. Postmark sends none.
///
/// # Errors
/// The payload is not a JSON object.
pub fn inbound(payload: &str, now: Timestamp) -> Result<InboundMail, String> {
    match Json::parse(payload) {
        Some(document @ Json::Object(_)) => Ok(inbound_mail(&document, None, now)),
        _ => Err("postmark: the payload is not a JSON object".to_owned()),
    }
}

/// The mapping itself. `fallback_id` is the id a fetch asked for, for details that name none.
fn inbound_mail(document: &Json, fallback_id: Option<&str>, now: Timestamp) -> InboundMail {
    let from_full = |name: &str| document.get("FromFull").and_then(|full| full.get(name));
    let from = parse_address(&text_of(from_full("Email").or(document.get("From"))));
    let from_name = text_of(from_full("Name").or(document.get("FromName")));
    let from_name = match js::trim(&from_name) {
        "" => from.name,
        name => Some(name.to_owned()),
    };
    let mut to: Vec<String> = Vec::new();
    let mut add = |address: String| {
        if !to.contains(&address) {
            to.push(address);
        }
    };
    for list in ["ToFull", "CcFull", "BccFull"] {
        for recipient in document.get(list).map(Json::items).unwrap_or_default() {
            let address = with_hash(
                &text_of(recipient.get("Email")),
                &text_of(recipient.get("MailboxHash")),
            );
            if !address.is_empty() {
                add(address);
            }
        }
    }
    // The original recipient is added even when nothing is left of it but white space: the TypeScript
    // checks the address it was given and not the one it adds.
    if let Some(original) = document
        .get("OriginalRecipient")
        .and_then(Json::as_str)
        .filter(|address| !address.is_empty())
    {
        add(with_hash(original, &text_of(document.get("MailboxHash"))));
    }
    // The first header of a name is the one kept. The names seen are held in a set: a mail may carry as many
    // headers as its sender likes, and looking each one up in the list would cost the square of their number.
    let mut headers: Vec<(String, Json)> = Vec::new();
    let mut seen: HashSet<&str> = HashSet::new();
    for header in document.get("Headers").map(Json::items).unwrap_or_default() {
        if let Some(name) = header.get("Name").and_then(Json::as_str)
            && seen.insert(name)
        {
            headers.push((name.to_owned(), Json::Text(text_of(header.get("Value")))));
        }
    }
    let ids = |name: &str| {
        let header = header_value(headers.iter().map(|(name, value)| (name.as_str(), value)), name);
        parse_message_ids(header.as_deref())
    };
    let text = match document.get("TextBody").and_then(Json::as_str) {
        Some(text) if !js::trim(text).is_empty() => text.to_owned(),
        _ => html_to_text(&text_of(document.get("HtmlBody"))),
    };
    InboundMail {
        provider_id: document
            .get("MessageID")
            .map_or_else(|| fallback_id.unwrap_or("").to_owned(), Json::text),
        from: from.address,
        from_name,
        to,
        subject: text_of(document.get("Subject")),
        message_id: ids("message-id").into_iter().next(),
        in_reply_to: ids("in-reply-to").into_iter().next(),
        references: ids("references"),
        text,
        received_at: time::parse(&text_of(document.get("Date")))
            .map_or_else(|| time::now_iso(now), time::iso),
    }
}

/// Lowercases an address and puts a `MailboxHash` back as its `+tag` when the address lacks one.
#[must_use]
pub fn with_hash(email: &str, hash: &str) -> String {
    let address = js::trim(email).to_lowercase();
    match address.rfind('@') {
        Some(at) if !hash.is_empty() && !address[..at].contains('+') => format!(
            "{}+{}@{}",
            &address[..at],
            js::trim(hash).to_lowercase(),
            &address[at + 1..]
        ),
        _ => address,
    }
}

impl Provider for Postmark {
    fn name(&self) -> &'static str {
        "postmark"
    }

    /// A payload is a received mail when it has a `MessageID`, a sender, and a `MessageStream` that is
    /// `inbound` or missing: a bounce or a delivery report on the same URL is no mail.
    fn webhook(
        &self,
        request: &Request<'_>,
        raw_body: &str,
        now: Timestamp,
    ) -> Result<Option<Hook>, WebhookRejected> {
        let Some(secret) = self.webhook_secret.as_deref().filter(|secret| !secret.is_empty()) else {
            return Err(WebhookRejected::with_status(
                "the webhook secret is not configured",
                403,
            ));
        };
        if !authorized(request, "webhook", secret) {
            return Err(WebhookRejected::with_status(
                "the request does not carry this connector's webhook secret",
                403,
            ));
        }
        let Some(document @ Json::Object(_)) = Json::parse(raw_body) else {
            return Ok(None);
        };
        let Some(provider_id) = document
            .get("MessageID")
            .and_then(Json::as_str)
            .filter(|id| !id.is_empty())
        else {
            return Ok(None);
        };
        let has_sender = ["FromFull", "From"]
            .iter()
            .any(|name| document.get(name).is_some_and(Json::truthy));
        let stream = document.get("MessageStream").and_then(Json::as_str);
        if !has_sender || stream.is_some_and(|stream| stream != "inbound") {
            return Ok(None);
        }
        Ok(Some(Hook {
            provider_id: provider_id.to_owned(),
            mail: Some(inbound_mail(&document, None, now)),
        }))
    }

    fn fetch_request(&self, provider_id: &str) -> HttpRequest {
        HttpRequest {
            method: "GET",
            url: format!(
                "{}/messages/inbound/{}/details",
                self.base_url,
                js::encode_uri_component(provider_id)
            ),
            headers: self.headers(),
            body: None,
        }
    }

    /// The details of an inbound message go through the webhook's own mapping; details that name no
    /// `MessageID` are the mail that was asked for.
    fn fetched(
        &self,
        provider_id: &str,
        status: u16,
        body: &str,
        now: Timestamp,
    ) -> Result<InboundMail, String> {
        if !is_ok(status) {
            return Err(format!("postmark: inbound message details answered {status}"));
        }
        match Json::parse(body) {
            Some(Json::Null) | None => {
                Err("postmark: the inbound message details could not be read".to_owned())
            }
            Some(details) => Ok(inbound_mail(&details, Some(provider_id), now)),
        }
    }

    fn send_request(&self, mail: &OutboundMail) -> HttpRequest {
        let body = SendBody {
            from: &mail.from,
            to: &mail.to,
            subject: &mail.subject,
            text_body: &mail.text,
            message_stream: "outbound",
            headers: mail
                .headers
                .iter()
                .map(|(name, value)| SendHeader { name, value })
                .collect(),
            reply_to: mail.reply_to.as_deref().filter(|address| !address.is_empty()),
        };
        let mut headers = self.headers();
        headers.push(("Content-Type", "application/json".to_owned()));
        HttpRequest {
            method: "POST",
            url: format!("{}/email", self.base_url),
            headers,
            body: Some(stringify(&body)),
        }
    }

    /// Postmark can refuse a mail with a 200: the answer's `ErrorCode` decides with the status. A refusal
    /// can be tried again only for a 429 or a 5xx.
    fn sent(&self, status: u16, body: &str) -> Result<String, SendError> {
        let answer = Json::parse(body).unwrap_or(Json::Null);
        let code = match answer.get("ErrorCode") {
            Some(Json::Number(code)) => *code,
            _ => 0.0,
        };
        if !is_ok(status) || code != 0.0 {
            let code = if code == 0.0 {
                String::new()
            } else {
                format!(" (error {})", js::number_text(code))
            };
            let message = match answer.get("Message").filter(|message| message.truthy()) {
                Some(message) => format!(": {}", message.text()),
                None => String::new(),
            };
            return Err(SendError::new(
                status,
                format!("postmark: send answered {status}{code}{message}"),
                Some(status == 429 || status >= 500),
            ));
        }
        Ok(text_of(answer.get("MessageID")))
    }
}
