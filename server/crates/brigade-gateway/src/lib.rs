//! The rules of Brigade's mail and Slack gateways, ported from the TypeScript of the Edge Functions
//! (`supabase/functions/_shared/` and the four handlers beside it).
//!
//! A gateway is an ordinary member of a team whose one session relays between the team's sessions and
//! people outside it: a person's mail or Slack message becomes a Brigade message to the session it names,
//! and a session's message to the gateway becomes a mail or a Slack post. This crate is what a gateway
//! decides and what it writes, and nothing else. It does no I/O, reads no clock, draws no randomness and
//! logs nothing: the caller receives the webhook, makes the HTTP requests and keeps the thread table, and a
//! function that needs the time, or something drawn at random, is handed it.
//!
//! Every text here reaches a person by mail or on Slack, or a session as a message, so each is the
//! TypeScript's, byte for byte, and so is what the TypeScript took from JavaScript without saying so: its
//! white space, its string lengths, its numbers, its dates and the order it lists things in. Where the port
//! could not follow, the module says where.
//!
//! - [`block`] reads the directives at the top of a text and writes the envelope block.
//! - [`mail`] holds the two mail shapes and the text rules every provider shares.
//! - [`text`] cuts a text to a line, to a number of characters and to a number of bytes.
//! - [`resolve`] decides where a mail and a message go, names the session an argument means, and writes
//!   the Brigade body and the mail.
//! - [`slack`] is the same for Slack: what an event and a slash command carry, where a person's message and
//!   a session's go, the Brigade body and the Slack text, the mentions and the roster.
//! - [`connector`] is the connector contract: the two JSON shapes, the shared secret, and what a connector
//!   and the core answer each other.
//! - [`webhook_signature`] checks a Standard Webhooks signature.
//! - [`providers`] are Resend and Postmark behind the connector, and the Slack Web API.
//! - [`notes`] is what the four handlers say and decide between their calls, so that a handler is the
//!   calls and their order.
//!
//! What the two gateways share is: [`parse_leading`], [`render_block`], [`escape_block_lines`] and
//! [`is_bare_command`]; [`resolve_recipient`] and [`resolve_recipient_across`]; [`decision_view`];
//! [`is_gateway_session`]; [`excerpt`], [`cut_chars`], [`one_line`] and [`truncate_utf8`]; and the thread
//! table's row, [`ThreadRow`], which mail reads through [`ThreadLookup`] and Slack through that and
//! [`SlackThreads`].

pub mod block;
pub mod connector;
pub mod mail;
pub mod notes;
pub mod providers;
pub mod resolve;
pub mod slack;
pub mod text;
pub mod webhook_signature;

mod js;
mod json;
mod time;

pub use block::{
    BlockError, Fields, Leading, escape_block_lines, find_blocks, is_bare_command, last_block, parse_leading,
    render_block,
};
pub use connector::{
    Answer, Hook, HttpRequest, Provider, Request, Route, SendError, WebhookRejected, authorized,
    from_inbound_wire, from_outbound_wire, to_inbound_wire, to_outbound_wire,
};
pub use mail::{InboundMail, OutboundMail, is_address, parse_address, parse_message_ids, strip_quoted};
pub use notes::Sending;
pub use providers::slack::SlackError;
pub use resolve::{
    BrigadeBody, Direction, Envelope, How, Inbound, MAX_BODY_BYTES, MAX_SUMMARY_CHARS, Outbound, Recipient,
    RenderContext, Sender, SessionRecord, Source, TeamRecipient, TeamSessions, ThreadKind, ThreadLookup,
    ThreadRow, brigade_body, decision_view, is_gateway_session, render_outbound, resolve_inbound,
    resolve_outbound, resolve_recipient, resolve_recipient_across, roster_text,
};
pub use slack::{
    InboundSlack, Mentions, MessageSource, ParsedRequest, SlackContext, SlackInbound, SlackOutbound,
    SlackRenderContext, SlackTarget, SlackThreads, TargetSource, escape_mrkdwn, is_slack_user_id,
    link_mentions, mention_names, mentions_user, parse_command_request, parse_events_request,
    parse_slack_target, pass_mention_markup, render_slack_text, resolve_slack_inbound,
    resolve_slack_outbound, slack_body, slack_description, slack_roster, strip_mention,
    verify_slack_signature,
};
pub use text::{cut_chars, excerpt, one_line, truncate_utf8};
