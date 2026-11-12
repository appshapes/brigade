//! The gateways' endpoints on the server's own listener, outside `/v1/`.
//!
//! `POST /gateway/slack` is Slack's: the Events API and the slash command. `POST /gateway/mail/resend` and
//! `POST /gateway/mail/postmark` are the mail provider's webhook, the one the operator configured. A path
//! that is none of these, or that is a gateway's the operator did not configure, is not an endpoint at all:
//! the HTTP layer answers it as it answers any path the contract does not know.
//!
//! An endpoint answers with the status and the JSON the rules of `brigade-gateway` give, which are what the
//! Edge Functions answered Slack and the providers with.
//!
//! Anyone can send a request here, so as little as can be is done for one before it is known whose it is:
//! a request that what came before its body already decides is answered before any of its body is read
//! ([`before_body`]), and a body that has to be read to know is read up to a megabyte.

use brigade_gateway::{Answer, notes};
use serde_json::json;

use super::{Gateways, mail_in, slack_in};
use crate::note;

/// Slack's endpoint.
const SLACK_PATH: &str = "/gateway/slack";

/// What every mail provider's endpoint begins with; the provider's name follows.
const MAIL_PATH: &str = "/gateway/mail/";

/// The most of a request from Slack that is read: an event or a slash command is a few kilobytes.
const SLACK_BYTES: usize = 1 << 20;

/// Which endpoint a request is for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Route {
    /// Slack's Events API and slash command.
    Slack,
    /// The mail provider's webhook.
    Mail,
}

impl Route {
    /// The endpoint a path names, among those of the gateways the operator configured.
    #[must_use]
    pub fn of(gateways: &Gateways, path: &str) -> Option<Self> {
        if path == SLACK_PATH {
            return gateways.slack.is_some().then_some(Self::Slack);
        }
        let mail = gateways.mail.as_ref()?;
        // What follows the provider's name is the provider's own: Postmark may carry its secret there.
        let rest = path.strip_prefix(MAIL_PATH)?.strip_prefix(mail.provider.name())?;
        (rest.is_empty() || rest.starts_with('/')).then_some(Self::Mail)
    }

    /// The most of a request's body the endpoint reads.
    #[must_use]
    pub fn most(self, gateways: &Gateways) -> usize {
        match (self, &gateways.mail) {
            (Self::Mail, Some(mail)) => mail.most(),
            _ => SLACK_BYTES,
        }
    }
}

/// What an endpoint is handed of a request.
pub struct Received {
    /// The method.
    pub method: String,
    /// The path of the request's URL, without the query, as it came.
    pub path: String,
    /// The headers, name and value, each byte of a value one character, as the Fetch standard reads one.
    pub headers: Vec<(String, String)>,
    /// The body as the Fetch standard's `text()` gives it: UTF-8, read leniently, without a byte order
    /// mark. Empty until it is read, and for a request whose body never is.
    pub body: String,
}

impl Received {
    /// The headers as the rules read them.
    #[must_use]
    pub fn headers(&self) -> Vec<(&str, &str)> {
        self.headers
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()))
            .collect()
    }

    /// What follows the mail provider's name in the path: empty, or a slash and the provider's own.
    #[must_use]
    pub fn after_provider(&self, name: &str) -> &str {
        self.path
            .strip_prefix(MAIL_PATH)
            .and_then(|rest| rest.strip_prefix(name))
            .unwrap_or("")
    }
}

/// What a request is answered before any of its body is read, when what came before the body already
/// decides it: a request that is not a `POST`, and a webhook whose provider carries its secret outside the
/// body and that does not carry it. `None` is a request whose body is to be read.
#[must_use]
pub fn before_body(gateways: &Gateways, route: Route, received: &Received) -> Option<Answer> {
    if received.method != "POST" {
        return Some(notes::answer_method_not_allowed());
    }
    match (route, &gateways.mail) {
        (Route::Mail, Some(mail)) => mail_in::refused_before_body(gateways, mail, received, note::now()),
        _ => None,
    }
}

/// Answers one request to a gateway's endpoint, its body read.
pub async fn answer(gateways: &Gateways, route: Route, received: &Received) -> Answer {
    // The clock is read once for a request, as the engine reads it once.
    let now = note::now();
    match (route, &gateways.slack, &gateways.mail) {
        (Route::Slack, Some(slack), _) => slack_in::answer(gateways, slack, received, now).await,
        (Route::Mail, _, Some(mail)) => mail_in::answer(gateways, mail, received, now).await,
        // A route is only ever made for a gateway that is configured.
        _ => failure(404, "no such endpoint"),
    }
}

/// The answer to a request whose body is longer than the endpoint reads.
#[must_use]
pub fn answer_too_long() -> Answer {
    failure(413, "the request's body is longer than this endpoint reads")
}

/// The answer to a request whose body did not arrive whole.
#[must_use]
pub fn answer_broken() -> Answer {
    failure(400, "the request's body could not be read")
}

/// The answer to a request whose body did not arrive in time.
#[must_use]
pub fn answer_late() -> Answer {
    failure(408, "the request's body did not arrive in time")
}

/// The answer to a request that came while the gateways were answering as many as they answer at once.
/// Slack and a mail provider deliver again.
#[must_use]
pub fn answer_busy() -> Answer {
    failure(503, "the gateways are busy; deliver the request again")
}

/// The answer to a request the server failed on for a reason of its own, which it has noted.
#[must_use]
pub fn answer_failed() -> Answer {
    failure(500, "the gateway failed")
}

/// `{"error": …}`, the shape of every failure the Edge Functions answered.
fn failure(status: u16, error: &str) -> Answer {
    Answer {
        status,
        body: json!({"error": error}).to_string(),
    }
}

/// The request as a mail provider's rule reads it. A provider that takes its secret from the path looks for
/// it after a segment named `webhook`, which is where a connector's URL had it; here the provider's own
/// name stands there, so the rule is handed what follows the name under the name it knows.
#[must_use]
pub fn as_connector_path(after_provider: &str) -> String {
    format!("/webhook{after_provider}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gateway::config::{self, MailProvider};
    use crate::gateway::testing::{WEBHOOK_SECRET, engine_on, gateways, mail, scratch, slack, team};

    #[tokio::test]
    async fn a_path_is_an_endpoint_only_of_a_gateway_the_operator_configured() {
        let data = scratch("endpoint-routes");
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        let both = gateways(
            &data.join("both"),
            &engine,
            config::Gateways {
                slack: Some(slack("http://127.0.0.1:1/api", &ops.join_secret)),
                mail: Some(mail(
                    MailProvider::Postmark,
                    "http://127.0.0.1:1",
                    &ops.join_secret,
                )),
            },
        );
        assert_eq!(Route::of(&both, "/gateway/slack"), Some(Route::Slack));
        assert_eq!(Route::of(&both, "/gateway/mail/postmark"), Some(Route::Mail));
        assert_eq!(
            Route::of(&both, "/gateway/mail/postmark/a-secret"),
            Some(Route::Mail)
        );
        for not_one in [
            "/gateway/",
            "/gateway/slack/",
            "/gateway/slack/events",
            "/gateway/mail",
            "/gateway/mail/",
            "/gateway/mail/resend",
            "/gateway/mail/postmarks",
            "/v1/gateway/slack",
        ] {
            assert_eq!(Route::of(&both, not_one), None, "{not_one}");
        }
        // Postmark's body is the whole mail, and is read only once the webhook is known to be Postmark's.
        assert_eq!(
            (Route::Slack.most(&both), Route::Mail.most(&both)),
            (1 << 20, 10 << 20)
        );

        let mail_only = gateways(
            &data.join("mail"),
            &engine,
            config::Gateways {
                slack: None,
                mail: Some(mail(MailProvider::Resend, "http://127.0.0.1:1", &ops.join_secret)),
            },
        );
        assert_eq!(Route::of(&mail_only, "/gateway/slack"), None);
        assert_eq!(Route::of(&mail_only, "/gateway/mail/resend"), Some(Route::Mail));
        assert_eq!(Route::of(&mail_only, "/gateway/mail/postmark"), None);
        // Resend's body is its signature's, an id and an envelope: a megabyte is all that is read of it.
        assert_eq!(Route::Mail.most(&mail_only), 1 << 20);

        // What follows the provider's name reaches the provider's rule under the name the rule knows.
        let received = Received {
            method: "POST".to_owned(),
            path: "/gateway/mail/postmark/a%20secret".to_owned(),
            headers: Vec::new(),
            body: String::new(),
        };
        assert_eq!(
            as_connector_path(received.after_provider("postmark")),
            "/webhook/a%20secret"
        );
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn what_comes_before_a_body_decides_a_request_that_it_can_and_no_other() {
        let data = scratch("endpoint-before");
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        let postmark = gateways(
            &data.join("postmark"),
            &engine,
            config::Gateways {
                slack: Some(slack("http://127.0.0.1:1/api", &ops.join_secret)),
                mail: Some(mail(
                    MailProvider::Postmark,
                    "http://127.0.0.1:1",
                    &ops.join_secret,
                )),
            },
        );
        let head = |method: &str, path: &str, authorization: Option<&str>| Received {
            method: method.to_owned(),
            path: path.to_owned(),
            headers: authorization
                .map(|value| ("authorization".to_owned(), value.to_owned()))
                .into_iter()
                .collect(),
            body: String::new(),
        };
        let decided = |gateways: &Gateways, route: Route, received: &Received| {
            before_body(gateways, route, received).map(|answer| answer.status)
        };

        // Postmark's secret is in a header or in the path: a webhook without it is refused unread.
        let path = "/gateway/mail/postmark";
        let (right, wrong) = (
            format!("Bearer {WEBHOOK_SECRET}"),
            "Bearer not-the-secret".to_owned(),
        );
        assert_eq!(
            decided(&postmark, Route::Mail, &head("POST", path, None)),
            Some(403)
        );
        assert_eq!(
            decided(&postmark, Route::Mail, &head("POST", path, Some(&wrong))),
            Some(403)
        );
        assert_eq!(
            decided(
                &postmark,
                Route::Mail,
                &head("POST", "/gateway/mail/postmark/not-the-secret", None)
            ),
            Some(403)
        );
        // One that carries it is read: only its body says whether it is a mail.
        assert_eq!(
            decided(&postmark, Route::Mail, &head("POST", path, Some(&right))),
            None
        );
        let with_secret = format!("{path}/{WEBHOOK_SECRET}");
        assert_eq!(
            decided(&postmark, Route::Mail, &head("POST", &with_secret, None)),
            None
        );
        // A request that is not a POST is answered unread, whoever sent it and wherever.
        assert_eq!(
            decided(&postmark, Route::Mail, &head("GET", path, Some(&right))),
            Some(405)
        );
        assert_eq!(
            decided(&postmark, Route::Slack, &head("PUT", "/gateway/slack", None)),
            Some(405)
        );
        // Slack signs the body, and so does Resend: nothing before the body says whose it is.
        assert_eq!(
            decided(&postmark, Route::Slack, &head("POST", "/gateway/slack", None)),
            None
        );
        let resend = gateways(
            &data.join("resend"),
            &engine,
            config::Gateways {
                slack: None,
                mail: Some(mail(MailProvider::Resend, "http://127.0.0.1:1", &ops.join_secret)),
            },
        );
        assert_eq!(
            decided(&resend, Route::Mail, &head("POST", "/gateway/mail/resend", None)),
            None
        );
        let _ = std::fs::remove_dir_all(&data);
    }
}
