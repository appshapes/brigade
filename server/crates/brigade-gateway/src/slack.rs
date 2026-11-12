//! The Slack gateway core (`supabase/functions/_shared/slack.ts`): what a Slack event and a slash command
//! carry, where a person's message goes and the Brigade body it becomes, where a session's message goes and
//! the Slack text it becomes, and the roster a person is shown.
//!
//! The design is the mail gateway's ([`crate::resolve`]) with Slack's own facts: Slack authenticates the
//! author (a user id is a verified identity, a name is not), a reply lives in a thread whose root timestamp
//! is the key, and there is no quoting. The thread table is behind [`SlackThreads`] and
//! [`ThreadLookup`], so every rule here runs without a store.
//!
//! Two things the TypeScript did are not followed, and neither can be reached by what Slack sends. It put
//! the bot's user id into a pattern as it stood, so an id with a character a pattern gives a meaning to
//! would have matched as one; here the id is matched as it is written, and an id is letters and digits.
//! And on a signed body that is JSON `null` it failed with an error of the runtime's, where
//! [`parse_events_request`] ignores the body as it ignores every other that is no event.

use std::sync::LazyLock;

use brigade_core::Timestamp;
use regex::Regex;
use ring::hmac;

use crate::block::{escape_block_lines, is_bare_command, parse_leading, render_block};
use crate::connector::{Request, constant_time_equal};
use crate::json::{Json, text_of};
use crate::mail::is_address;
use crate::resolve::{
    BrigadeBody, Direction, Envelope, MAX_BODY_BYTES, MAX_SUMMARY_CHARS, SessionRecord, TeamSessions,
    ThreadKind, ThreadLookup, ThreadRow, decision_view, is_gateway_session,
};
use crate::text::{cut_chars, excerpt, one_line, truncate_utf8};
use crate::{js, time};

/// The `harness_version` the Slack gateway registers its session with.
pub const SLACK_GATEWAY_VERSION: &str = "0.1.0";
/// The name of the Slack gateway's session.
pub const SLACK_SESSION_NAME: &str = "slack-gateway";
/// The `harness` of the Slack gateway's session. Members find the gateway on the roster by it.
pub const SLACK_HARNESS: &str = "gateway-slack";
/// How far a request's timestamp may be from now, either way, in seconds.
pub const SIGNATURE_TOLERANCE_SECONDS: i64 = 300;
/// What ends a person's text that was cut to fit a Brigade message.
pub const SLACK_CUT_MARKER: &str =
    "\n[… cut by the Slack gateway: the message was longer than a Brigade message may be]";

/// `/^<@([UW][A-Z0-9]+)(?:\|[^>]*)?>$/`: Slack's own mention of a user, alone.
static USER_MARKUP: LazyLock<Regex> = LazyLock::new(|| js::regex(r"^<@([UW][A-Z0-9]+)(?:\|[^>]*)?>$"));

/// `/^<#([CG][A-Z0-9]+)(?:\|[^>]*)?>$/`: Slack's own mention of a channel, alone.
static CHANNEL_MARKUP: LazyLock<Regex> = LazyLock::new(|| js::regex(r"^<#([CG][A-Z0-9]+)(?:\|[^>]*)?>$"));

/// `/^[UW][A-Z0-9]{6,}$/`: a Slack user id, U, or W in an Enterprise Grid.
static USER_ID: LazyLock<Regex> = LazyLock::new(|| js::regex(r"^[UW][A-Z0-9]{6,}$"));

/// `/^[CGD][A-Z0-9]{6,}$/`: a Slack channel id.
static CHANNEL_ID: LazyLock<Regex> = LazyLock::new(|| js::regex(r"^[CGD][A-Z0-9]{6,}$"));

/// `/^@([^\s@#]+)$/`.
static AT_NAME: LazyLock<Regex> = LazyLock::new(|| js::regex(&format!(r"^@([^{s}@#]+)$", s = js::SPACE)));

/// `/^#([^\s@#]+)$/`.
static HASH_NAME: LazyLock<Regex> = LazyLock::new(|| js::regex(&format!(r"^#([^{s}@#]+)$", s = js::SPACE)));

/// `/&lt;((?:@[UW]|#[CG])[A-Z0-9]+(?:\|[^&<>|]*)?)&gt;/g`.
static ESCAPED_MARKUP: LazyLock<Regex> =
    LazyLock::new(|| js::regex(r"&lt;((?:@[UW]|#[CG])[A-Z0-9]+(?:\|[^&<>|]*)?)&gt;"));

/// The Slack gateway session's `↳` line. At most 256 code points (`docs/protocol-v1.md` 4.4.2).
#[must_use]
pub fn slack_description(bot_handle: &str, workspace: &str) -> String {
    format!(
        "slack gateway for {workspace}: a person DMs @{bot_handle} or mentions it with a first line \"to: <session or member>\"; a session writes to me with \"to: @name\", \"to: #channel\" or \"to: <email>\""
    )
}

// ---- Signature --------------------------------------------------------------------------------------------

/// Whether a request carries Slack's signature: `X-Slack-Signature` is `v0=` and the hexadecimal
/// HMAC-SHA256 of `v0:<timestamp>:<body>` under the app's signing secret, and `X-Slack-Request-Timestamp` is
/// within `tolerance_seconds` of `now`. An empty secret verifies nothing.
///
/// The timestamp is read as JavaScript's `Number` reads a string, and signed as the header wrote it.
#[must_use]
pub fn verify_slack_signature(
    secret: &str,
    request: &Request<'_>,
    body: &str,
    now: Timestamp,
    tolerance_seconds: i64,
) -> bool {
    let header = |name: &str| request.header(name).filter(|value| !value.is_empty());
    let (Some(timestamp), Some(signature)) =
        (header("x-slack-request-timestamp"), header("x-slack-signature"))
    else {
        return false;
    };
    let at = js::number(&timestamp);
    // Both are whole seconds far below 2^53, where a double holds every integer.
    let (now, tolerance) = (time::now_seconds(now) as f64, tolerance_seconds as f64);
    if !at.is_finite() || (now - at).abs() > tolerance {
        return false;
    }
    sign_slack(secret, &timestamp, body).is_some_and(|expected| constant_time_equal(&signature, &expected))
}

/// The header a Slack request would carry, for tests. `None` for an empty secret, where the TypeScript
/// throws: WebCrypto imports no key of no bytes.
#[must_use]
pub fn sign_slack(secret: &str, timestamp: &str, body: &str) -> Option<String> {
    if secret.is_empty() {
        return None;
    }
    let key = hmac::Key::new(hmac::HMAC_SHA256, secret.as_bytes());
    let tag = hmac::sign(&key, format!("v0:{timestamp}:{body}").as_bytes());
    let hex: String = tag.as_ref().iter().map(|byte| format!("{byte:02x}")).collect();
    Some(format!("v0={hex}"))
}

// ---- Events -----------------------------------------------------------------------------------------------

/// How a person's words reached the gateway.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MessageSource {
    /// A `message` event: a direct message, or a reply in a channel thread.
    Message,
    /// An `app_mention` event: a channel message that mentions the bot.
    AppMention,
    /// A slash command.
    Command,
}

impl MessageSource {
    /// The source's name, as the TypeScript spelled it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Message => "message",
            Self::AppMention => "app_mention",
            Self::Command => "command",
        }
    }
}

/// One thing a person did that the gateway may carry: a direct message to the bot, a mention of it in a
/// channel, a reply in a channel thread, or a slash command.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InboundSlack {
    /// Slack's event id, the key a second delivery is recognised by; `cmd:<trigger_id>` for a slash
    /// command.
    pub event_id: String,
    /// How the words reached the gateway.
    pub source: MessageSource,
    /// The person's Slack user id.
    pub user: String,
    /// The channel's id.
    pub channel: String,
    /// `im`, `channel`, `group` (a private channel) or `mpim`; `None` when Slack did not say.
    pub channel_type: Option<String>,
    /// The message's own timestamp; `None` for a slash command.
    pub ts: Option<String>,
    /// The timestamp of the thread's root, when the message is a reply inside a thread. An event whose
    /// `thread_ts` is the empty string has `Some("")` here: it names no thread to look up, and it is still
    /// what an answer in a direct message is threaded under and what the thread row records.
    pub thread_ts: Option<String>,
    /// What the person wrote, without the bot's mention and with Slack's three entities read.
    pub text: String,
    /// A channel message that did not mention the bot: a reply in a thread, carried only when the thread
    /// is the gateway's ([`resolve_slack_inbound`] decides). Slack delivers these once the app has the
    /// channel-history scopes.
    pub implicit: bool,
}

impl InboundSlack {
    /// Whether the message is in a direct message with the bot.
    #[must_use]
    pub fn is_direct(&self) -> bool {
        self.channel_type.as_deref() == Some("im")
    }

    /// The message's own timestamp, when it has one that is not empty: what a permalink and a reaction
    /// are asked for.
    #[must_use]
    pub fn posted_ts(&self) -> Option<&str> {
        self.ts.as_deref().filter(|ts| !ts.is_empty())
    }
}

/// What a request to the gateway's Slack endpoint is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ParsedRequest {
    /// Slack's check of the request URL: the challenge is answered as it came.
    UrlVerification {
        /// The nonce to answer.
        challenge: String,
    },
    /// An event the gateway may carry.
    Event {
        /// What the person did.
        message: InboundSlack,
    },
    /// A slash command.
    Command {
        /// What the person did.
        message: InboundSlack,
        /// The command's name, without its slash. Nothing checks it.
        command: String,
        /// Where Slack takes a later answer. Nothing uses it.
        response_url: Option<String>,
    },
    /// A request the gateway does not carry.
    Ignore {
        /// Why, as the request is answered.
        reason: String,
    },
}

fn ignore(reason: impl Into<String>) -> ParsedRequest {
    ParsedRequest::Ignore {
        reason: reason.into(),
    }
}

/// `String(value)` of a member that may be missing: `undefined` for one that is.
fn string_of(value: Option<&Json>) -> String {
    value.map_or_else(|| "undefined".to_owned(), Json::text)
}

/// Reads an Events API body. `bot_user_id` is the gateway's own user, whose messages are ignored; `None`,
/// or an empty id, when it is not known.
///
/// Carried: a direct message, a mention, and a reply in a channel thread that does not mention the bot
/// (marked [`InboundSlack::implicit`]). Edits, deletions and Slack's other subtypes are not a person's new
/// words; a reply the person also sent to the channel (`thread_broadcast`) is.
#[must_use]
pub fn parse_events_request(raw: &str, bot_user_id: Option<&str>) -> ParsedRequest {
    let bot_user_id = bot_user_id.filter(|id| !id.is_empty());
    let Some(doc) = Json::parse(raw) else {
        return ignore("not json");
    };
    let kind = doc.member("type");
    let is = |value: Option<&Json>, text: &str| value.and_then(Json::as_str) == Some(text);
    if is(kind, "url_verification")
        && let Some(challenge) = doc.member("challenge").and_then(Json::as_str)
    {
        return ParsedRequest::UrlVerification {
            challenge: challenge.to_owned(),
        };
    }
    // `typeof event === "object"`: an object or an array, and an array has no member the rules read.
    let event = doc
        .member("event")
        .filter(|event| matches!(event, Json::Object(_) | Json::List(_)));
    let Some(event) = event.filter(|_| is(kind, "event_callback")) else {
        return ignore(format!("type {}", string_of(kind)));
    };
    let event_id = doc.member("event_id").and_then(Json::as_str).unwrap_or("");
    if event_id.is_empty() {
        return ignore("no event id");
    }
    let source = match event.member("type").and_then(Json::as_str) {
        Some("message") => MessageSource::Message,
        Some("app_mention") => MessageSource::AppMention,
        _ => return ignore(format!("event {}", string_of(event.member("type")))),
    };
    let truthy = |name: &str| event.member(name).is_some_and(Json::truthy);
    let other_subtype = truthy("subtype") && !is(event.member("subtype"), "thread_broadcast");
    let Some(user) = event
        .member("user")
        .and_then(Json::as_str)
        .filter(|_| !other_subtype && !truthy("bot_id"))
    else {
        return ignore("not a person's message");
    };
    if bot_user_id == Some(user) {
        return ignore("our own message");
    }
    let text = event.member("text").and_then(Json::as_str).unwrap_or("");
    let ts = event.member("ts").and_then(Json::as_str);
    let thread_ts = event
        .member("thread_ts")
        .and_then(Json::as_str)
        .filter(|thread_ts| Some(*thread_ts) != ts);
    let channel_type = event.member("channel_type").and_then(Json::as_str);
    let mut implicit = false;
    if source == MessageSource::Message && channel_type != Some("im") {
        // A channel message arrives once the app has the channel-history scopes. One that mentions the bot
        // is delivered as `app_mention` too, and that copy is the one carried; a top-level message without
        // a mention is not ours; a reply in a thread may be, when the thread is the gateway's.
        let Some(bot) = bot_user_id else {
            return ignore("a channel message before the bot's identity is known");
        };
        if mentions_user(text, bot) {
            return ignore("carried by the mention event");
        }
        if thread_ts.is_none_or(str::is_empty) {
            return ignore("a channel message without a mention");
        }
        implicit = true;
    }
    ParsedRequest::Event {
        message: InboundSlack {
            event_id: event_id.to_owned(),
            source,
            user: user.to_owned(),
            channel: text_of(event.get("channel")),
            channel_type: channel_type.map(str::to_owned),
            ts: ts.map(str::to_owned),
            thread_ts: thread_ts.map(str::to_owned),
            text: strip_mention(text, bot_user_id),
            implicit,
        },
    }
}

/// Reads a slash command's form body. A command Slack gave no `trigger_id` gets the one `new_trigger`
/// makes, which the TypeScript drew at random: this crate draws nothing, so the caller hands the source in,
/// and it is asked only then.
#[must_use]
pub fn parse_command_request(raw: &str, new_trigger: impl FnOnce() -> String) -> ParsedRequest {
    let form = js::form_pairs(raw);
    let field = |name: &str| {
        form.iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    };
    let (user, channel) = (field("user_id").unwrap_or(""), field("channel_id").unwrap_or(""));
    if user.is_empty() || channel.is_empty() {
        return ignore("not a slash command");
    }
    let trigger = field("trigger_id").map_or_else(new_trigger, str::to_owned);
    let command = field("command").unwrap_or("");
    ParsedRequest::Command {
        command: command.strip_prefix('/').unwrap_or(command).to_owned(),
        response_url: field("response_url").map(str::to_owned),
        message: InboundSlack {
            event_id: format!("cmd:{trigger}"),
            source: MessageSource::Command,
            user: user.to_owned(),
            channel: channel.to_owned(),
            channel_type: (field("channel_name") == Some("directmessage")).then(|| "im".to_owned()),
            ts: None,
            thread_ts: None,
            text: js::trim(field("text").unwrap_or("")).to_owned(),
            implicit: false,
        },
    }
}

/// Where Slack's mention of a user is in a text, `<@U…>` or `<@U…|name>`, each as the bytes it spans.
fn mentions_of(text: &str, user_id: &str) -> Vec<(usize, usize)> {
    let open = format!("<@{user_id}");
    let mut found = Vec::new();
    let mut from = 0;
    while let Some(at) = text[from..].find(&open) {
        let start = from + at;
        let after = start + open.len();
        let rest = &text[after..];
        // `(?:\|[^>]*)?>`: the closing bracket at once, or a bar and everything up to the next one.
        let close = if rest.starts_with('>') {
            Some(after)
        } else if rest.starts_with('|') {
            rest.find('>').map(|at| after + at)
        } else {
            None
        };
        match close {
            Some(close) => {
                found.push((start, close + 1));
                from = close + 1;
            }
            None => from = start + 1,
        }
    }
    found
}

/// Whether a text carries Slack's mention of a user, `<@U…>` or `<@U…|name>`.
#[must_use]
pub fn mentions_user(text: &str, user_id: &str) -> bool {
    !mentions_of(text, user_id).is_empty()
}

/// Removes the bot's own mention from a text and reads Slack's three entities.
#[must_use]
pub fn strip_mention(text: &str, bot_user_id: Option<&str>) -> String {
    let mut stripped = String::with_capacity(text.len());
    let mut at = 0;
    if let Some(bot) = bot_user_id.filter(|id| !id.is_empty()) {
        for (start, end) in mentions_of(text, bot) {
            stripped.push_str(&text[at..start]);
            at = end;
        }
    }
    stripped.push_str(&text[at..]);
    // In this order, as the TypeScript's three replacements ran: `&amp;lt;` is `&lt;`, not `<`.
    let read = stripped
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&amp;", "&");
    js::trim(&read).to_owned()
}

/// Makes untrusted text safe to post: Slack's three control characters.
#[must_use]
pub fn escape_mrkdwn(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

// ---- Targets ----------------------------------------------------------------------------------------------

/// Whom a session's message is for, as its `to:` line names them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SlackTarget {
    /// A person, by their Slack user id.
    User {
        /// The user id.
        id: String,
    },
    /// A person, by a name the user list is searched for.
    UserName {
        /// The name, without its at sign.
        name: String,
    },
    /// A person, by their email address.
    UserEmail {
        /// The address, lowercase.
        email: String,
    },
    /// A channel, by its id.
    Channel {
        /// The channel id.
        id: String,
    },
    /// A channel, by a name the channel list is searched for.
    ChannelName {
        /// The name, without its hash sign.
        name: String,
    },
}

/// Reads the `to:` value of a session's message: `@name`, `#channel`, an email address, a Slack user or
/// channel id, or the `<@U…|name>` and `<#C…|name>` forms a copied Slack message carries.
#[must_use]
pub fn parse_slack_target(raw: &str) -> Option<SlackTarget> {
    let value = js::trim(raw);
    let first = |pattern: &Regex| pattern.captures(value).map(|found| found[1].to_owned());
    if let Some(id) = first(&USER_MARKUP) {
        return Some(SlackTarget::User { id });
    }
    if let Some(id) = first(&CHANNEL_MARKUP) {
        return Some(SlackTarget::Channel { id });
    }
    if USER_ID.is_match(value) {
        return Some(SlackTarget::User { id: value.to_owned() });
    }
    if CHANNEL_ID.is_match(value) {
        return Some(SlackTarget::Channel { id: value.to_owned() });
    }
    if let Some(name) = first(&AT_NAME) {
        return Some(SlackTarget::UserName { name });
    }
    if let Some(name) = first(&HASH_NAME) {
        return Some(SlackTarget::ChannelName { name });
    }
    is_address(value).then(|| SlackTarget::UserEmail {
        email: value.to_lowercase(),
    })
}

// ---- Inbound ----------------------------------------------------------------------------------------------

/// The thread table, as the inbound rules of Slack read it. The caller keeps it.
pub trait SlackThreads {
    /// The newest Slack row of a thread: the channel and the timestamp of its root message.
    fn by_thread(&self, channel: &str, thread_ts: &str) -> Option<ThreadRow>;
}

/// Where the gateway found the session a person's message is for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TargetSource {
    /// The `send` of a slash command.
    Command,
    /// A `to:` the person typed, as a header line or in a block.
    Block,
    /// The thread the person replied in.
    Thread,
}

impl TargetSource {
    /// The source's name, as the TypeScript spelled it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Command => "command",
            Self::Block => "block",
            Self::Thread => "thread",
        }
    }
}

/// What a person's Slack message is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SlackInbound {
    /// A command for the gateway itself.
    Command {
        /// The command, in lowercase; `help` for a slash command with no text.
        command: String,
        /// What follows the command's word in a slash command, its white space made single spaces.
        args: String,
        /// The text the command was read from.
        text: String,
    },
    /// A message for a session.
    Message {
        /// What names the session: for [`crate::resolve_recipient_across`] when the person typed it, a
        /// session id when a thread gave it.
        target: String,
        /// The Brigade message this one answers. A `reply-to:` the person typed with nothing after it is
        /// the empty string, and the thread's message does not replace it.
        reply_to: Option<String>,
        /// The summary the person gave.
        summary: Option<String>,
        /// What the person wrote, below its directives.
        text: String,
        /// Where the target was found.
        source: TargetSource,
    },
    /// A message that names no session and answers none.
    Unaddressed {
        /// What the person wrote.
        text: String,
    },
    /// A message whose directives could not be read.
    Invalid {
        /// Why, as the person is told.
        reason: String,
    },
    /// A message that is not the gateway's, and is not answered.
    Ignore {
        /// Why, as the request is answered.
        reason: String,
    },
}

/// Decides what a person's message is and where it goes: their explicit `to:` line or block first, then
/// the thread they replied in. A slash command's text is `sessions`, `help`, or
/// `send <session or member> <text>`; what names a session is [`crate::resolve_recipient`]'s business. A
/// channel reply that did not mention the bot is carried only inside a thread the gateway is part of (a
/// session's post, or a person's earlier message to one) and is otherwise ignored, not answered: the bot
/// would else speak in every thread of every channel it has joined.
#[must_use]
pub fn resolve_slack_inbound(message: &InboundSlack, threads: &dyn SlackThreads) -> SlackInbound {
    if message.source == MessageSource::Command {
        // Split at every run of JavaScript's white space, so a text that ends with one has an empty last
        // word: `send 3f9a2 ` is a message with no text.
        let words = js::split_space(&message.text);
        let (verb, rest) = (words[0].to_lowercase(), &words[1..]);
        if verb == "send" && rest.len() >= 2 {
            return SlackInbound::Message {
                target: rest[0].to_owned(),
                reply_to: None,
                summary: None,
                text: rest[1..].join(" "),
                source: TargetSource::Command,
            };
        }
        return SlackInbound::Command {
            command: if verb.is_empty() { "help".to_owned() } else { verb },
            args: rest.join(" "),
            text: message.text.clone(),
        };
    }
    // The newest row of the thread: a session's post (answer it) or the person's own earlier message (a
    // second message to the same session; it cannot be a reply_to, since the gateway sent that one).
    let thread = message
        .thread_ts
        .as_deref()
        .filter(|thread_ts| !thread_ts.is_empty())
        .and_then(|thread_ts| threads.by_thread(&message.channel, thread_ts));
    if message.implicit && thread.is_none() {
        return SlackInbound::Ignore {
            reason: "a channel reply outside a Brigade thread".to_owned(),
        };
    }
    let leading = match parse_leading(&message.text) {
        Ok(leading) => leading,
        Err(error) => {
            return SlackInbound::Invalid {
                reason: error.to_string(),
            };
        }
    };
    let fields = leading.fields.clone().unwrap_or_default();
    let filled = |key: &str| {
        fields
            .get(key)
            .map(String::as_str)
            .filter(|value| !value.is_empty())
    };
    let text = js::trim(&leading.rest).to_owned();
    if let Some(command) = filled("command") {
        return SlackInbound::Command {
            command: js::trim(command).to_lowercase(),
            args: String::new(),
            text,
        };
    }
    if leading.fields.is_none()
        && let Some(command) = is_bare_command(&text)
    {
        return SlackInbound::Command {
            command: command.to_owned(),
            args: String::new(),
            text,
        };
    }
    // An empty `to:` names nobody, so the message is resolved as if it had none.
    let mut target = filled("to").map(str::to_owned);
    let mut reply_to = fields.get("reply-to").cloned();
    let mut source = TargetSource::Block;
    if target.is_none()
        && let Some(thread) = thread
    {
        if thread.direction == Direction::Out {
            reply_to = reply_to.or(Some(thread.brigade_message_id));
        }
        target = Some(thread.session_id).filter(|session_id| !session_id.is_empty());
        source = TargetSource::Thread;
    }
    match target {
        Some(target) => SlackInbound::Message {
            target,
            reply_to,
            summary: fields.get("summary").cloned(),
            text,
            source,
        },
        None => SlackInbound::Unaddressed { text },
    }
}

/// What the gateway learned from Slack about a person's message, for the body it becomes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SlackContext {
    /// The person's name, as their profile gives it; unverified text.
    pub user_name: Option<String>,
    /// The channel's name.
    pub channel_name: Option<String>,
    /// A link to the message.
    pub permalink: Option<String>,
}

/// Renders a person's Slack message as the body of a Brigade message: a header line, the text (escaped so
/// it cannot forge a block), and the envelope block. The text is cut so that the whole fits the protocol's
/// cap on a body, which is counted in bytes of UTF-8; the summary is the text's first line, cut to the cap
/// on a summary, in code points.
///
/// The TypeScript meant a message with no text to have the summary `slack message`; its excerpt answers
/// `message` first, so that is the summary, here too.
#[must_use]
pub fn slack_body(message: &InboundSlack, text: &str, context: &SlackContext) -> BrigadeBody {
    let filled = |value: &Option<String>| value.clone().filter(|value| !value.is_empty());
    let (user_name, channel_name) = (filled(&context.user_name), filled(&context.channel_name));
    let who = match &user_name {
        Some(name) => format!("@{name} ({}, unverified name)", message.user),
        None => message.user.clone(),
    };
    let place = match &channel_name {
        _ if message.is_direct() => "in a direct message".to_owned(),
        Some(name) => format!("in #{name}"),
        None => format!("in {}", message.channel),
    };
    let head = format!("Slack message from {who} {place}:\n\n");
    let block = format!(
        "\n\n{}",
        render_block(&[
            ("via", Some("slack")),
            ("from", Some(&message.user)),
            ("from-name", user_name.as_deref()),
            ("channel", Some(&message.channel)),
            ("channel-name", channel_name.as_deref()),
            ("ts", message.ts.as_deref()),
            ("thread", message.thread_ts.as_deref()),
            ("permalink", context.permalink.as_deref()),
        ])
    );
    let budget = MAX_BODY_BYTES.saturating_sub(head.len() + block.len());
    let carried = truncate_utf8(&escape_block_lines(text), budget, SLACK_CUT_MARKER);
    let summary = match excerpt(text, MAX_SUMMARY_CHARS) {
        line if line.is_empty() => "slack message".to_owned(),
        line => line,
    };
    BrigadeBody {
        body: format!("{head}{carried}{block}"),
        summary: cut_chars(&summary, MAX_SUMMARY_CHARS),
    }
}

// ---- Outbound ---------------------------------------------------------------------------------------------

/// Where a session's message to the gateway goes, and what it says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SlackOutbound {
    /// Whom it is for. A reply goes to the channel its thread is in.
    pub target: SlackTarget,
    /// The timestamp of the root of the thread a reply goes into; `None` for a new post.
    pub thread_ts: Option<String>,
    /// The person a reply mentions: the one whose message it answers.
    pub mention: Option<String>,
    /// The text: the body below its directives.
    pub text: String,
}

/// Decides where a session's message goes: its `to:` directive, or the thread of the Slack message it
/// replies to. A `to:` wins over a reply, and the post it starts is a new one.
///
/// # Errors
/// The reason the sending session is told: directives that could not be read, a `to:` that names nothing
/// on Slack, or no address at all.
pub fn resolve_slack_outbound(env: &Envelope, threads: &dyn ThreadLookup) -> Result<SlackOutbound, String> {
    let leading = parse_leading(&env.body).map_err(|error| error.to_string())?;
    let fields = leading.fields.unwrap_or_default();
    let text = leading.rest;
    if let Some(to) = fields.get("to").filter(|to| !to.is_empty()) {
        let Some(target) = parse_slack_target(to) else {
            return Err(format!(
                "\"to: {to}\" is not @name, #channel, an email address or a Slack id"
            ));
        };
        return Ok(SlackOutbound {
            target,
            thread_ts: None,
            mention: None,
            text,
        });
    }
    if let Some(reply_to) = env.reply_to.as_deref().filter(|id| !id.is_empty())
        && let Some(row) = threads.by_brigade_id(reply_to)
        && row.kind == Some(ThreadKind::Slack)
        && row.direction == Direction::In
    {
        return Ok(SlackOutbound {
            target: SlackTarget::Channel { id: row.address },
            thread_ts: row.mail_message_id,
            mention: row.actor,
            text,
        });
    }
    Err(
        "no address: start the body with a line \"to: @name\", \"to: #channel\" or \"to: <email>\", or reply (--reply-to) to a message that came from Slack"
            .to_owned(),
    )
}

/// What a Slack message is rendered with.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SlackRenderContext {
    /// The team's name.
    pub team_name: String,
    /// The bot's handle, without its at sign.
    pub bot_handle: String,
    /// The installed app's token carries the channel-history scopes, so a reply in a channel thread reaches
    /// the gateway without a mention and the footer asks for none. Until the administrator reinstalls the
    /// app with those scopes it does not, and the footer says to mention the bot.
    pub hears_channel_threads: bool,
    /// The Slack user behind each @handle in the text, as the caller resolved them; each becomes a
    /// mention.
    pub mentions: Mentions,
}

/// Writes the Slack message for a Brigade envelope: who wrote it, the text (escaped), and how to reply,
/// and nothing else. The gateway needs no breadcrumb in the text: a reply is routed by its thread, through
/// the thread table ([`resolve_slack_inbound`]), and Slack never quotes. A mention in front notifies the
/// person in a channel thread. The text is still escaped against block lines, so a session cannot show a
/// person a block that looks like the gateway's own; Slack's own mention markup for a user or a channel,
/// and the @handles the gateway resolved, are the one thing let through, so a session can tag a person.
///
/// The sender line is the session and its label, nothing else (owner rulings 2026-11-09, card 193). No
/// "unverified" marker: output a person reads shows the label alone, and protocol v1 B-3 keeps the marker
/// for what a model is handed, the frame. No team: it told a reader in the team's own workspace little.
/// The label is still self-reported text.
#[must_use]
pub fn render_slack_text(env: &Envelope, out: &SlackOutbound, context: &SlackRenderContext) -> String {
    let escape_text = |text: &str| {
        link_mentions(
            &pass_mention_markup(&escape_mrkdwn(&escape_block_lines(text))),
            &context.mentions,
        )
    };
    let text = js::trim(&out.text);
    let body = match decision_view(text, &context.team_name, escape_text) {
        Some(decision) => decision,
        None if text.starts_with(&format!(
            "Brigade digest: team {}, repository ",
            context.team_name
        )) =>
        {
            format!(
                "*Brigade digest*\n{}",
                escape_text(&text["Brigade digest: ".len()..])
            )
        }
        None => escape_text(text),
    };
    let mention = match out.mention.as_deref().filter(|user| !user.is_empty()) {
        Some(user) => format!("<@{user}> "),
        None => String::new(),
    };
    let label = match env
        .sender
        .human_label
        .as_deref()
        .filter(|label| !label.is_empty())
    {
        Some(label) => format!(" · {}", escape_mrkdwn(label)),
        None => String::new(),
    };
    let head = format!(
        "{mention}*{}*{label}\n\n",
        escape_mrkdwn(&env.sender.session_name)
    );
    // A channel id that begins with D is a direct message, where every reply reaches the bot.
    let in_channel = match &out.target {
        SlackTarget::Channel { id } => !id.starts_with('D'),
        SlackTarget::ChannelName { .. } => true,
        _ => false,
    };
    let bot = &context.bot_handle;
    let how = if in_channel && !context.hears_channel_threads {
        format!("Reply in this thread and mention @{bot} to answer.")
    } else {
        "Reply in this thread to answer.".to_owned()
    };
    format!(
        "{head}{body}\n\n{how}\n_To write to another session, message @{bot} with a first line \"to: <session or member>\"._"
    )
}

// ---- Mentions ---------------------------------------------------------------------------------------------

/// The Slack user behind each @handle of a text: what the TypeScript kept as an object from handle to user
/// id, and lists as JavaScript lists an object's members. That order is the order the handles were put
/// in, except that a handle which is a whole number below 2^32 - 1, written without a leading zero, comes
/// before every other, the smallest first. [`link_mentions`] links the handles in that order, and one link
/// can make the next possible: `@1@a` with both handles known links both when `1` goes first.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Mentions(Vec<(String, String)>);

impl Mentions {
    /// No handle.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Says which user a handle is. A handle said twice keeps its place and takes the later user.
    pub fn insert(&mut self, name: impl Into<String>, user_id: impl Into<String>) {
        let (name, user_id) = (name.into(), user_id.into());
        match self.0.iter_mut().find(|(known, _)| *known == name) {
            Some(entry) => entry.1 = user_id,
            None => self.0.push((name, user_id)),
        }
    }

    /// Whether no handle is known.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// `Object.entries`: each handle and its user, in JavaScript's order.
    #[must_use]
    pub fn entries(&self) -> Vec<(&str, &str)> {
        fn pair(entry: &(String, String)) -> (&str, &str) {
            (&entry.0, &entry.1)
        }
        let mut numbered: Vec<(u32, (&str, &str))> = self
            .0
            .iter()
            .filter_map(|entry| Some((array_index(&entry.0)?, pair(entry))))
            .collect();
        numbered.sort_by_key(|(index, _)| *index);
        numbered
            .into_iter()
            .map(|(_, entry)| entry)
            .chain(
                self.0
                    .iter()
                    .filter(|entry| array_index(&entry.0).is_none())
                    .map(pair),
            )
            .collect()
    }
}

impl<N: Into<String>, U: Into<String>> FromIterator<(N, U)> for Mentions {
    fn from_iter<I: IntoIterator<Item = (N, U)>>(pairs: I) -> Self {
        let mut mentions = Self::new();
        for (name, user_id) in pairs {
            mentions.insert(name, user_id);
        }
        mentions
    }
}

/// The number a member's name is when JavaScript takes it for an array index: digits only, no leading
/// zero, and less than 2^32 - 1.
fn array_index(name: &str) -> Option<u32> {
    let canonical = name == "0" || !name.starts_with('0');
    if !canonical || name.is_empty() || !name.bytes().all(|byte| byte.is_ascii_digit()) {
        return None;
    }
    name.parse::<u32>().ok().filter(|index| *index != u32::MAX)
}

/// `[\w.@<-]`: what the at sign of a handle does not follow. An at sign begins a handle only where it
/// begins a word, so the domain of an email address and the `@U…` inside Slack's own `<@U…>` are not one.
///
/// The TypeScript says this with a look-behind, `(?<![\w.@<-])`, which the `regex` crate does not have, so
/// the handle patterns are matched by hand here. The look-behind reads one UTF-16 code unit and this reads
/// one character: the two agree, because every character of the class is ASCII and half of a character
/// outside the Basic Multilingual Plane is none of them.
fn joins_handle(c: char) -> bool {
    js::is_word(c) || matches!(c, '.' | '@' | '<' | '-')
}

/// Whether the at sign at byte `sign` of `text` begins a word.
fn begins_word(text: &str, sign: usize) -> bool {
    !text[..sign].chars().next_back().is_some_and(joins_handle)
}

/// Lists the @handles in a session's text, each once, as written and in the order they first appear.
///
/// `/(?<![\w.@<-])@([A-Za-z0-9][\w.-]*)/g`, then each without the `.`, `_` and `-` that end it: trailing
/// punctuation is the sentence's.
#[must_use]
pub fn mention_names(text: &str) -> Vec<String> {
    let mut names: Vec<String> = Vec::new();
    let mut from = 0;
    while let Some(at) = text[from..].find('@') {
        let sign = from + at;
        from = sign + 1;
        let rest = &text[from..];
        if !begins_word(text, sign) || !rest.starts_with(|c: char| c.is_ascii_alphanumeric()) {
            continue;
        }
        let length = rest
            .find(|c: char| !(js::is_word(c) || c == '.' || c == '-'))
            .unwrap_or(rest.len());
        // The search goes on after the handle, as a pattern's does after its match.
        from += length;
        let name = rest[..length].trim_end_matches(['.', '_', '-']);
        if !name.is_empty() && !names.iter().any(|known| known == name) {
            names.push(name.to_owned());
        }
    }
    names
}

/// Whether an @handle is a user id written bare, which needs no lookup.
#[must_use]
pub fn is_slack_user_id(name: &str) -> bool {
    USER_ID.is_match(name)
}

/// Restores, in an escaped text, Slack's own mention of a user or a channel (`<@U…>`, `<@U…|name>`,
/// `<#C…>`, `<#C…|name>`), so a session can tag a person by the id a `from` line gave it. The broadcast
/// forms (`<!here>`, `<!channel>`, `<!everyone>`, user groups) stay literal: a session does not page a
/// whole channel.
#[must_use]
pub fn pass_mention_markup(escaped: &str) -> String {
    ESCAPED_MARKUP.replace_all(escaped, "<${1}>").into_owned()
}

/// Turns each @handle the map knows into Slack's mention of that user, in an escaped text whose markup
/// [`pass_mention_markup`] has already restored. A handle is matched whole and as written, and a handle
/// whose user is not a Slack user id is left as text.
#[must_use]
pub fn link_mentions(escaped: &str, ids: &Mentions) -> String {
    let mut linked = escaped.to_owned();
    for (name, user_id) in ids.entries() {
        if is_slack_user_id(user_id) && !name.is_empty() {
            linked = link_handle(&linked, name, user_id);
        }
    }
    linked
}

/// `text.replace(/(?<![\w.@<-])@<name>(?![\w-]|[._-]+\w)/g, "<@<user id>>")`, by hand: the pattern has a
/// look-behind.
fn link_handle(text: &str, name: &str, user_id: &str) -> String {
    let handle = format!("@{name}");
    let mut linked = String::with_capacity(text.len());
    let (mut copied, mut from) = (0, 0);
    while let Some(at) = text[from..].find(&handle) {
        let (start, end) = (from + at, from + at + handle.len());
        if begins_word(text, start) && !handle_goes_on(&text[end..]) {
            linked.push_str(&text[copied..start]);
            linked.push_str(&format!("<@{user_id}>"));
            (copied, from) = (end, end);
        } else {
            from = start + 1;
        }
    }
    linked.push_str(&text[copied..]);
    linked
}

/// `[\w-]|[._-]+\w` at the start of `rest`: whether what follows a handle's name is more of a handle, so
/// that the name is not the whole of it. A letter, a digit, an underscore or a hyphen is; so is a run of
/// `.`, `_` and `-` with a letter, a digit or an underscore after any part of it. Punctuation that ends
/// the sentence is not.
fn handle_goes_on(rest: &str) -> bool {
    for (at, c) in rest.chars().enumerate() {
        if js::is_word(c) || (at == 0 && c == '-') {
            return true;
        }
        if !matches!(c, '.' | '-') {
            return false;
        }
    }
    false
}

// ---- Roster -----------------------------------------------------------------------------------------------

/// Lists the sessions a person can write to: the ones online now, the gateways left out. A gateway that
/// serves several teams (card 128) lists them grouped by team, in name order, `(none)` under a team with
/// nobody online; with one team the text is what it always was.
///
/// The teams are ordered as `localeCompare` orders their names. That is exact for names in ASCII; a name
/// with a character outside it is placed by that character's code point, after every ASCII one.
#[must_use]
pub fn slack_roster<S: AsRef<str>>(
    teams: &[TeamSessions<'_>],
    gateway_ids: &[S],
    bot_handle: &str,
) -> String {
    let mut sorted: Vec<&TeamSessions<'_>> = teams.iter().collect();
    sorted.sort_by(|x, y| js::locale_compare(x.team_name, y.team_name));
    let mut lines: Vec<String> = Vec::new();
    if let [only] = sorted.as_slice() {
        lines.push(format!(
            "Sessions on Brigade team *{}* right now (names and labels are their owners' own words):",
            escape_mrkdwn(only.team_name)
        ));
        lines.push(String::new());
        lines.extend(roster_rows(&only.sessions, gateway_ids));
    } else {
        lines.push(format!(
            "Sessions on the {} Brigade teams this gateway serves right now (names and labels are their owners' own words):",
            sorted.len()
        ));
        for team in &sorted {
            lines.push(String::new());
            lines.push(format!("*{}*", escape_mrkdwn(team.team_name)));
            lines.extend(roster_rows(&team.sessions, gateway_ids));
        }
    }
    lines.push(String::new());
    lines.push(format!(
        "To write to one: message @{bot_handle} with a first line `to: <the five characters, or a member's label>`, or `/brigade send <five characters or label> <text>`."
    ));
    lines.join("\n")
}

fn roster_rows<S: AsRef<str>>(sessions: &[&SessionRecord], gateway_ids: &[S]) -> Vec<String> {
    let mut lines = Vec::new();
    for session in sessions {
        if gateway_ids.iter().any(|id| id.as_ref() == session.session_id)
            || is_gateway_session(session)
            || session.state == "offline"
        {
            continue;
        }
        let label = match session.human_label.as_deref().filter(|label| !label.is_empty()) {
            Some(label) => format!(" · {}", escape_mrkdwn(label)),
            None => String::new(),
        };
        lines.push(format!(
            "• `{}`  {}  _{}_{label}",
            js::utf16_suffix(&session.session_id, 5),
            escape_mrkdwn(&session.session_name),
            session.state
        ));
        if let Some(description) = session
            .session_description
            .as_deref()
            .filter(|line| !line.is_empty())
        {
            lines.push(format!("      ↳ {}", escape_mrkdwn(&one_line(description))));
        }
    }
    if lines.is_empty() {
        lines.push("  (none)".to_owned());
    }
    lines
}

#[cfg(test)]
mod tests {
    use super::{Mentions, array_index, handle_goes_on, link_mentions, mention_names, mentions_of};

    #[test]
    fn a_mention_is_closed_at_once_or_after_a_bar() {
        assert_eq!(mentions_of("a <@U1> b <@U1|x y> c", "U1"), [(2, 7), (10, 19)]);
        // No bracket closes it, or something other than a bar follows the id: no mention.
        assert_eq!(mentions_of("<@U1|never closed", "U1"), []);
        assert_eq!(mentions_of("<@U12> <@U1 >", "U1"), []);
        // A bar's label runs to the next bracket, across a line.
        assert_eq!(mentions_of("<@U1|a\nb>", "U1"), [(0, 9)]);
        // The search goes on inside a mention that was never closed.
        assert_eq!(mentions_of("<@U1|<@U1>", "U1"), [(0, 10)]);
        assert_eq!(mentions_of("<@U1x<@U1>", "U1"), [(5, 10)]);
    }

    #[test]
    fn a_handle_begins_a_word_and_is_letters_digits_and_the_three_marks() {
        assert_eq!(mention_names("@a @b"), ["a", "b"]);
        for none in [
            "x@a", "_@a", ".@a", "@@a", "<@a", "-@a", "9@a", "@", "@ a", "@_a", "@.a", "@é",
        ] {
            assert!(mention_names(none).is_empty(), "{none}");
        }
        // What stands before the at sign is one character, of any width.
        assert_eq!(mention_names("é@a 🙂@b >@c (@d"), ["a", "b", "c", "d"]);
        // The name ends at the first character that is none of its own, and loses the marks that end it.
        assert_eq!(
            mention_names("@a.b-c_d! @e._- @f..g @h é"),
            ["a.b-c_d", "e", "f..g", "h"]
        );
        assert_eq!(mention_names("@aé @a"), ["a"]);
        // The search goes on after a handle, so an at sign inside one is not looked at again.
        assert_eq!(mention_names("@a@b"), ["a"]);
        assert_eq!(mention_names("@a.@b"), ["a"]);
    }

    #[test]
    fn what_follows_a_name_decides_whether_it_is_the_whole_handle() {
        for more in ["x", "_", "-", "9", ".x", "._", "..x", ".-x", ".-_", "-."] {
            assert!(handle_goes_on(more), "{more}");
        }
        for whole in ["", " ", ".", "..", ".-", ". x", ".é", "é", ",x", ":", ".,x"] {
            assert!(!handle_goes_on(whole), "{whole}");
        }
    }

    #[test]
    fn handles_are_listed_as_javascript_lists_the_members_of_an_object() {
        let mentions: Mentions = [
            ("b", "U0000000B"),
            ("10", "U00000010"),
            ("a", "U0000000A"),
            ("9", "U00000009"),
            ("07", "U00000007"),
            ("4294967295", "U4294967295"),
            ("4294967294", "U4294967294"),
            ("0", "U00000000"),
            ("b", "U000000BB"),
        ]
        .into_iter()
        .collect();
        let names: Vec<&str> = mentions.entries().into_iter().map(|(name, _)| name).collect();
        assert_eq!(
            names,
            ["0", "9", "10", "4294967294", "b", "a", "07", "4294967295"]
        );
        assert_eq!(mentions.entries()[4], ("b", "U000000BB"));
        assert_eq!(array_index(""), None);
        assert_eq!(array_index("1e3"), None);
        assert_eq!(array_index("99999999999"), None);
    }

    #[test]
    fn one_link_can_make_the_next_possible() {
        let known = |names: &[&str]| -> Mentions { names.iter().map(|name| (*name, "U0000000A")).collect() };
        // `@a` is no handle while a digit stands before it; once `@1` is a mention, a bracket does.
        assert_eq!(
            link_mentions("@1@a", &known(&["a", "1"])),
            "<@U0000000A><@U0000000A>"
        );
        assert_eq!(link_mentions("@b@a", &known(&["a", "b"])), "<@U0000000A>@a");
        assert_eq!(
            link_mentions("@b@a", &known(&["b", "a"])),
            "<@U0000000A><@U0000000A>"
        );
        // A name is text, whatever a pattern would make of it, and a name with an at sign is matched whole.
        assert_eq!(link_mentions("@a.c @a+c", &known(&["a.c"])), "<@U0000000A> @a+c");
        assert_eq!(link_mentions("@a@a@a", &known(&["a@a"])), "<@U0000000A>@a");
    }
}
