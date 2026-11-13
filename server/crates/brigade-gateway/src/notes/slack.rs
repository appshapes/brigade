//! What `slack-in` and `slack-out` say and decide between their calls
//! (`supabase/functions/slack-in/index.ts`, `supabase/functions/slack-out/index.ts`).
//!
//! Inbound, a person is answered where they are: a slash command in its own HTTP response, which only
//! they see; a direct message in the direct message; a channel mention ephemerally, so the channel is not
//! told twice. Outbound, a session whose message could not be posted is told by a Brigade message, and a
//! failure Slack says a later attempt can mend leaves the message in the inbox for the next turn.

use serde::Serialize;

use super::{Sending, failure, ignored, json, not_delivered_key};
use crate::connector::Answer;
use crate::js;
use crate::json::Json;
use crate::providers::slack::{Metadata, PostEphemeral, PostMessage, Posted, SlackError, granted_scopes};
use crate::resolve::{BrigadeBody, Direction, Envelope, SessionRecord, TeamRecipient, ThreadKind, ThreadRow};
use crate::slack::{
    InboundSlack, MessageSource, ParsedRequest, SlackInbound, SlackOutbound, SlackTarget, TargetSource,
    escape_mrkdwn, parse_command_request, parse_events_request,
};
use crate::text::excerpt;

/// The bot's handle where the gateway's configuration names none.
pub const DEFAULT_BOT_HANDLE: &str = "brigade";
/// The workspace's name where the gateway's configuration names none.
pub const DEFAULT_WORKSPACE: &str = "slack";
/// The reaction the bot puts on a person's message once it is sent on.
pub const REACTION: &str = "eyes";
/// The scope under which Slack delivers a channel thread's replies to the gateway without a mention.
pub const CHANNEL_HISTORY_SCOPE: &str = "channels:history";
/// The summary of the note that tells a session its message was not delivered.
pub const NOT_DELIVERED_SUMMARY: &str = "slack gateway: not delivered";
/// How many code points of a message's summary, or of its text, a thread row keeps as its subject.
pub const SUBJECT_CHARS: usize = 120;
/// Why the inbound handler fails when no team of the project has a Slack gateway.
pub const NOT_INSTALLED: &str =
    "the slack gateway is not installed for any team of this project: run its install target";

// ---- slack-in: the request --------------------------------------------------------------------------------

/// The challenge of Slack's check of the request URL, when a request is one. It carries nothing but a
/// nonce to answer, and it is answered before the signature is checked: Slack sends it when the request
/// URL is saved, which can be before the gateway has any secret. `content_type` is the request's
/// `Content-Type` header, empty when it has none.
#[must_use]
pub fn challenge(content_type: &str, raw: &str) -> Option<String> {
    if !content_type.contains("application/json") {
        return None;
    }
    let request = Json::parse(raw)?;
    if request.member("type").and_then(Json::as_str) != Some("url_verification") {
        return None;
    }
    request
        .member("challenge")
        .and_then(Json::as_str)
        .map(str::to_owned)
}

/// Reads a signed request: a slash command when its `Content-Type` says it is a form, an Events API body
/// otherwise. `bot_user_id` and `new_trigger` are [`parse_events_request`]'s and
/// [`parse_command_request`]'s.
#[must_use]
pub fn parse_request(
    content_type: &str,
    raw: &str,
    bot_user_id: Option<&str>,
    new_trigger: impl FnOnce() -> String,
) -> ParsedRequest {
    if content_type.contains("application/x-www-form-urlencoded") {
        parse_command_request(raw, new_trigger)
    } else {
        parse_events_request(raw, bot_user_id)
    }
}

/// The key a request is recognised by when Slack delivers it again, and the idempotency key of the
/// Brigade message it becomes.
#[must_use]
pub fn received_key(event_id: &str) -> String {
    format!("slack:{event_id}")
}

/// The answer to Slack's check of the request URL.
#[must_use]
pub fn answer_challenge(challenge: &str) -> Answer {
    #[derive(Serialize)]
    struct Challenge<'a> {
        challenge: &'a str,
    }
    json(200, &Challenge { challenge })
}

/// The answer to a request that does not carry Slack's signature.
#[must_use]
pub fn answer_unsigned() -> Answer {
    failure(401, "the request is not signed by this gateway's Slack app")
}

/// The answer to a request the gateway does not carry. `reason` is [`ParsedRequest::Ignore`]'s or
/// [`SlackInbound::Ignore`]'s.
#[must_use]
pub fn answer_ignored(reason: &str) -> Answer {
    ignored(reason)
}

/// The answer to an event whose person was told something where they are.
#[must_use]
pub fn answer_told() -> Answer {
    #[derive(Serialize)]
    struct Told {
        told: bool,
    }
    json(200, &Told { told: true })
}

/// The answer to an event that was sent on as a Brigade message. `team_name` is the name of the team
/// whose gateway sent it.
#[must_use]
pub fn answer_carried(
    message_id: &str,
    recipient_session_id: &str,
    team_name: &str,
    source: TargetSource,
    duplicate: bool,
) -> Answer {
    #[derive(Serialize)]
    struct Carried<'a> {
        message_id: &'a str,
        to: &'a str,
        team: &'a str,
        source: &'a str,
        duplicate: bool,
    }
    json(
        200,
        &Carried {
            message_id,
            to: recipient_session_id,
            team: team_name,
            source: source.as_str(),
            duplicate,
        },
    )
}

/// Why the inbound handler fails when the team a session is in has no Slack gateway.
#[must_use]
pub fn no_gateway(team_name: &str) -> String {
    format!("team {team_name} has no slack gateway row")
}

// ---- slack-in: what a person is told ----------------------------------------------------------------------

/// What a person is told when the directives at the top of their message could not be read.
#[must_use]
pub fn invalid(reason: &str) -> String {
    format!("The lines at the top of your message could not be read: {reason}.")
}

/// What a person is told when their message names no session and answers none. `roster` is
/// [`crate::slack_roster`]'s text.
#[must_use]
pub fn unaddressed(roster: &str) -> String {
    format!(
        "No session was named. Start your message with a line `to: <session or member>`, or reply in the thread under a session's message.\n\n{roster}"
    )
}

/// What a person is told for a command: the roster for `sessions` and `help`, and for any other word that
/// it is none, then the roster.
#[must_use]
pub fn command(command: &str, roster: &str) -> String {
    if command == "sessions" || command == "help" {
        roster.to_owned()
    } else {
        format!(
            "Unknown command `{command}`. Try `sessions`, `help`, or `send <session or member> <text>`.\n\n{roster}"
        )
    }
}

/// What a person is told when their message names no session it can go to. `error` is
/// [`crate::resolve_recipient_across`]'s; it may name sessions and teams, so it is escaped like every
/// other untrusted text.
#[must_use]
pub fn not_sent(error: &str, roster: &str) -> String {
    format!("The message was not sent: {}.\n\n{roster}", escape_mrkdwn(error))
}

/// What a person is told under a roster that does not list every team the gateway serves, because the
/// gateways of `left_out` of them are no members of their teams at the moment: a team's owner revoked its
/// gateway, and the gateway has not joined again. `None` when every team is listed. No team is named: the
/// person may be of another one.
///
/// The Edge Functions have no such sentence. They fail a request when one team's roster cannot be read,
/// which stops every team's people for as long as one team's gateway is out.
#[must_use]
pub fn not_connected(left_out: usize) -> Option<String> {
    match left_out {
        0 => None,
        1 => Some(
            "One team's gateway is not connected at the moment, so its sessions cannot be reached."
                .to_owned(),
        ),
        several => Some(format!(
            "{several} teams' gateways are not connected at the moment, so their sessions cannot be reached."
        )),
    }
}

/// The roster a person is shown: [`crate::slack_roster`]'s text of the teams whose gateways are members,
/// and under it [`not_connected`]'s sentence when `left_out` teams are not among them. Every answer that
/// shows the roster shows this one, so a person told that nobody has the name they wrote is told as well
/// that some sessions cannot be reached now.
#[must_use]
pub fn roster_of_connected(roster: &str, left_out: usize) -> String {
    match not_connected(left_out) {
        Some(sentence) => format!("{roster}\n\n{sentence}"),
        None => roster.to_owned(),
    }
}

/// What a person is told about a message that is not sent on: `None` for one that is to be sent, and for
/// one that is ignored, which is not answered.
#[must_use]
pub fn answer_for(resolution: &SlackInbound, roster: &str) -> Option<String> {
    match resolution {
        SlackInbound::Invalid { reason } => Some(invalid(reason)),
        SlackInbound::Unaddressed { .. } => Some(unaddressed(roster)),
        SlackInbound::Command { command: word, .. } => Some(command(word, roster)),
        SlackInbound::Message { .. } | SlackInbound::Ignore { .. } => None,
    }
}

/// How a person is answered where they are.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Tell {
    /// In the HTTP response to a slash command, which only the person sees. Nothing else is answered.
    Response(Answer),
    /// With `chat.postMessage`, in the thread of a direct message. A post that fails is not tried again,
    /// and the request is answered [`answer_told`] either way.
    Post(PostMessage),
    /// With `chat.postEphemeral`, in a channel. A post that fails is not tried again, and the request is
    /// answered [`answer_told`] either way.
    Ephemeral(PostEphemeral),
}

/// Decides how `text` reaches the person who wrote `message`. An answer in a direct message goes under the
/// thread the person wrote in, or under their message when they wrote in none; an answer in a channel goes
/// into the thread they wrote in, or into the channel.
#[must_use]
pub fn tell(message: &InboundSlack, text: &str) -> Tell {
    if message.source == MessageSource::Command {
        return Tell::Response(answer_command(text));
    }
    if message.is_direct() {
        Tell::Post(PostMessage {
            channel: message.channel.clone(),
            text: text.to_owned(),
            thread_ts: message.thread_ts.clone().or_else(|| message.ts.clone()),
            metadata: None,
        })
    } else {
        Tell::Ephemeral(PostEphemeral {
            channel: message.channel.clone(),
            user: message.user.clone(),
            text: text.to_owned(),
            thread_ts: message.thread_ts.clone(),
        })
    }
}

/// The answer to a slash command: `text`, shown to the person alone.
#[must_use]
pub fn answer_command(text: &str) -> Answer {
    #[derive(Serialize)]
    struct Ephemeral<'a> {
        response_type: &'static str,
        text: &'a str,
    }
    json(
        200,
        &Ephemeral {
            response_type: "ephemeral",
            text,
        },
    )
}

// ---- slack-in: a message that is sent on ------------------------------------------------------------------

/// Whether the channel's name is asked of Slack for the body: not in a direct message, which has none.
#[must_use]
pub fn wants_channel_name(message: &InboundSlack) -> bool {
    !message.is_direct()
}

/// The Brigade message a person's Slack message becomes. `carried` is [`crate::slack_body`]'s, and
/// `summary` and `reply_to` are the resolution's: a summary the person gave is the message's, and
/// otherwise the body's own is.
#[must_use]
pub fn send(
    message: &InboundSlack,
    recipient_session_id: &str,
    carried: &BrigadeBody,
    summary: Option<&str>,
    reply_to: Option<&str>,
) -> Sending {
    Sending {
        recipient_session_id: recipient_session_id.to_owned(),
        body: carried.body.clone(),
        summary: summary.unwrap_or(&carried.summary).to_owned(),
        reply_to: reply_to.map(str::to_owned),
        idempotency_key: received_key(&message.event_id),
    }
}

/// The thread row of a person's message that was sent on, so that the session's answer finds the thread.
/// `message_id` is the id the engine gave the Brigade message and `summary` is [`crate::slack_body`]'s,
/// whatever summary the person gave. The thread is the one the person wrote in, or the one their message
/// starts; a slash command has neither.
#[must_use]
pub fn thread_row_in(
    message_id: &str,
    recipient_session_id: &str,
    message: &InboundSlack,
    summary: &str,
) -> ThreadRow {
    ThreadRow {
        brigade_message_id: message_id.to_owned(),
        direction: Direction::In,
        session_id: recipient_session_id.to_owned(),
        address: message.channel.clone(),
        mail_message_id: message.thread_ts.clone().or_else(|| message.ts.clone()),
        provider_mail_id: Some(message.event_id.clone()),
        subject: Some(summary.to_owned()),
        kind: Some(ThreadKind::Slack),
        actor: Some(message.user.clone()),
    }
}

/// Says where a person's message went: the session (and its team, when the gateway serves several), and,
/// when they named a member, why that one and which other sessions of theirs are online, so that a wrong
/// pick is corrected by sending to one of those.
#[must_use]
pub fn sent_note(recipient: &TeamRecipient<'_>, served_teams: usize) -> String {
    let name = |session: &SessionRecord| {
        format!(
            "*{}* (`…{}`)",
            escape_mrkdwn(&session.session_name),
            js::utf16_suffix(&session.session_id, 5)
        )
    };
    let found = &recipient.recipient;
    let mut text = format!("Sent to {}", name(found.session));
    if served_teams > 1 {
        text.push_str(&format!(" on team *{}*", escape_mrkdwn(recipient.team_name)));
    }
    if let Some(who) = &found.who {
        let seen = if found.session.state == "offline" {
            "offline; the message waits for it".to_owned()
        } else {
            format!("the {} session of theirs seen most recently", found.session.state)
        };
        text.push_str(&format!(": {}'s {seen}", escape_mrkdwn(who)));
        let online: Vec<String> = found
            .others
            .iter()
            .filter(|session| session.state != "offline")
            .map(|session| name(session))
            .collect();
        if !online.is_empty() {
            let (sessions, are) = if online.len() == 1 {
                ("session", "is")
            } else {
                ("sessions", "are")
            };
            text.push_str(&format!(
                ". {} other {sessions} of theirs {are} online: {}; send to one of those to choose it",
                online.len(),
                online.join(", ")
            ));
        }
    }
    format!("{text}. Accepted means stored for that session, not read; its reply will come to you here.")
}

/// How the person is told that their message was sent on, when they are: a slash command is always
/// answered with [`sent_note`], in its response; a person who named a member is told which of their
/// sessions got the message; a person who named a session is not told, and the reaction on their message
/// is the whole of it.
#[must_use]
pub fn confirmation(
    message: &InboundSlack,
    recipient: &TeamRecipient<'_>,
    served_teams: usize,
) -> Option<Tell> {
    confirmation_among(message, recipient, served_teams, 0)
}

/// [`confirmation`] for a request that left `left_out` of the gateway's teams out, because their gateways
/// are no members of them at the moment, and found the session among the `connected` others.
///
/// A name that sessions of two teams answer to is refused, and the person is asked which they mean: a
/// member's label, and the characters that end a session's id as well. With one of those teams left out
/// the name is answered to once, and the message goes on. So whenever a team was left out the person is
/// told where the message went, however they named the session, and the reaction is not the whole of it.
/// The note says under where it went that some sessions could not be reached ([`not_connected`]), and it
/// names the team the message went to as it does for every gateway that serves several: the person can
/// tell that the session they meant may be one of those that were not weighed. With no team left out it
/// is [`confirmation`], word for word.
#[must_use]
pub fn confirmation_among(
    message: &InboundSlack,
    recipient: &TeamRecipient<'_>,
    connected: usize,
    left_out: usize,
) -> Option<Tell> {
    let named_a_member = recipient.recipient.who.is_some();
    (message.source == MessageSource::Command || named_a_member || left_out > 0).then(|| {
        let note = sent_note(recipient, connected + left_out);
        match not_connected(left_out) {
            Some(sentence) => tell(message, &format!("{note}\n\n{sentence}")),
            None => tell(message, &note),
        }
    })
}

// ---- slack-out --------------------------------------------------------------------------------------------

/// Whether the installed app hears a channel thread's replies without a mention: `auth.test` succeeded and
/// the scopes its answer lists hold [`CHANNEL_HISTORY_SCOPE`]. `scopes_header` is the value of the
/// answer's `x-oauth-scopes` header. When Slack cannot be asked the footer asks for the mention, which
/// always works.
#[must_use]
pub fn hears_channel_threads<T>(auth_test: &Result<T, SlackError>, scopes_header: Option<&str>) -> bool {
    auth_test.is_ok()
        && granted_scopes(scopes_header)
            .is_some_and(|scopes| scopes.iter().any(|scope| scope == CHANNEL_HISTORY_SCOPE))
}

/// The key the one lookup of a handle in a turn is kept under: the handle in lowercase, so that `@Alice`
/// and `@alice` are looked up once.
#[must_use]
pub fn mention_key(name: &str) -> String {
    name.to_lowercase()
}

/// A target as the session that named it is told of it.
#[must_use]
pub fn describe_target(target: &SlackTarget) -> String {
    match target {
        SlackTarget::User { id } | SlackTarget::Channel { id } => id.clone(),
        SlackTarget::UserName { name } => format!("@{name}"),
        SlackTarget::UserEmail { email } => email.clone(),
        SlackTarget::ChannelName { name } => format!("#{name}"),
    }
}

fn note(env: &Envelope, body: String) -> Sending {
    Sending {
        recipient_session_id: env.sender.session_id.clone(),
        body,
        summary: NOT_DELIVERED_SUMMARY.to_owned(),
        reply_to: Some(env.message_id.clone()),
        idempotency_key: not_delivered_key(&env.message_id),
    }
}

/// The note to a session whose message could not be delivered. `reason` is
/// [`crate::resolve_slack_outbound`]'s, or the text of the failure that ended the search for its target.
#[must_use]
pub fn undeliverable(env: &Envelope, reason: &str) -> Sending {
    note(
        env,
        format!(
            "The Slack gateway could not deliver your message {}: {reason}",
            env.message_id
        ),
    )
}

/// The note to a session whose message names a person or a channel Slack does not know.
#[must_use]
pub fn no_match(env: &Envelope, target: &SlackTarget) -> Sending {
    undeliverable(
        env,
        &format!("no Slack user or channel matches \"{}\"", describe_target(target)),
    )
}

/// The note to a session whose message Slack refused to post in `channel`. `reason` is the failure's text.
#[must_use]
pub fn not_posted(env: &Envelope, channel: &str, reason: &str) -> Sending {
    note(
        env,
        format!(
            "The Slack gateway could not post your message {} to {channel}: {reason}",
            env.message_id
        ),
    )
}

/// Whether a failure ends a message's delivery: the session is told and the message acknowledged. A
/// failure that does not is one Slack says a later attempt can mend, and the message is left in the inbox
/// for the next turn.
///
/// A request that got no answer, and an answer that could not be read, are final: the TypeScript took
/// only Slack's own word for a failure that passes.
#[must_use]
pub fn is_final(error: &SlackError) -> bool {
    !error.retryable()
}

/// Whether a refused post is answered by joining the channel and posting once more: the bot is not in it.
/// A join that fails, and a second post that fails, are the delivery's failure.
#[must_use]
pub fn joins_first(error: &SlackError) -> bool {
    error.code == "not_in_channel"
}

/// The post of a session's message: `text` is [`crate::render_slack_text`]'s and `channel` the channel
/// the target resolved to. A reply goes into its thread. The post carries the Brigade message's id, its
/// sender's session and the team's name beside its text.
#[must_use]
pub fn post(env: &Envelope, out: &SlackOutbound, channel: &str, text: &str, team_name: &str) -> PostMessage {
    let field = |name: &str, value: &str| (name.to_owned(), value.to_owned());
    PostMessage {
        channel: channel.to_owned(),
        text: text.to_owned(),
        thread_ts: out.thread_ts.clone(),
        metadata: Some(Metadata {
            event_type: "brigade_message".to_owned(),
            event_payload: vec![
                field("message_id", &env.message_id),
                field("from_session", &env.sender.session_id),
                field("team", team_name),
            ],
        }),
    }
}

/// The thread row of a session's message that was posted, so that a person's reply in the thread finds
/// the session. The thread is the one the post went into, or the one it starts.
#[must_use]
pub fn thread_row_out(env: &Envelope, out: &SlackOutbound, posted: &Posted) -> ThreadRow {
    let about = match env.summary.as_deref().filter(|summary| !summary.is_empty()) {
        Some(summary) => summary,
        None => &out.text,
    };
    ThreadRow {
        brigade_message_id: env.message_id.clone(),
        direction: Direction::Out,
        session_id: env.sender.session_id.clone(),
        address: posted.channel.clone(),
        mail_message_id: Some(out.thread_ts.clone().unwrap_or_else(|| posted.ts.clone())),
        provider_mail_id: Some(posted.ts.clone()),
        subject: Some(excerpt(about, SUBJECT_CHARS)),
        kind: Some(ThreadKind::Slack),
        actor: out.mention.clone(),
    }
}
