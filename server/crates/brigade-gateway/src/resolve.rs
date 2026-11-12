//! The gateway core (`supabase/functions/_shared/gateway.ts`): where a person's mail goes and the Brigade
//! body it becomes, where a session's message goes and the mail it becomes, and the rules both gateways
//! share for naming a session.
//!
//! The thread table is behind [`ThreadLookup`], so every rule here runs without a store. The precedence a
//! person's mail is resolved by: an explicit block (or `to:` line) they typed; then the reply address tag
//! and the mail headers, through the thread table; then a block quoted from the mail they answer.
//!
//! What is not mail's alone is public for the Slack gateway to call: [`resolve_recipient`],
//! [`resolve_recipient_across`], [`decision_view`], [`is_gateway_session`] and the shapes they read.

use std::cmp::Ordering;
use std::sync::LazyLock;

pub use brigade_core::limits::{MAX_BODY_BYTES, MAX_SUMMARY_CHARS};
use regex::Regex;

use crate::block::{
    BlockError, Fields, escape_block_lines, is_bare_command, last_block, parse_leading, render_block,
};
use crate::js;
use crate::mail::{InboundMail, OutboundMail, domain_of, is_address, local_of, strip_quoted};
use crate::text::{EXCERPT_CHARS, cut_chars, excerpt, one_line, truncate_utf8};

/// The `harness_version` the mail gateway registers its session with.
pub const GATEWAY_VERSION: &str = "0.1.0";
/// The name of the mail gateway's session.
pub const SESSION_NAME: &str = "mail-gateway";
/// The `harness` of the mail gateway's session. Members find the gateway on the roster by it.
pub const HARNESS: &str = "gateway-email";
/// What the harness of every gateway begins with, whatever it relays to. The Go side of the same rule is
/// `gateway.IsHarness` (`internal/gateway/harness.go`); the prefix has to agree across the two.
pub const GATEWAY_HARNESS_PREFIX: &str = "gateway-";
/// What ends a mail's text that was cut to fit a Brigade message.
pub const MAIL_CUT_MARKER: &str =
    "\n[… cut by the mail gateway: the email was longer than a Brigade message may be]";

/// `/brigade\.([0-9a-f-]{36})@/i`: a Message-ID the gateway once wrote for a Brigade message.
static OWN_MESSAGE_ID: LazyLock<Regex> = LazyLock::new(|| js::regex(r"(?i-u:brigade)\.([0-9a-fA-F-]{36})@"));

/// `/^Decision d-\d{8}-[0-9a-f]{6}: (.+)$/`.
static DECISION: LazyLock<Regex> = LazyLock::new(|| {
    js::regex(&format!(
        r"^Decision d-[0-9]{{8}}-[0-9a-f]{{6}}: ({dot}+)$",
        dot = js::DOT
    ))
});

/// `/, answer by (\d{4}-\d{2}-\d{2})$/`.
static ANSWER_BY: LazyLock<Regex> = LazyLock::new(|| js::regex(r", answer by ([0-9]{4}-[0-9]{2}-[0-9]{2})$"));

/// Who sent a Brigade message.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Sender {
    /// The sender's principal reference.
    pub principal_ref: String,
    /// The label the sender's member chose; unverified text.
    pub human_label: Option<String>,
    /// The sending session's id.
    pub session_id: String,
    /// The sending session's name.
    pub session_name: String,
}

/// A Brigade message as the gateway's session receives it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Envelope {
    /// The message's id.
    pub message_id: String,
    /// The team the message is in.
    pub team_ref: String,
    /// Who sent it.
    pub sender: Sender,
    /// The session it was sent to: the gateway's.
    pub recipient_session_id: String,
    /// The sender's one-line summary.
    pub summary: Option<String>,
    /// The body, directives and all.
    pub body: String,
    /// The message this one answers.
    pub reply_to: Option<String>,
    /// How many replies deep the message is.
    pub hop_count: u32,
    /// When the backend accepted it, as the backend writes a time.
    pub created_at: String,
}

/// One row of a team's roster.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SessionRecord {
    /// The session's id.
    pub session_id: String,
    /// The session's name. It is never an address.
    pub session_name: String,
    /// The session's own line about what it is doing.
    pub session_description: Option<String>,
    /// The principal reference of the session's member.
    pub principal_ref: String,
    /// The label the member chose; unverified text.
    pub human_label: Option<String>,
    /// `active`, `idle` or `offline`.
    pub state: String,
    /// What the session does with a team message: `accept`, `hold` or `refuse`.
    pub inbound: Option<String>,
    /// The harness the session runs in.
    pub harness: Option<String>,
    /// When the backend last heard from the session.
    pub last_seen_at: Option<String>,
}

/// Which way a thread row's message went.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Direction {
    /// From a person to a session.
    In,
    /// From a session to a person.
    Out,
}

/// Which gateway a thread row belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ThreadKind {
    /// The mail gateway.
    Email,
    /// The Slack gateway.
    Slack,
}

/// What a gateway remembers of one Brigade message it relayed, so that an answer finds its way back.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ThreadRow {
    /// The Brigade message the row describes.
    pub brigade_message_id: String,
    /// Which way the message went.
    pub direction: Direction,
    /// The session: the recipient of a message that came in, the sender of one that went out.
    pub session_id: String,
    /// The person's address; on Slack, the channel.
    pub address: String,
    /// The person's Message-ID for a mail that came in; on Slack, the thread's root.
    pub mail_message_id: Option<String>,
    /// The provider's own id.
    pub provider_mail_id: Option<String>,
    /// The mail's subject, which a reply repeats.
    pub subject: Option<String>,
    /// The gateway the row belongs to; a row with none is mail's.
    pub kind: Option<ThreadKind>,
    /// On Slack, the person, so that a reply can mention them.
    pub actor: Option<String>,
}

/// The thread table, as the rules read it. The caller keeps it; a lookup that finds nothing is `None`.
pub trait ThreadLookup {
    /// The row of one Brigade message.
    fn by_brigade_id(&self, id: &str) -> Option<ThreadRow>;
    /// The newest mail row whose Message-ID is one of `ids`.
    fn by_mail_ids(&self, ids: &[String]) -> Option<ThreadRow>;
}

/// The mail gateway session's `↳` line: the one place the roster tells a model and a person where mail
/// goes. At most 256 code points (`docs/protocol-v1.md` 4.4.2).
#[must_use]
pub fn description(public_address: &str) -> String {
    format!(
        "email gateway: a person writes to {public_address} with a first line \"to: <session or member>\"; a session writes to me with the same line, \"to: <address>\""
    )
}

/// The Reply-To address of an outbound mail: the inbox with the Brigade message id as a plus tag, so a
/// reply names the message it answers in its own To header, whatever the mail client did to the rest.
#[must_use]
pub fn reply_tag(inbox: &str, message_id: &str) -> String {
    format!("{}+{message_id}@{}", local_of(inbox), domain_of(inbox))
}

/// The message id a reply address carries, in lowercase, when one of the delivery addresses is the inbox
/// with a plus tag that has the form of one.
///
/// A message id has one of two forms, by the backend that made it: a UUID where the backend is a Supabase
/// project, as the TypeScript knew it, and thirty-two hexadecimal digits where it is a Brigade server. The
/// second is the one departure from the TypeScript here: without it no reply to a mail this gateway sent
/// from a server would find its thread by the address it was sent to.
#[must_use]
pub fn tag_of<S: AsRef<str>>(addresses: &[S], inbox: &str) -> Option<String> {
    let tagged = format!("{}+", local_of(inbox).to_lowercase());
    let domain = domain_of(inbox);
    addresses
        .iter()
        .map(AsRef::as_ref)
        .filter(|address| domain_of(address) == domain)
        .filter_map(|address| {
            local_of(address)
                .to_lowercase()
                .strip_prefix(&tagged)
                .map(str::to_owned)
        })
        .find(|tag| is_uuid(tag) || is_server_id(tag))
}

/// Whether a mail was delivered to this gateway's inbox, or to a reply tag of it. A provider account's
/// webhook fires for every mail the account receives, so a second team's gateway on the same account sees
/// this team's mail too; each gateway reads only what was addressed to it.
#[must_use]
pub fn addressed_to_inbox<S: AsRef<str>>(addresses: &[S], inbox: &str) -> bool {
    let local = local_of(inbox).to_lowercase();
    let tagged = format!("{local}+");
    let domain = domain_of(inbox);
    addresses.iter().map(AsRef::as_ref).any(|address| {
        let theirs = local_of(address).to_lowercase();
        domain_of(address) == domain && (theirs == local || theirs.starts_with(&tagged))
    })
}

/// Picks, among a project's mail gateways, the first whose inbox the mail was delivered to (or a reply tag
/// of it); `None` when the mail was for none of them. `inbox` reads a gateway's inbox.
#[must_use]
pub fn gateway_for_mail<'a, T, S: AsRef<str>>(
    gateways: &'a [T],
    to: &[S],
    inbox: impl Fn(&T) -> &str,
) -> Option<&'a T> {
    gateways
        .iter()
        .find(|gateway| addressed_to_inbox(to, inbox(gateway)))
}

/// Whether a mail's From address may write to the gateway: the list is missing or empty, so the gateway is
/// open; or it names the address; or it names the address's domain as `@domain` (that domain exactly, not a
/// subdomain). A From is unverified text, so this bars strangers; it authenticates no one.
#[must_use]
pub fn sender_allowed(from: &str, allowed: Option<&[String]>) -> bool {
    let list: Vec<String> = allowed
        .unwrap_or_default()
        .iter()
        .map(|entry| js::trim(entry).to_lowercase())
        .filter(|entry| !entry.is_empty())
        .collect();
    if list.is_empty() {
        return true;
    }
    let address = js::trim(from).to_lowercase();
    let domain = domain_of(&address);
    if domain.is_empty() || local_of(&address).is_empty() {
        return false;
    }
    let at_domain = format!("@{domain}");
    list.iter().any(|entry| *entry == address || *entry == at_domain)
}

/// How an argument named the session it resolved to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum How {
    /// By the session's id in full.
    Id,
    /// By the characters that end the session's id.
    Tail,
    /// By its member's label.
    Label,
    /// By the start of its member's principal reference.
    Principal,
}

impl How {
    /// The rule's name, as the TypeScript spelled it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Id => "id",
            Self::Tail => "tail",
            Self::Label => "label",
            Self::Principal => "principal",
        }
    }
}

/// The session an argument names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Recipient<'a> {
    /// The session.
    pub session: &'a SessionRecord,
    /// The rule that found it.
    pub how: How,
    /// The member the argument named, as it was written: set when `how` is a member rule.
    pub who: Option<String>,
    /// The rest of the member's sessions that could have taken the message, best first.
    pub others: Vec<&'a SessionRecord>,
}

/// One team's roster, for a gateway that serves several teams.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamSessions<'a> {
    /// The team's id.
    pub team_id: &'a str,
    /// The team's name.
    pub team_name: &'a str,
    /// The team's roster, its own gateway session left out.
    pub sessions: Vec<&'a SessionRecord>,
}

/// The session an argument names, and the team it is in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamRecipient<'a> {
    /// The session and how it was found.
    pub recipient: Recipient<'a>,
    /// The id of the session's team.
    pub team_id: &'a str,
    /// The name of the session's team.
    pub team_name: &'a str,
}

/// Finds the session an argument names, the first rule that matches winning: the id in full; a unique tail
/// of at least five characters, as `brigade send` does (`internal/harness/commands/send.go`); a member, by
/// the label the roster shows (case-insensitive, the whole string) or by the first eight or more characters
/// of their principal reference, as `brigade sessions --member` does. A member is answered with the best
/// of their sessions: the gateways and the sessions that refuse team messages left out, active before idle
/// before offline, the most recently seen among equals. A session's name is never an address. A label is
/// unverified text anyone can copy, so one that two members share is refused, not guessed at.
///
/// The lengths are counted in UTF-16 code units, as the TypeScript's `.length` counts them: a four-letter
/// label with an accent is still too short to be a tail.
///
/// # Errors
/// The sentence the person is told, when the argument names nothing or names too much.
pub fn resolve_recipient<'a>(
    arg: &str,
    sessions: impl IntoIterator<Item = &'a SessionRecord>,
) -> Result<Recipient<'a>, String> {
    let sessions: Vec<&SessionRecord> = sessions.into_iter().collect();
    let raw = js::trim(arg);
    let wanted = raw.to_lowercase();
    if wanted.is_empty() {
        return Err("no session or member was named".to_owned());
    }
    if let Some(exact) = sessions
        .iter()
        .copied()
        .find(|s| s.session_id.to_lowercase() == wanted)
    {
        return Ok(found(exact, How::Id));
    }
    if js::utf16_len(&wanted) >= 5 {
        let tails: Vec<&SessionRecord> = sessions
            .iter()
            .copied()
            .filter(|s| s.session_id.to_lowercase().ends_with(&wanted))
            .collect();
        if let [only] = tails[..] {
            return Ok(found(only, How::Tail));
        }
        if tails.len() > 1 {
            return Err(ambiguous_tail(&tails));
        }
    }
    let mut how = How::Label;
    let mut mine: Vec<&SessionRecord> = sessions
        .iter()
        .copied()
        .filter(|s| has_label(&wanted, s))
        .collect();
    if mine.is_empty() && is_principal_prefix(&wanted) {
        how = How::Principal;
        mine = sessions
            .iter()
            .copied()
            .filter(|s| s.principal_ref.to_lowercase().starts_with(&wanted))
            .collect();
    }
    if mine.is_empty() {
        return Err(nobody_named(raw));
    }
    let mut principals: Vec<&str> = Vec::new();
    for session in &mine {
        if !principals.contains(&session.principal_ref.as_str()) {
            principals.push(&session.principal_ref);
        }
    }
    if principals.len() > 1 {
        let prefixes: Vec<&str> = principals.iter().map(|p| js::utf16_prefix(p, 8)).collect();
        return Err(format!(
            "\"{raw}\" is the label of {} members (a label is unverified text; anyone can pick one); name one by the first eight characters of their principal reference: {}",
            principals.len(),
            prefixes.join(", ")
        ));
    }
    let mut eligible: Vec<&SessionRecord> = mine.into_iter().filter(|s| can_receive(s)).collect();
    eligible.sort_by(|x, y| by_suitability(x, y));
    let Some((best, others)) = eligible.split_first() else {
        return Err(format!("no session of {raw} can receive a message"));
    };
    Ok(Recipient {
        session: best,
        how,
        who: Some(raw.to_owned()),
        others: others.to_vec(),
    })
}

/// [`resolve_recipient`] over several teams' rosters, for a gateway that serves them all. A session id is
/// unique across the project, so the id and tail rules run on the union exactly as they run inside one
/// team. A label or a principal prefix names a member, and a member is per team (a credential is per team
/// per machine, so one person is one principal per team): when exactly one team has such a member, that
/// team's rules decide; when several do, the answer lists each team's candidates, the best three, by the
/// five characters that end their ids, which always go through. The gateway never picks a team for the
/// person.
///
/// # Errors
/// The sentence the person is told, when the argument names nothing or names too much.
pub fn resolve_recipient_across<'a>(
    arg: &str,
    teams: &[TeamSessions<'a>],
) -> Result<TeamRecipient<'a>, String> {
    let raw = js::trim(arg);
    let wanted = raw.to_lowercase();
    if wanted.is_empty() {
        return Err("no session or member was named".to_owned());
    }
    let all = || {
        teams
            .iter()
            .flat_map(|team| team.sessions.iter().map(move |session| (*session, team)))
    };
    let in_team = |(session, team): (&'a SessionRecord, &TeamSessions<'a>), how| TeamRecipient {
        recipient: found(session, how),
        team_id: team.team_id,
        team_name: team.team_name,
    };
    if let Some(exact) = all().find(|(s, _)| s.session_id.to_lowercase() == wanted) {
        return Ok(in_team(exact, How::Id));
    }
    if js::utf16_len(&wanted) >= 5 {
        let tails: Vec<_> = all()
            .filter(|(s, _)| s.session_id.to_lowercase().ends_with(&wanted))
            .collect();
        if let [only] = tails[..] {
            return Ok(in_team(only, How::Tail));
        }
        if tails.len() > 1 {
            let sessions: Vec<&SessionRecord> = tails.iter().map(|(s, _)| *s).collect();
            return Err(ambiguous_tail(&sessions));
        }
    }
    let matching: Vec<&TeamSessions<'a>> = teams
        .iter()
        .filter(|team| team.sessions.iter().any(|s| names_member(&wanted, s)))
        .collect();
    match matching[..] {
        [] => Err(nobody_named(raw)),
        [team] => resolve_recipient(raw, team.sessions.iter().copied()).map(|recipient| TeamRecipient {
            recipient,
            team_id: team.team_id,
            team_name: team.team_name,
        }),
        _ => {
            let lines: Vec<String> = matching
                .iter()
                .map(|team| {
                    let mut candidates: Vec<&SessionRecord> = team
                        .sessions
                        .iter()
                        .copied()
                        .filter(|s| names_member(&wanted, s) && can_receive(s))
                        .collect();
                    candidates.sort_by(|x, y| by_suitability(x, y));
                    let best: Vec<String> = candidates
                        .iter()
                        .take(3)
                        .map(|s| {
                            format!(
                                "{} {} ({})",
                                js::utf16_suffix(&s.session_id, 5),
                                s.session_name,
                                s.state
                            )
                        })
                        .collect();
                    let best = if best.is_empty() {
                        "no session can receive a message".to_owned()
                    } else {
                        best.join(", ")
                    };
                    format!("{}: {best}", team.team_name)
                })
                .collect();
            Err(format!(
                "\"{raw}\" is a member of {} teams; name one session by the five characters that end its id: {}",
                matching.len(),
                lines.join("; ")
            ))
        }
    }
}

fn found(session: &SessionRecord, how: How) -> Recipient<'_> {
    Recipient {
        session,
        how,
        who: None,
        others: Vec::new(),
    }
}

fn ambiguous_tail(tails: &[&SessionRecord]) -> String {
    let ids: Vec<&str> = tails.iter().map(|s| s.session_id.as_str()).collect();
    format!(
        "these characters end {} session ids; use one of them in full: {}",
        tails.len(),
        ids.join(", ")
    )
}

fn nobody_named(raw: &str) -> String {
    if js::utf16_len(raw) < 5 {
        format!(
            "\"{raw}\" is too short to name a session (give at least the last five characters of its id), and no member has that label"
        )
    } else {
        format!("no session ends with \"{raw}\", and no member has that label")
    }
}

/// Whether a lowercased argument is a session's member's label, the whole of it.
fn has_label(wanted: &str, session: &SessionRecord) -> bool {
    js::trim(session.human_label.as_deref().unwrap_or("")).to_lowercase() == wanted
}

/// Whether a lowercased argument can be the start of a principal reference: eight or more characters, all
/// of them hexadecimal digits or hyphens.
fn is_principal_prefix(wanted: &str) -> bool {
    wanted.len() >= 8
        && wanted
            .bytes()
            .all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f' | b'-'))
}

/// Whether a lowercased argument names a session's member: their label, the whole string, or the first
/// eight or more characters of their principal reference (the two member rules of [`resolve_recipient`]).
fn names_member(wanted: &str, session: &SessionRecord) -> bool {
    has_label(wanted, session)
        || (is_principal_prefix(wanted) && session.principal_ref.to_lowercase().starts_with(wanted))
}

/// Whether a session can be the one a member's message goes to: not a gateway, and not refusing.
fn can_receive(session: &SessionRecord) -> bool {
    !is_gateway_session(session) && session.inbound.as_deref() != Some("refuse")
}

/// Orders a member's sessions: by state, then the most recently seen first.
///
/// The TypeScript compared the two times with `localeCompare`, the runtime's collation; this compares them
/// character by character. The two agree on times written in one format, which is what a backend writes.
/// They differ where a time with a fraction of a second meets one without and the zone is written `+00:00`:
/// collation puts `.` before `+`, and the characters' own order puts `+` first.
fn by_suitability(x: &SessionRecord, y: &SessionRecord) -> Ordering {
    let rank = |session: &SessionRecord| match session.state.as_str() {
        "active" => 0,
        "idle" => 1,
        "offline" => 2,
        _ => 3,
    };
    rank(x).cmp(&rank(y)).then_with(|| {
        y.last_seen_at
            .as_deref()
            .unwrap_or("")
            .cmp(x.last_seen_at.as_deref().unwrap_or(""))
    })
}

/// Where the gateway found the session a mail is for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Source {
    /// A `to:` the person typed, as a header line or in a block.
    Block,
    /// The plus tag of the address the mail was delivered to.
    ReplyAddress,
    /// The mail's In-Reply-To and References headers.
    Headers,
    /// The block of the mail the person answered, quoted under their text.
    QuotedBlock,
}

impl Source {
    /// The source's name, as the TypeScript spelled it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Block => "block",
            Self::ReplyAddress => "reply-address",
            Self::Headers => "headers",
            Self::QuotedBlock => "quoted-block",
        }
    }
}

/// What a received mail is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Inbound {
    /// A command for the gateway itself.
    Command {
        /// The command, in lowercase.
        command: String,
        /// What the person typed under it.
        text: String,
    },
    /// A message for a session.
    Message {
        /// What names the session: for [`resolve_recipient`] when the person typed it, a session id when a
        /// thread or a quoted block gave it.
        target: String,
        /// The Brigade message this one answers. A `reply-to:` the person typed with nothing after it is
        /// the empty string, and nothing found later replaces it.
        reply_to: Option<String>,
        /// The summary the person gave.
        summary: Option<String>,
        /// What the person typed, without what their client quoted.
        text: String,
        /// Where the target was found.
        source: Source,
    },
    /// A mail that names no session and answers none.
    Unaddressed {
        /// What the person typed.
        text: String,
    },
    /// A mail whose directives could not be read.
    Invalid {
        /// Why, as the person is told.
        reason: String,
    },
}

/// Decides what a received mail is and where it goes.
#[must_use]
pub fn resolve_inbound(mail: &InboundMail, inbox: &str, threads: &dyn ThreadLookup) -> Inbound {
    let leading = match parse_leading(&mail.text) {
        Ok(leading) => leading,
        Err(error) => {
            return Inbound::Invalid {
                reason: error.to_string(),
            };
        }
    };
    let reply = strip_quoted(&leading.rest);
    let text = reply.body;
    let fields = leading.fields.clone().unwrap_or_default();
    if let Some(command) = filled(&fields, "command") {
        return Inbound::Command {
            command: js::trim(command).to_lowercase(),
            text,
        };
    }
    if leading.fields.is_none() {
        // A mail that says only `sessions` (or `help`), in its text or, with an empty text, in its subject.
        let bare = is_bare_command(&text).or_else(|| {
            js::trim(&text)
                .is_empty()
                .then(|| is_bare_command(&mail.subject))
                .flatten()
        });
        if let Some(command) = bare {
            return Inbound::Command {
                command: command.to_owned(),
                text,
            };
        }
    }

    // An empty `to:` names nobody, so the mail is resolved as if it had none.
    let mut target = filled(&fields, "to").map(str::to_owned);
    let mut reply_to = fields.get("reply-to").cloned();
    let summary = fields.get("summary").cloned();
    let mut source = Source::Block;

    if target.is_none()
        && let Some(row) = tag_of(&mail.to, inbox).and_then(|tag| threads.by_brigade_id(&tag))
        && row.direction == Direction::Out
    {
        target = Some(row.session_id);
        reply_to = reply_to.or(Some(row.brigade_message_id));
        source = Source::ReplyAddress;
    }
    if target.is_none() {
        let ids: Vec<String> = mail
            .in_reply_to
            .iter()
            .chain(&mail.references)
            .filter(|id| !id.is_empty())
            .cloned()
            .collect();
        // An id the gateway wrote itself names its message; with one in the headers, the others are not
        // looked up even when its message is not in the table.
        let row = match OWN_MESSAGE_ID.captures(&ids.join(" ")) {
            Some(own) => threads.by_brigade_id(&own[1]),
            None if ids.is_empty() => None,
            None => threads.by_mail_ids(&ids),
        };
        if let Some(row) = row.filter(|row| row.direction == Direction::Out) {
            target = Some(row.session_id);
            reply_to = reply_to.or(Some(row.brigade_message_id));
            source = Source::Headers;
        }
    }
    if target.is_none()
        && let Some(quoted) = last_block(&reply.quoted)
        && let Some(to) = filled(&quoted, "to")
    {
        target = Some(to.to_owned());
        reply_to = reply_to.or_else(|| quoted.get("reply-to").cloned());
        source = Source::QuotedBlock;
    }
    match target {
        Some(target) => Inbound::Message {
            target,
            reply_to,
            summary,
            text,
            source,
        },
        None => Inbound::Unaddressed { text },
    }
}

/// The body and the summary of the Brigade message a mail becomes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BrigadeBody {
    /// The body: who wrote, the text, the envelope block.
    pub body: String,
    /// The summary: the subject, or `email` for a mail without one.
    pub summary: String,
}

/// Renders a person's mail as the body of a Brigade message: a header line, the text (escaped so it cannot
/// forge a block), and the envelope block. The text is cut so that the whole fits the protocol's cap on a
/// body, which is counted in bytes of UTF-8; the summary is cut to the cap on a summary, in code points.
#[must_use]
pub fn brigade_body(mail: &InboundMail, text: &str) -> BrigadeBody {
    let who = match mail.from_name.as_deref().filter(|name| !name.is_empty()) {
        Some(name) => format!("{name} <{}>", mail.from),
        None => mail.from.clone(),
    };
    let subject = one_line(&mail.subject);
    let head = format!("Email from {who} (unverified), subject \"{subject}\":\n\n");
    let block = format!(
        "\n\n{}",
        render_block(&[
            ("via", Some("email")),
            ("from", Some(&mail.from)),
            ("subject", Some(&subject)),
            ("mail-id", mail.message_id.as_deref()),
            ("received", Some(&mail.received_at)),
        ])
    );
    let budget = MAX_BODY_BYTES.saturating_sub(head.len() + block.len());
    let text = truncate_utf8(&escape_block_lines(js::trim(text)), budget, MAIL_CUT_MARKER);
    let summary = match js::trim(&mail.subject) {
        "" => "email",
        subject => subject,
    };
    BrigadeBody {
        body: format!("{head}{text}{block}"),
        summary: cut_chars(summary, MAX_SUMMARY_CHARS),
    }
}

/// Where a session's message to the gateway goes, and what it says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Outbound {
    /// The address the mail goes to.
    pub address: String,
    /// The mail's subject, on one line.
    pub subject: String,
    /// The Message-ID of the person's mail this one answers.
    pub in_reply_to: Option<String>,
    /// The text of the mail: the body below its directives.
    pub text: String,
}

/// Reads a decision ask in the labelled shape `brigade decision` sent through 0.47.2 (a preamble line, a
/// `Decision <id>:` line, a "for …, asked …" line, then `options:` and `default:` lines) and returns it as
/// the command writes it now and a person reads it: the question, then the options as a list with the
/// default marked, then the answer-by date when the asker gave one. The preamble, the decision id and the
/// rest of the "for …, asked …" line are left out: the sender line and the thread already say who asks and
/// where the answer goes.
///
/// This is only a view of an older plugin's text, on Slack and in a mail alike, not a wire format or a
/// trusted message type: any other text, today's ask included, is `None` and is shown as it stands, and an
/// unrecognised line is never discarded. `escape` is applied to each piece of the asker's text.
#[must_use]
pub fn decision_view(text: &str, team: &str, escape: impl Fn(&str) -> String) -> Option<String> {
    let lines: Vec<&str> = text.split('\n').collect();
    let suffix = format!(
        " from team {team}. Answer however you wish; the session acts on your answer and closes the decision."
    );
    let to = lines[0]
        .strip_prefix("Brigade decision for ")?
        .strip_suffix(suffix.as_str())
        .filter(|to| !to.is_empty())?;
    let question = DECISION.captures(lines.get(1)?)?;
    let asked = lines
        .get(2)
        .filter(|line| line.starts_with(&format!("  for {to}, asked ")))?;
    let mut next = 3;
    let mut options: Vec<&str> = Vec::new();
    if let Some(list) = lines.get(next).and_then(|line| line.strip_prefix("  options: ")) {
        options = list.split(" | ").collect();
        next += 1;
    }
    let mut fallback = None;
    if let Some(option) = lines.get(next).and_then(|line| line.strip_prefix("  default: ")) {
        fallback = Some(option);
        next += 1;
    }
    if next != lines.len() {
        return None;
    }
    // The command refuses a default that is none of the options; one that is not is a shape this view does
    // not know.
    let marked = match fallback {
        Some(option) => Some(options.iter().position(|o| *o == option)?),
        None => None,
    };
    let mut parts = vec![escape(&question[1])];
    if !options.is_empty() {
        let list: Vec<String> = options
            .iter()
            .enumerate()
            .map(|(at, option)| {
                let mark = if Some(at) == marked { "[default] " } else { "" };
                format!("- {mark}{}", escape(option))
            })
            .collect();
        parts.push(list.join("\n"));
    }
    if let Some(by) = ANSWER_BY.captures(asked) {
        parts.push(format!("Answer by {}.", &by[1]));
    }
    Some(parts.join("\n\n"))
}

/// Decides where a session's message to the gateway goes: a `to:` directive in its body, or the thread of
/// the mail it replies to. A `to:` wins over a reply, and the mail it starts is a new one.
///
/// # Errors
/// The reason the sending session is told: directives that could not be read, a `to:` that is no address,
/// or no address at all.
pub fn resolve_outbound(
    env: &Envelope,
    threads: &dyn ThreadLookup,
    team_name: &str,
) -> Result<Outbound, String> {
    let leading = parse_leading(&env.body).map_err(|error: BlockError| error.to_string())?;
    let fields = leading.fields.unwrap_or_default();
    // An older plugin's decision ask is mailed as today's reads. Its summary carries the decision id, so
    // the subject is taken from the question instead.
    let ask = decision_view(js::trim(&leading.rest), team_name, str::to_owned);
    let about = match (&ask, env.summary.as_deref().filter(|summary| !summary.is_empty())) {
        (Some(ask), _) => ask.as_str(),
        (None, Some(summary)) => summary,
        (None, None) => leading.rest.as_str(),
    };
    let default_subject = || format!("[brigade/{team_name}] {}", excerpt(about, EXCERPT_CHARS));
    let subject_or_default =
        |subject: Option<&str>| match subject.map(js::trim).filter(|subject| !subject.is_empty()) {
            Some(subject) => subject.to_owned(),
            None => default_subject(),
        };
    if let Some(to) = filled(&fields, "to") {
        let address = js::trim(to).to_lowercase();
        if !is_address(&address) {
            return Err(format!("\"to: {to}\" is not an email address"));
        }
        let subject = subject_or_default(fields.get("subject").map(String::as_str));
        return Ok(Outbound {
            address,
            subject: one_line(&subject),
            in_reply_to: None,
            text: ask.clone().unwrap_or_else(|| leading.rest.clone()),
        });
    }
    if let Some(reply_to) = env.reply_to.as_deref().filter(|id| !id.is_empty())
        && let Some(row) = threads.by_brigade_id(reply_to)
        && row.direction == Direction::In
    {
        let base = subject_or_default(row.subject.as_deref());
        let is_reply = base
            .get(..3)
            .is_some_and(|start| start.eq_ignore_ascii_case("re:"));
        let subject = if is_reply { base } else { format!("Re: {base}") };
        return Ok(Outbound {
            address: row.address,
            subject: one_line(&subject),
            in_reply_to: row.mail_message_id,
            text: ask.clone().unwrap_or_else(|| leading.rest.clone()),
        });
    }
    Err(
        "no address: start the body with a line \"to: <email address>\", or reply (--reply-to) to a message that came from an email"
            .to_owned(),
    )
}

/// What a mail is rendered with: the team's name and its three addresses.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RenderContext {
    /// The team's name.
    pub team_name: String,
    /// The From of every mail the gateway sends.
    pub from: String,
    /// The provider's receiving address: where replies go (tagged) and what a mail must be addressed to.
    pub inbox: String,
    /// The address people are told to write to; the inbox itself unless the team routes a nicer one to it.
    pub public_address: String,
}

/// Writes the mail for a Brigade message: who wrote it, the text (escaped), the envelope block with the
/// reply pre-addressed, and how to reply. Threading rides on the Reply-To tag first (a reply names the
/// message it answers in its own To header, whatever the client did to the rest), then the mail headers,
/// then the quoted block.
#[must_use]
pub fn render_outbound(env: &Envelope, out: &Outbound, ctx: &RenderContext) -> OutboundMail {
    // The sender line is the session and its label, nothing else, as on Slack: no "unverified" marker in
    // what a person reads, and no team, which the subject and the block below carry. A sender without a
    // label is named by its session alone.
    let label = env
        .sender
        .human_label
        .as_deref()
        .filter(|label| !label.is_empty());
    let head = match label {
        Some(label) => format!("{} · {label}\n\n", env.sender.session_name),
        None => format!("{}\n\n", env.sender.session_name),
    };
    let hops = env.hop_count.to_string();
    let block = render_block(&[
        ("team", Some(&ctx.team_name)),
        ("message", Some(&env.message_id)),
        ("from-session", Some(&env.sender.session_id)),
        ("from-name", Some(&env.sender.session_name)),
        ("from-label", env.sender.human_label.as_deref()),
        ("from-principal", Some(&env.sender.principal_ref)),
        ("in-reply-to", env.reply_to.as_deref()),
        ("hops", Some(&hops)),
        ("sent", Some(&env.created_at)),
        ("to", Some(&env.sender.session_id)),
        ("reply-to", Some(&env.message_id)),
    ]);
    let tail = format!(
        "\n\nReply to this email to answer. To write to another session, send a new email to {} whose first line is \"to: <session>\".\n",
        ctx.public_address
    );
    let text = format!(
        "{head}{}\n\n{block}{tail}",
        escape_block_lines(js::trim(&out.text))
    );
    // No Message-ID header: a provider's relay replaces it with its own, so the thread table never knows
    // the id a reply's In-Reply-To will carry. The Reply-To tag is the key that survives; the two headers
    // below thread the mail under the person's own message in their client.
    let mut headers = vec![("Auto-Submitted".to_owned(), "auto-generated".to_owned())];
    if let Some(in_reply_to) = out.in_reply_to.as_deref().filter(|id| !id.is_empty()) {
        headers.push(("In-Reply-To".to_owned(), in_reply_to.to_owned()));
        headers.push(("References".to_owned(), in_reply_to.to_owned()));
    }
    OutboundMail {
        from: ctx.from.clone(),
        to: out.address.clone(),
        reply_to: Some(reply_tag(&ctx.inbox, &env.message_id)),
        subject: out.subject.clone(),
        text,
        headers,
    }
}

/// Whether a roster row is a gateway of any kind (a harness name starting `gateway-`: email, Slack, or a
/// self-hosted webhook gateway): a person cannot usefully write to one, so every roster shown to a person
/// leaves them out, and a member's best session is never one.
#[must_use]
pub fn is_gateway_session(session: &SessionRecord) -> bool {
    session
        .harness
        .as_deref()
        .is_some_and(|harness| harness.starts_with(GATEWAY_HARNESS_PREFIX))
}

/// Lists the sessions a person can write to by mail: the ones online now, the gateways left out. An offline
/// session can still be addressed (its mail waits for it), but a person is shown who is there.
#[must_use]
pub fn roster_text<'a>(
    team_name: &str,
    sessions: impl IntoIterator<Item = &'a SessionRecord>,
    gateway_session_id: &str,
    inbox: &str,
) -> String {
    let mut lines = vec![
        format!(
            "Sessions on Brigade team {team_name} right now (names and labels are their owners' own words):"
        ),
        String::new(),
    ];
    let mut listed = false;
    for session in sessions {
        if session.session_id == gateway_session_id
            || is_gateway_session(session)
            || session.state == "offline"
        {
            continue;
        }
        listed = true;
        let row = format!(
            "  {}  {}  {}  {}",
            js::utf16_suffix(&session.session_id, 5),
            session.session_name,
            session.state,
            session.human_label.as_deref().unwrap_or("")
        );
        lines.push(js::trim_end(&row).to_owned());
        if let Some(description) = session
            .session_description
            .as_deref()
            .filter(|line| !line.is_empty())
        {
            lines.push(format!("         ↳ {}", one_line(description)));
        }
    }
    if !listed {
        lines.push("  (none)".to_owned());
    }
    lines.extend([
        String::new(),
        format!("To write to one, send an email to {inbox} whose first line is:"),
        String::new(),
        "  to: <the five characters in the first column, or a member's label>".to_owned(),
        String::new(),
    ]);
    lines.join("\n")
}

/// A field that is there and not empty: what a JavaScript condition on a string asks.
fn filled<'a>(fields: &'a Fields, key: &str) -> Option<&'a str> {
    fields
        .get(key)
        .map(String::as_str)
        .filter(|value| !value.is_empty())
}

/// Whether `text` has the form of a UUID: 8, 4, 4, 4 and 12 hexadecimal digits between hyphens.
fn is_uuid(text: &str) -> bool {
    let bytes = text.as_bytes();
    bytes.len() == 36
        && bytes.iter().enumerate().all(|(at, byte)| match at {
            8 | 13 | 18 | 23 => *byte == b'-',
            _ => byte.is_ascii_hexdigit(),
        })
}

/// Whether `text` has the form of an id a Brigade server makes: thirty-two hexadecimal digits.
fn is_server_id(text: &str) -> bool {
    text.len() == 32 && text.bytes().all(|byte| byte.is_ascii_hexdigit())
}

#[cfg(test)]
mod tests {
    use super::{Direction, Inbound, SessionRecord, Source, ThreadLookup, ThreadRow, decision_view, is_uuid};
    use super::{is_server_id, tag_of};
    use super::{resolve_inbound, resolve_recipient};
    use crate::mail::InboundMail;

    struct Threads(Vec<ThreadRow>);

    impl ThreadLookup for Threads {
        fn by_brigade_id(&self, id: &str) -> Option<ThreadRow> {
            self.0.iter().find(|row| row.brigade_message_id == id).cloned()
        }

        fn by_mail_ids(&self, ids: &[String]) -> Option<ThreadRow> {
            self.0
                .iter()
                .find(|row| row.mail_message_id.as_ref().is_some_and(|id| ids.contains(id)))
                .cloned()
        }
    }

    const INBOX: &str = "a1b2c3d4@example.resend.app";
    const SESSION: &str = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
    const MESSAGE: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";

    fn mail(text: &str) -> InboundMail {
        InboundMail {
            provider_id: "re_1".to_owned(),
            from: "alice@example.com".to_owned(),
            from_name: None,
            to: vec![INBOX.to_owned()],
            subject: "s".to_owned(),
            message_id: None,
            in_reply_to: None,
            references: Vec::new(),
            text: text.to_owned(),
            received_at: "2026-10-03T16:00:00Z".to_owned(),
        }
    }

    fn out_row() -> ThreadRow {
        ThreadRow {
            brigade_message_id: MESSAGE.to_owned(),
            direction: Direction::Out,
            session_id: SESSION.to_owned(),
            address: "alice@example.com".to_owned(),
            mail_message_id: Some("<their@mail>".to_owned()),
            provider_mail_id: None,
            subject: None,
            kind: None,
            actor: None,
        }
    }

    #[test]
    fn a_uuid_has_its_hyphens_in_place_and_hexadecimal_digits_between() {
        assert!(is_uuid(MESSAGE));
        assert!(is_uuid("0F0F0F0F-0f0f-4f0f-8f0f-0f0f0f0f0f0f"));
        assert!(!is_uuid("0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0"));
        assert!(!is_uuid("0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0g"));
        assert!(!is_uuid("0f0f0f0f00f0f-4f0f-8f0f-0f0f0f0f0f0f"));
        assert!(!is_uuid("------------------------------------"));
    }

    #[test]
    fn a_reply_address_carries_a_servers_message_id_as_well_as_a_uuid() {
        const SERVER_ID: &str = "0f1e2d3c4b5a69788796a5b4c3d2e1f0";
        assert!(is_server_id(SERVER_ID));
        assert!(!is_server_id(&SERVER_ID[1..]));
        assert!(!is_server_id("0f1e2d3c4b5a69788796a5b4c3d2e1fg"));
        let inbox = "ops@in.example.com";
        assert_eq!(
            tag_of(
                &[format!("Ops+{}@in.example.com", SERVER_ID.to_uppercase())],
                inbox
            ),
            Some(SERVER_ID.to_owned())
        );
        assert_eq!(
            tag_of(&[format!("ops+{MESSAGE}@in.example.com")], inbox),
            Some(MESSAGE.to_owned())
        );
        // A tag that is neither, the inbox itself, and another domain carry no message id.
        assert_eq!(tag_of(&["ops+hello@in.example.com"], inbox), None);
        assert_eq!(tag_of(&["ops@in.example.com"], inbox), None);
        assert_eq!(
            tag_of(&[format!("ops+{SERVER_ID}@elsewhere.example.com")], inbox),
            None
        );
    }

    #[test]
    fn an_empty_to_names_nobody_and_an_empty_reply_to_is_kept() {
        let mut reply = mail("to:\nreply-to:\n\nhello");
        reply.to = vec![format!("a1b2c3d4+{MESSAGE}@example.resend.app")];
        let resolved = resolve_inbound(&reply, INBOX, &Threads(vec![out_row()]));
        assert_eq!(
            resolved,
            Inbound::Message {
                target: SESSION.to_owned(),
                reply_to: Some(String::new()),
                summary: None,
                text: "hello".to_owned(),
                source: Source::ReplyAddress,
            }
        );
        let nowhere = resolve_inbound(&mail("to:\n\nhello"), INBOX, &Threads(Vec::new()));
        assert_eq!(
            nowhere,
            Inbound::Unaddressed {
                text: "hello".to_owned()
            }
        );
    }

    #[test]
    fn a_reply_to_a_mail_that_came_in_is_not_a_thread_to_answer() {
        let mut row = out_row();
        row.direction = Direction::In;
        let mut reply = mail("hello");
        reply.to = vec![format!("a1b2c3d4+{MESSAGE}@example.resend.app")];
        reply.in_reply_to = Some("<their@mail>".to_owned());
        let resolved = resolve_inbound(&reply, INBOX, &Threads(vec![row]));
        assert_eq!(
            resolved,
            Inbound::Unaddressed {
                text: "hello".to_owned()
            }
        );
    }

    #[test]
    fn the_headers_find_a_thread_by_the_persons_own_message_id() {
        let mut reply = mail("hello");
        reply.references = vec![
            String::new(),
            "<other@mail>".to_owned(),
            "<their@mail>".to_owned(),
        ];
        let resolved = resolve_inbound(&reply, INBOX, &Threads(vec![out_row()]));
        assert_eq!(
            resolved,
            Inbound::Message {
                target: SESSION.to_owned(),
                reply_to: Some(MESSAGE.to_owned()),
                summary: None,
                text: "hello".to_owned(),
                source: Source::Headers,
            }
        );
        // An id of the gateway's own form is looked up alone, in the case it was written in, and the others
        // are not tried when it is not in the table.
        reply.in_reply_to = Some("<BRIGADE.0F0F0F0F-0f0f-4f0f-8f0f-0f0f0f0f0f0e@x>".to_owned());
        let resolved = resolve_inbound(&reply, INBOX, &Threads(vec![out_row()]));
        assert_eq!(
            resolved,
            Inbound::Unaddressed {
                text: "hello".to_owned()
            }
        );
    }

    #[test]
    fn a_decision_is_a_view_of_one_exact_shape() {
        let preamble = "Brigade decision for Alice from team brigade. Answer however you wish; the session acts on your answer and closes the decision.";
        let view = |rest: &str| {
            decision_view(&format!("{preamble}\n{rest}"), "brigade", |text| {
                format!("<{text}>")
            })
        };
        let asked = "  for Alice, asked 2026-10-29 by sam@example.com";
        assert_eq!(
            view(&format!("Decision d-20261029-a1b2c3: Ship?\n{asked}")).as_deref(),
            Some("<Ship?>")
        );
        assert_eq!(
            view(&format!(
                "Decision d-20261029-a1b2c3: Ship?\n{asked}, answer by 2026-10-31\n  options: a | b | a\n  default: a"
            ))
            .as_deref(),
            Some("<Ship?>\n\n- [default] <a>\n- <b>\n- <a>\n\nAnswer by 2026-10-31.")
        );
        assert_eq!(
            view(&format!(
                "Decision d-20261029-a1b2c3: Ship?\n{asked}\n  options: "
            ))
            .as_deref(),
            Some("<Ship?>\n\n- <>")
        );
        assert_eq!(
            view(&format!(
                "Decision d-20261029-a1b2c3: Ship?\n{asked}\n  default: a"
            )),
            None
        );
        assert_eq!(view(&format!("Decision d-20261029-A1B2C3: Ship?\n{asked}")), None);
        assert_eq!(view(&format!("Decision d-2026102-a1b2c3: Ship?\n{asked}")), None);
        assert_eq!(view(&format!("Decision d-20261029-a1b2c3: \n{asked}")), None);
        assert_eq!(
            view(&format!("Decision d-20261029-a1b2c3: Ship?\r\n{asked}")),
            None
        );
        assert_eq!(
            view("Decision d-20261029-a1b2c3: Ship?\n  for Bob, asked 2026-10-29"),
            None
        );
        assert_eq!(view("Decision d-20261029-a1b2c3: Ship?"), None);
        assert_eq!(decision_view(preamble, "brigade", str::to_owned), None);
        // The preamble with nobody between its two halves names nobody.
        let nobody = "Brigade decision for from team brigade. Answer however you wish; the session acts on your answer and closes the decision.";
        assert_eq!(
            decision_view(
                &format!("{nobody}\nDecision d-20261029-a1b2c3: Ship?\n  for , asked x"),
                "brigade",
                str::to_owned
            ),
            None
        );
    }

    #[test]
    fn a_length_in_a_recipient_rule_is_in_utf16_code_units() {
        let sessions = [SessionRecord {
            session_id: SESSION.to_owned(),
            session_name: "frank".to_owned(),
            principal_ref: "p1".to_owned(),
            state: "idle".to_owned(),
            ..SessionRecord::default()
        }];
        let error = |arg: &str| resolve_recipient(arg, &sessions).unwrap_err();
        // Four letters, five bytes: too short.
        assert!(error("José").contains("too short"));
        // Three characters, five code units: long enough to be a tail.
        assert_eq!(
            error("a🙂🙂"),
            "no session ends with \"a🙂🙂\", and no member has that label"
        );
    }
}
