//! The requests of the contract's section 2, read and checked.
//!
//! The checks are BAP/1's `Validate` rules, in its order, with its `details`: the bundled adapter runs them
//! before it sends, and the contract says a server never relies on that.

use crate::error::Error;
use crate::json::Document;
use crate::limits;

/// The fixed, case-sensitive prefix of a join secret (BAP/1 4.4.10).
pub const JOIN_SECRET_PREFIX: &str = "brg1.";

/// A parsed `brg1.<team_ref>.<secret>`. It prints without its secret half.
#[derive(Clone, PartialEq, Eq)]
pub struct JoinSecret {
    team_ref: String,
    whole: String,
}

impl JoinSecret {
    /// Parses a join secret. The team reference is everything between the prefix and the last `.`, because
    /// the protocol does not freeze its shape.
    ///
    /// # Errors
    /// `invalid_input` naming `join_secret`, with no part of the input in it.
    pub fn parse(text: &str) -> Result<Self, Error> {
        let rest = text
            .strip_prefix(JOIN_SECRET_PREFIX)
            .ok_or_else(Error::malformed_join_secret)?;
        let (team_ref, secret) = rest.rsplit_once('.').ok_or_else(Error::malformed_join_secret)?;
        if team_ref.is_empty() || secret.is_empty() || !clean(team_ref) || !clean(secret) {
            return Err(Error::malformed_join_secret());
        }
        Ok(Self {
            team_ref: team_ref.to_owned(),
            whole: text.to_owned(),
        })
    }

    /// The team the secret names. It is not secret: it appears in ordinary results.
    #[must_use]
    pub fn team_ref(&self) -> &str {
        &self.team_ref
    }

    /// The whole secret, for the one use it has: its hash.
    #[must_use]
    pub fn expose(&self) -> &str {
        &self.whole
    }
}

impl std::fmt::Debug for JoinSecret {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(formatter, "{JOIN_SECRET_PREFIX}{}.[redacted]", self.team_ref)
    }
}

/// Whether a component of a join secret is free of whitespace, control characters and format characters:
/// the usual paste accidents, and what could forge the structure of a log line.
fn clean(component: &str) -> bool {
    !component
        .chars()
        .any(|c| c.is_whitespace() || c.is_control() || is_format(c))
}

/// Whether `c` is in Unicode's general category Cf.
fn is_format(c: char) -> bool {
    matches!(
        u32::from(c),
        0x00AD
            | 0x0600..=0x0605
            | 0x061C
            | 0x06DD
            | 0x070F
            | 0x0890..=0x0891
            | 0x08E2
            | 0x180E
            | 0x200B..=0x200F
            | 0x202A..=0x202E
            | 0x2060..=0x2064
            | 0x2066..=0x206F
            | 0xFEFF
            | 0xFFF9..=0xFFFB
            | 0x110BD
            | 0x110CD
            | 0x13430..=0x1343F
            | 0x1BCA0..=0x1BCA3
            | 0x1D173..=0x1D17A
            | 0xE0001
            | 0xE0020..=0xE007F
    )
}

fn cap_chars(field: &str, value: &str, limit: usize) -> Result<(), Error> {
    let count = value.chars().count();
    if count > limit {
        Err(Error::too_long(field, limit, count, "codepoints"))
    } else {
        Ok(())
    }
}

fn cap_bytes(field: &str, value: &str, limit: usize) -> Result<(), Error> {
    if value.len() > limit {
        Err(Error::too_long(field, limit, value.len(), "bytes"))
    } else {
        Ok(())
    }
}

fn one_of(field: &str, value: &str, allowed: &[&str]) -> Result<(), Error> {
    if allowed.contains(&value) {
        Ok(())
    } else {
        Err(Error::invalid_value(field, &allowed.join(", ")))
    }
}

/// An optional text member within a code-point cap.
fn optional_text(document: &Document, name: &str, limit: usize) -> Result<Option<String>, Error> {
    let Some(value) = document.text(name)? else {
        return Ok(None);
    };
    cap_chars(name, &value, limit)?;
    Ok(Some(value))
}

/// A lease in the range every version 1 server accepts (the contract's section 5).
fn lease_seconds(document: &Document) -> Result<Option<i64>, Error> {
    let Some(seconds) = document.integer("lease_seconds")? else {
        return Ok(None);
    };
    if !(limits::LEASE_MIN_SECONDS..=limits::LEASE_MAX_SECONDS).contains(&seconds) {
        return Err(Error::out_of_range(
            "lease_seconds",
            limits::LEASE_MIN_SECONDS,
            limits::LEASE_MAX_SECONDS,
        ));
    }
    Ok(Some(seconds))
}

fn context_used_tokens(document: &Document) -> Result<Option<i64>, Error> {
    let Some(tokens) = document.integer("context_used_tokens")? else {
        return Ok(None);
    };
    if !(0..=limits::MAX_CONTEXT_USED_TOKENS).contains(&tokens) {
        return Err(Error::out_of_range(
            "context_used_tokens",
            0,
            limits::MAX_CONTEXT_USED_TOKENS,
        ));
    }
    Ok(Some(tokens))
}

/// The rule a team's name is held to, whoever names the team: a `/v1/team/create`, and the operator who
/// takes in a team that exists elsewhere. The name is there, and within its cap in code points.
///
/// # Errors
/// `invalid_input` naming `team_name`, when the name is empty or over the cap.
pub fn check_team_name(team_name: &str) -> Result<(), Error> {
    if team_name.is_empty() {
        return Err(Error::required("team_name"));
    }
    cap_chars("team_name", team_name, limits::MAX_TEAM_NAME_CODEPOINTS)
}

const ACTIVITIES: &[&str] = &["busy", "idle"];
const INBOUND_POLICIES: &[&str] = &["accept", "hold", "refuse"];

/// `/v1/team/create`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamCreate {
    /// The team's display name.
    pub team_name: String,
    /// The creator's label; empty when the request gave none.
    pub human_label: String,
}

/// `/v1/team/join`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamJoin {
    /// The secret the joiner presents.
    pub join_secret: JoinSecret,
    /// The joiner's label; empty when the request gave none.
    pub human_label: String,
}

/// Whom a `/v1/team/revoke-member` revokes: the two forms of BAP/1 4.4.10.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Revocation {
    /// One member, by its principal.
    Principal {
        /// The principal whose membership ends.
        principal_ref: String,
        /// Whether its later join is refused as well, until a revocation without a ban lifts that.
        ban: bool,
    },
    /// Every active member that joined with a secret of this version or a lower one, the owner excepted.
    UpToVersion(u32),
}

/// `/v1/team/revoke-member`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamRevoke {
    /// The team.
    pub team_ref: String,
    /// Whom the request revokes.
    pub whom: Revocation,
}

/// `/v1/team/transfer`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TeamTransfer {
    /// The team.
    pub team_ref: String,
    /// The principal that is to own it.
    pub principal_ref: String,
}

/// A session's facts as a registration reports them (BAP/1 4.4.2).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Registration {
    /// The team the session registers in.
    pub team_ref: String,
    /// The registering harness.
    pub harness: String,
    /// The harness's version.
    pub harness_version: String,
    /// The session's display name.
    pub session_name: String,
    /// The session's own claim about its work.
    pub session_description: Option<String>,
    /// `busy` or `idle`.
    pub activity: String,
    /// `accept`, `hold` or `refuse`.
    pub inbound: String,
    /// The lease asked for; `None` is the default.
    pub lease_seconds: Option<i64>,
    /// The workspace's label.
    pub workspace_label: Option<String>,
    /// The harness's default label for its principal.
    pub human_label: Option<String>,
    /// The model identity the harness reports.
    pub model: Option<String>,
    /// The harness's count of the tokens its context holds.
    pub context_used_tokens: Option<i64>,
    /// The version of the Brigade harness.
    pub brigade_version: Option<String>,
    /// The descriptor of the session's folder-sync adapter.
    pub sync_peer: Option<String>,
    /// The session to re-open, when the registration resumes one.
    pub resume: Option<String>,
}

/// What a heartbeat changes. Absent means unchanged (BAP/1 4.4.4).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HeartbeatChanges {
    /// `busy` or `idle`.
    pub activity: Option<String>,
    /// A new display name.
    pub session_name: Option<String>,
    /// A new description; an empty one is a value like any other.
    pub session_description: Option<String>,
    /// `accept`, `hold` or `refuse`.
    pub inbound: Option<String>,
    /// A new lease.
    pub lease_seconds: Option<i64>,
    /// The model identity.
    pub model: Option<String>,
    /// The context's token count.
    pub context_used_tokens: Option<i64>,
    /// The Brigade harness's version.
    pub brigade_version: Option<String>,
    /// The sync adapter's descriptor.
    pub sync_peer: Option<String>,
}

/// `/v1/session/heartbeat`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Heartbeat {
    /// The session to renew.
    pub session_id: String,
    /// What the heartbeat changes.
    pub changes: HeartbeatChanges,
}

/// `/v1/session/list`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionList {
    /// The team whose sessions are listed.
    pub team_ref: String,
    /// The session to mark `is_self`.
    pub session_id: Option<String>,
    /// Whether offline sessions are listed too.
    pub include_offline: bool,
}

/// `/v1/message/send` (BAP/1 4.4.6).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Send {
    /// The session that sends.
    pub sender_session_id: String,
    /// The session that receives.
    pub recipient_session_id: String,
    /// The message's text.
    pub body: String,
    /// The message's one-line summary; empty when the request gave none.
    pub summary: String,
    /// The message this one answers; empty when the request gave none.
    pub reply_to: String,
    /// The sender's idempotency key; empty when the request gave none.
    pub idempotency_key: String,
}

/// `/v1/message/receive` (the contract's section 4).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Receive {
    /// The session whose inbox is read.
    pub session_id: String,
    /// The most messages to return.
    pub limit: usize,
    /// A cursor from an earlier result.
    pub after: Option<String>,
    /// How long to hold the request while the answer would be empty.
    pub wait_seconds: i64,
}

/// `/v1/message/ack` (BAP/1 4.4.8).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ack {
    /// The session that acknowledges.
    pub session_id: String,
    /// The messages it acknowledges.
    pub message_ids: Vec<String>,
}

/// One request of the contract's section 2.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Command {
    /// `/v1/team/create`.
    TeamCreate(TeamCreate),
    /// `/v1/team/join`.
    TeamJoin(TeamJoin),
    /// `/v1/team/leave`, with the team to leave.
    TeamLeave(String),
    /// `/v1/team/members`, with the team to list.
    TeamMembers(String),
    /// `/v1/team/rotate-secret`, with the team whose secret is replaced.
    TeamRotateSecret(String),
    /// `/v1/team/revoke-member`.
    TeamRevokeMember(TeamRevoke),
    /// `/v1/team/transfer`.
    TeamTransfer(TeamTransfer),
    /// `/v1/session/register`.
    SessionRegister(Registration),
    /// `/v1/session/heartbeat`.
    SessionHeartbeat(Heartbeat),
    /// `/v1/session/list`.
    SessionList(SessionList),
    /// `/v1/session/close`, with the session to close.
    SessionClose(String),
    /// `/v1/message/send`.
    MessageSend(Send),
    /// `/v1/message/receive`.
    MessageReceive(Receive),
    /// `/v1/message/ack`.
    MessageAck(Ack),
    /// `/v1/credential/revoke`.
    CredentialRevoke,
}

impl Command {
    /// Reads the request at `path` with body `body`.
    ///
    /// # Errors
    /// `not_found` for a path the contract does not have, `invalid_input` for a body that fails its shape.
    pub fn parse(path: &str, body: &[u8]) -> Result<Self, Error> {
        if !Self::known_path(path) {
            return Err(Error::no_such_endpoint());
        }
        if body.len() > limits::MAX_REQUEST_BYTES {
            return Err(Error::body_too_long());
        }
        Self::read(path, &Document::parse(body)?)
    }

    /// Reads the request at `path` from a body that is already known to be one JSON object.
    ///
    /// The members are checked in the order the stand-in server checks them
    /// (`internal/adapters/rest/stub`), so that a request with two things wrong is told the same one by
    /// either: the members the contract adds first, then the lease, then BAP/1's shape in its own order.
    ///
    /// # Errors
    /// `not_found` for a path the contract does not have, `invalid_input` for a body that fails its shape.
    pub fn read(path: &str, document: &Document) -> Result<Self, Error> {
        match path {
            "/v1/team/create" => team_create(document),
            "/v1/team/join" => team_join(document),
            "/v1/team/leave" => Ok(Self::TeamLeave(document.named_required_text("team_ref")?)),
            "/v1/team/members" => Ok(Self::TeamMembers(document.named_required_text("team_ref")?)),
            "/v1/team/rotate-secret" => Ok(Self::TeamRotateSecret(document.named_required_text("team_ref")?)),
            "/v1/team/revoke-member" => team_revoke_member(document),
            "/v1/team/transfer" => team_transfer(document),
            "/v1/session/register" => session_register(document),
            "/v1/session/heartbeat" => session_heartbeat(document),
            "/v1/session/list" => session_list(document),
            "/v1/session/close" => Ok(Self::SessionClose(document.named_required_text("session_id")?)),
            "/v1/message/send" => message_send(document),
            "/v1/message/receive" => message_receive(document),
            "/v1/message/ack" => message_ack(document),
            "/v1/credential/revoke" => Ok(Self::CredentialRevoke),
            _ => Err(Error::no_such_endpoint()),
        }
    }

    /// Whether `path` is one of the contract's section 2 that carries a command.
    #[must_use]
    pub fn known_path(path: &str) -> bool {
        matches!(
            path,
            "/v1/team/create"
                | "/v1/team/join"
                | "/v1/team/leave"
                | "/v1/team/members"
                | "/v1/team/rotate-secret"
                | "/v1/team/revoke-member"
                | "/v1/team/transfer"
                | "/v1/session/register"
                | "/v1/session/heartbeat"
                | "/v1/session/list"
                | "/v1/session/close"
                | "/v1/message/send"
                | "/v1/message/receive"
                | "/v1/message/ack"
                | "/v1/credential/revoke"
        )
    }

    /// Whether the request at `path` is answered without a known credential: the two that can create a
    /// principal.
    #[must_use]
    pub fn open_path(path: &str) -> bool {
        matches!(path, "/v1/team/create" | "/v1/team/join")
    }

    /// Whether the request at `path` names its team in a `team_ref` whose membership is checked before the
    /// rest of the request is read.
    #[must_use]
    pub fn team_scoped_path(path: &str) -> bool {
        matches!(
            path,
            "/v1/team/members" | "/v1/session/register" | "/v1/session/list"
        )
    }

    /// Whether the request at `path` is one of the three that only a team's owner is answered (the
    /// contract's section 3a). It names its team in a `team_ref`, and that the caller owns the team is
    /// checked before the rest of the request is read: a caller that is not the owner learns nothing from
    /// what else the request held.
    #[must_use]
    pub fn administration_path(path: &str) -> bool {
        matches!(
            path,
            "/v1/team/rotate-secret" | "/v1/team/revoke-member" | "/v1/team/transfer"
        )
    }
}

fn team_create(document: &Document) -> Result<Command, Error> {
    // An absent name is an empty one, and the rule refuses both in the same words.
    let team_name = document.text("team_name")?.unwrap_or_default();
    check_team_name(&team_name)?;
    let human_label =
        optional_text(document, "human_label", limits::MAX_HUMAN_LABEL_CHARS)?.unwrap_or_default();
    Ok(Command::TeamCreate(TeamCreate {
        team_name,
        human_label,
    }))
}

fn team_join(document: &Document) -> Result<Command, Error> {
    let secret = document.required_text("join_secret")?;
    let human_label =
        optional_text(document, "human_label", limits::MAX_HUMAN_LABEL_CHARS)?.unwrap_or_default();
    // `backend` is the adapter's own and never the server's, but a request that carries one that is not an
    // object, `null` included, is malformed wherever it is read (BAP/1 4.4.10).
    if document.is_object("backend") == Some(false) {
        return Err(Error::wrong_type("backend", "a JSON object"));
    }
    Ok(Command::TeamJoin(TeamJoin {
        join_secret: JoinSecret::parse(&secret)?,
        human_label,
    }))
}

/// The two forms of a revocation, read in the order the adapter checks them in: both forms given, then a
/// ban beside a version, then the version's range, then neither form.
fn team_revoke_member(document: &Document) -> Result<Command, Error> {
    const VERSION: &str = "joined_secret_version_lte";
    let team_ref = document.named_required_text("team_ref")?;
    // An empty principal is an absent one, as it is to the adapter.
    let principal_ref = document
        .named_text("principal_ref")?
        .filter(|principal_ref| !principal_ref.is_empty());
    let ban = document.named_boolean("ban")?.unwrap_or(false);
    let whom = match (principal_ref, document.has_value(VERSION)) {
        (Some(_), true) => {
            return Err(Error::refused_value(
                "principal_ref",
                "given without joined_secret_version_lte: they are two forms of one request",
            ));
        }
        (Some(principal_ref), false) => Revocation::Principal { principal_ref, ban },
        (None, true) => {
            // A revocation by version is for the members that are to come back with the new secret, and
            // a ban would keep them out.
            if ban {
                return Err(Error::refused_value(
                    "ban",
                    "given with principal_ref only: a revocation by version never bans",
                ));
            }
            let max_version = document.named_integer(VERSION, 0, 0, limits::MAX_SECRET_VERSION)?;
            Revocation::UpToVersion(u32::try_from(max_version).unwrap_or(u32::MAX))
        }
        (None, false) => return Err(Error::required("principal_ref")),
    };
    Ok(Command::TeamRevokeMember(TeamRevoke { team_ref, whom }))
}

fn team_transfer(document: &Document) -> Result<Command, Error> {
    Ok(Command::TeamTransfer(TeamTransfer {
        team_ref: document.named_required_text("team_ref")?,
        principal_ref: document.named_required_text("principal_ref")?,
    }))
}

fn session_register(document: &Document) -> Result<Command, Error> {
    let team_ref = document.named_required_text("team_ref")?;
    let lease_seconds = lease_seconds(document)?;
    let harness = document.required_text("harness")?;
    cap_chars("harness", &harness, limits::MAX_HARNESS_CHARS)?;
    let harness_version = document.required_text("harness_version")?;
    cap_chars("harness_version", &harness_version, limits::MAX_HARNESS_CHARS)?;
    let session_name = document.required_text("session_name")?;
    cap_chars("session_name", &session_name, limits::MAX_SESSION_NAME_CODEPOINTS)?;
    let session_description = optional_text(document, "session_description", limits::MAX_DESCRIPTION_CHARS)?;
    let activity = document.text("activity")?.unwrap_or_default();
    one_of("activity", &activity, ACTIVITIES)?;
    let inbound = document.text("inbound")?.unwrap_or_default();
    one_of("inbound", &inbound, INBOUND_POLICIES)?;
    let workspace_label = optional_text(document, "workspace_label", limits::MAX_WORKSPACE_LABEL_CHARS)?;
    let human_label = optional_text(document, "human_label", limits::MAX_HUMAN_LABEL_CHARS)?;
    let model = optional_text(document, "model", limits::MAX_MODEL_CHARS)?;
    let context_used_tokens = context_used_tokens(document)?;
    let brigade_version = optional_text(document, "brigade_version", limits::MAX_BRIGADE_VERSION_CHARS)?;
    let sync_peer = optional_text(document, "sync_peer", limits::MAX_SYNC_PEER_CHARS)?;
    let resume = match document.object("resume")? {
        Some(resume) => Some(resume.required_text("session_id")?),
        // A value that is not an object is a document that does not read as a registration.
        None if document.has_value("resume") => return Err(Error::malformed_json()),
        None => None,
    };
    Ok(Command::SessionRegister(Registration {
        team_ref,
        harness,
        harness_version,
        session_name,
        session_description,
        activity,
        inbound,
        lease_seconds,
        workspace_label,
        human_label,
        model,
        context_used_tokens,
        brigade_version,
        sync_peer,
        resume,
    }))
}

fn session_heartbeat(document: &Document) -> Result<Command, Error> {
    let session_id = document.named_required_text("session_id")?;
    let heartbeat = document
        .object("heartbeat")?
        .ok_or_else(|| Error::required("heartbeat"))?;
    let lease_seconds = lease_seconds(&heartbeat)?;
    let activity = heartbeat.text("activity")?;
    if let Some(activity) = &activity {
        one_of("activity", activity, ACTIVITIES)?;
    }
    // A name that is present must be a name: an empty one is refused, where an empty description is a value.
    let session_name = heartbeat.text("session_name")?;
    if let Some(name) = &session_name {
        if name.is_empty() {
            return Err(Error::required("session_name"));
        }
        cap_chars("session_name", name, limits::MAX_SESSION_NAME_CODEPOINTS)?;
    }
    let session_description =
        optional_text(&heartbeat, "session_description", limits::MAX_DESCRIPTION_CHARS)?;
    let inbound = heartbeat.text("inbound")?;
    if let Some(inbound) = &inbound {
        one_of("inbound", inbound, INBOUND_POLICIES)?;
    }
    let model = optional_text(&heartbeat, "model", limits::MAX_MODEL_CHARS)?;
    let brigade_version = optional_text(&heartbeat, "brigade_version", limits::MAX_BRIGADE_VERSION_CHARS)?;
    let sync_peer = optional_text(&heartbeat, "sync_peer", limits::MAX_SYNC_PEER_CHARS)?;
    let context_used_tokens = context_used_tokens(&heartbeat)?;
    Ok(Command::SessionHeartbeat(Heartbeat {
        session_id,
        changes: HeartbeatChanges {
            activity,
            session_name,
            session_description,
            inbound,
            lease_seconds,
            model,
            context_used_tokens,
            brigade_version,
            sync_peer,
        },
    }))
}

fn session_list(document: &Document) -> Result<Command, Error> {
    Ok(Command::SessionList(SessionList {
        team_ref: document.named_required_text("team_ref")?,
        session_id: document.named_text("session_id")?.filter(|id| !id.is_empty()),
        include_offline: document.named_boolean("include_offline")?.unwrap_or(false),
    }))
}

fn message_send(document: &Document) -> Result<Command, Error> {
    // The members are read first, so a document that does not read is told so before any rule is applied.
    let sender_session_id = document.text("sender_session_id")?.unwrap_or_default();
    let recipient_session_id = document.text("recipient_session_id")?.unwrap_or_default();
    let body = document.text("body")?.unwrap_or_default();
    let summary = document.text("summary")?.unwrap_or_default();
    let reply_to = document.text("reply_to")?.unwrap_or_default();
    let idempotency_key = document.text("idempotency_key")?.unwrap_or_default();
    // The one shape where loose parsing has a named exception: a member the server stamps is refused with
    // any value, null included, because ignoring a forged sender would hide a client's bug (BAP/1 4.4.6).
    for forbidden in [
        "sender",
        "principal_ref",
        "human_label",
        "team_ref",
        "created_at",
        "hop_count",
    ] {
        if document.present(forbidden) {
            return Err(Error::forbidden_member(forbidden));
        }
    }
    for (name, value) in [
        ("sender_session_id", &sender_session_id),
        ("recipient_session_id", &recipient_session_id),
        ("body", &body),
    ] {
        if value.is_empty() {
            return Err(Error::required(name));
        }
    }
    cap_bytes("body", &body, limits::MAX_BODY_BYTES)?;
    cap_chars("summary", &summary, limits::MAX_SUMMARY_CHARS)?;
    cap_chars(
        "idempotency_key",
        &idempotency_key,
        limits::MAX_IDEMPOTENCY_KEY_CHARS,
    )?;
    Ok(Command::MessageSend(Send {
        sender_session_id,
        recipient_session_id,
        body,
        summary,
        reply_to,
        idempotency_key,
    }))
}

fn message_receive(document: &Document) -> Result<Command, Error> {
    let session_id = document.named_required_text("session_id")?;
    let default = limits::RECEIVE_DEFAULT_LIMIT as i64;
    let limit = document.named_integer("limit", default, 1, limits::RECEIVE_MAX_LIMIT as i64)?;
    let wait_seconds = document.named_integer("wait_seconds", 0, 0, limits::RECEIVE_MAX_WAIT_SECONDS)?;
    let after = document.named_text("after")?.filter(|cursor| !cursor.is_empty());
    Ok(Command::MessageReceive(Receive {
        session_id,
        limit: usize::try_from(limit).unwrap_or(limits::RECEIVE_DEFAULT_LIMIT),
        after,
        wait_seconds,
    }))
}

fn message_ack(document: &Document) -> Result<Command, Error> {
    Ok(Command::MessageAck(Ack {
        session_id: document.named_required_text("session_id")?,
        message_ids: document.required_ids("message_ids")?,
    }))
}

#[cfg(test)]
mod tests {
    use super::{Command, JoinSecret, Revocation, TeamRevoke, TeamTransfer};
    use crate::error::{Code, Error};

    fn refused(path: &str, body: &str) -> Error {
        Command::parse(path, body.as_bytes()).unwrap_err()
    }

    fn field(error: &Error) -> &str {
        error.details.get("field").map_or("", String::as_str)
    }

    fn reason(error: &Error) -> &str {
        error.details.get("reason").map_or("", String::as_str)
    }

    const REGISTRATION: &str = r#""harness":"claude-code","harness_version":"2.1","session_name":"s","activity":"busy","inbound":"accept","team_ref":"t""#;

    #[test]
    fn an_unknown_path_is_not_found_whatever_the_body() {
        let error = refused("/v1/nope", "not json");
        assert_eq!(error.code, Code::NotFound);
        assert_eq!(error.message, "no such endpoint");
    }

    #[test]
    fn a_body_over_the_cap_is_refused_before_it_is_parsed() {
        let body = format!("{{\"team_name\":\"{}\"}}", "x".repeat(1 << 20));
        let error = refused("/v1/team/create", &body);
        assert_eq!((field(&error), reason(&error)), ("body", "too_long"));
    }

    #[test]
    fn a_team_name_is_capped_in_code_points_not_bytes() {
        // Sixty-four two-byte characters are 128 bytes and exactly at the cap.
        let at_cap = format!("{{\"team_name\":\"{}\"}}", "é".repeat(64));
        assert!(Command::parse("/v1/team/create", at_cap.as_bytes()).is_ok());
        let over = format!("{{\"team_name\":\"{}\"}}", "é".repeat(65));
        let error = refused("/v1/team/create", &over);
        assert_eq!((field(&error), reason(&error)), ("team_name", "too_long"));
        assert_eq!(error.details["unit"], "codepoints");
        assert_eq!(error.details["actual"], "65");
        assert_eq!(field(&refused("/v1/team/create", "{}")), "team_name");
    }

    #[test]
    fn a_join_secret_is_parsed_at_the_last_dot_and_never_printed() {
        let secret = JoinSecret::parse("brg1.team.with.dots.s3cret").unwrap();
        assert_eq!(secret.team_ref(), "team.with.dots");
        assert_eq!(secret.expose(), "brg1.team.with.dots.s3cret");
        assert_eq!(format!("{secret:?}"), "brg1.team.with.dots.[redacted]");
    }

    #[test]
    fn a_malformed_join_secret_is_refused_without_echoing_it() {
        for bad in [
            "",
            "brg2.t.s",
            "brg1.t",
            "brg1..s",
            "brg1.t.",
            "brg1.t t.s",
            "brg1.t.s\n",
            "brg1.t.\u{200b}s",
            "BRG1.t.s",
        ] {
            let Err(error) = JoinSecret::parse(bad) else {
                panic!("accepted {bad:?}");
            };
            assert_eq!((field(&error), reason(&error)), ("join_secret", "invalid_value"));
            assert!(bad.is_empty() || !error.message.contains(bad));
        }
        // A `backend` that is not an object is refused, and `null` is not an object.
        for backend in ["\"x\"", "null", "[]"] {
            let body = format!("{{\"join_secret\":\"brg1.t.s\",\"backend\":{backend}}}");
            assert_eq!(field(&refused("/v1/team/join", &body)), "backend", "{backend}");
        }
        assert!(
            Command::parse(
                "/v1/team/join",
                br#"{"join_secret":"brg1.t.s","backend":{"url":"x"}}"#
            )
            .is_ok()
        );
    }

    #[test]
    fn a_registration_is_checked_member_by_member() {
        let ok = format!(
            "{{{REGISTRATION},\"lease_seconds\":30,\"resume\":{{\"session_id\":\"r\"}},\"unknown\":1}}"
        );
        let Command::SessionRegister(registration) =
            Command::parse("/v1/session/register", ok.as_bytes()).unwrap()
        else {
            panic!("not a registration");
        };
        assert_eq!(registration.lease_seconds, Some(30));
        assert_eq!(registration.resume.as_deref(), Some("r"));
        assert_eq!(registration.session_description, None);

        for (extra, named) in [
            ("\"lease_seconds\":29", "lease_seconds"),
            ("\"lease_seconds\":601", "lease_seconds"),
            ("\"context_used_tokens\":-1", "context_used_tokens"),
            ("\"context_used_tokens\":9007199254740992", "context_used_tokens"),
            ("\"resume\":{}", "resume.session_id"),
        ] {
            let error = refused("/v1/session/register", &format!("{{{REGISTRATION},{extra}}}"));
            assert_eq!(field(&error), named, "{extra}");
        }
        let long = format!("{{{REGISTRATION},\"model\":\"{}\"}}", "m".repeat(129));
        assert_eq!(field(&refused("/v1/session/register", &long)), "model");
        let version = format!("{{{REGISTRATION},\"brigade_version\":\"{}\"}}", "v".repeat(65));
        assert_eq!(
            field(&refused("/v1/session/register", &version)),
            "brigade_version"
        );
        let peer = format!("{{{REGISTRATION},\"sync_peer\":\"{}\"}}", "p".repeat(257));
        assert_eq!(field(&refused("/v1/session/register", &peer)), "sync_peer");
        let no_team = format!("{{{}}}", REGISTRATION.replace(",\"team_ref\":\"t\"", ""));
        assert_eq!(field(&refused("/v1/session/register", &no_team)), "team_ref");
    }

    #[test]
    fn a_shape_member_of_the_wrong_type_is_a_document_that_does_not_read() {
        for extra in [
            "\"resume\":\"r\"",
            "\"resume\":5",
            "\"model\":7",
            "\"lease_seconds\":\"90\"",
            "\"context_used_tokens\":1.5",
        ] {
            let error = refused("/v1/session/register", &format!("{{{REGISTRATION},{extra}}}"));
            assert_eq!((field(&error), reason(&error)), ("", "malformed_json"), "{extra}");
        }
        let error = refused("/v1/message/ack", r#"{"session_id":"s","message_ids":[1]}"#);
        assert_eq!(reason(&error), "malformed_json");
        // Minus zero is zero, as the reference reads it.
        let zero = format!("{{{REGISTRATION},\"context_used_tokens\":-0}}");
        assert!(Command::parse("/v1/session/register", zero.as_bytes()).is_ok());
    }

    #[test]
    fn the_team_and_the_lease_are_told_before_the_shape() {
        // Two things wrong: the member the contract adds wins over BAP/1's shape, and the lease over the rest.
        let error = refused("/v1/session/register", r#"{"activity":"asleep"}"#);
        assert_eq!(field(&error), "team_ref");
        let error = refused(
            "/v1/session/register",
            r#"{"team_ref":"t","lease_seconds":5,"activity":"asleep"}"#,
        );
        assert_eq!(field(&error), "lease_seconds");
        let error = refused(
            "/v1/session/heartbeat",
            r#"{"session_id":"s","heartbeat":{"lease_seconds":5,"activity":"x"}}"#,
        );
        assert_eq!(field(&error), "lease_seconds");
    }

    #[test]
    fn an_enumerated_member_names_what_it_allows() {
        let body = format!("{{{}}}", REGISTRATION.replace("busy", "asleep"));
        let error = refused("/v1/session/register", &body);
        assert_eq!((field(&error), reason(&error)), ("activity", "invalid_value"));
        assert_eq!(error.details["allowed"], "busy, idle");
        let body = format!("{{{}}}", REGISTRATION.replace("accept", "maybe"));
        assert_eq!(field(&refused("/v1/session/register", &body)), "inbound");
    }

    #[test]
    fn a_heartbeat_changes_only_what_it_names() {
        let Command::SessionHeartbeat(heartbeat) = Command::parse(
            "/v1/session/heartbeat",
            br#"{"session_id":"s","heartbeat":{"session_description":"","activity":null}}"#,
        )
        .unwrap() else {
            panic!("not a heartbeat");
        };
        assert_eq!(heartbeat.changes.session_description.as_deref(), Some(""));
        assert_eq!(heartbeat.changes.activity, None);
        assert_eq!(heartbeat.changes.session_name, None);

        // A heartbeat that is absent, null or not an object is the same refusal.
        for body in [
            r#"{"session_id":"s"}"#,
            r#"{"session_id":"s","heartbeat":null}"#,
            r#"{"session_id":"s","heartbeat":"x"}"#,
        ] {
            let error = refused("/v1/session/heartbeat", body);
            assert_eq!(
                (field(&error), reason(&error)),
                ("heartbeat", "required"),
                "{body}"
            );
        }
        assert_eq!(
            field(&refused("/v1/session/heartbeat", r#"{"heartbeat":{}}"#)),
            "session_id"
        );
        let error = refused(
            "/v1/session/heartbeat",
            r#"{"session_id":"s","heartbeat":{"session_name":""}}"#,
        );
        assert_eq!((field(&error), reason(&error)), ("session_name", "required"));
        let error = refused(
            "/v1/session/heartbeat",
            r#"{"session_id":"s","heartbeat":{"lease_seconds":5}}"#,
        );
        assert_eq!(field(&error), "lease_seconds");
        // A member the contract adds is named when its type is wrong.
        let error = refused("/v1/session/close", r#"{"session_id":5}"#);
        assert_eq!((field(&error), reason(&error)), ("session_id", "invalid_value"));
        let error = refused("/v1/session/list", r#"{"team_ref":"t","include_offline":"yes"}"#);
        assert_eq!(field(&error), "include_offline");
    }

    #[test]
    fn a_send_refuses_every_member_the_server_stamps() {
        let base = r#""sender_session_id":"a","recipient_session_id":"b","body":"hi""#;
        assert!(Command::parse("/v1/message/send", format!("{{{base}}}").as_bytes()).is_ok());
        for forbidden in [
            "sender",
            "principal_ref",
            "human_label",
            "team_ref",
            "created_at",
            "hop_count",
        ] {
            let error = refused("/v1/message/send", &format!("{{{base},\"{forbidden}\":null}}"));
            assert_eq!((field(&error), reason(&error)), (forbidden, "forbidden_member"));
        }
    }

    #[test]
    fn a_body_is_capped_in_bytes_and_a_summary_in_code_points() {
        let send = |body: &str, summary: &str| {
            format!(
                r#"{{"sender_session_id":"a","recipient_session_id":"b","body":"{body}","summary":"{summary}"}}"#
            )
        };
        assert!(
            Command::parse(
                "/v1/message/send",
                send(&"x".repeat(16_384), &"é".repeat(200)).as_bytes()
            )
            .is_ok()
        );
        let error = refused("/v1/message/send", &send(&"x".repeat(16_385), ""));
        assert_eq!((field(&error), error.details["unit"].as_str()), ("body", "bytes"));
        // 8,193 two-byte characters are 16,386 bytes: over the byte cap although under 16,384 characters.
        let error = refused("/v1/message/send", &send(&"é".repeat(8193), ""));
        assert_eq!(field(&error), "body");
        let error = refused("/v1/message/send", &send("x", &"é".repeat(201)));
        assert_eq!(
            (field(&error), error.details["unit"].as_str()),
            ("summary", "codepoints")
        );
        assert_eq!(field(&refused("/v1/message/send", &send("", ""))), "body");
        let key = format!(
            r#"{{"sender_session_id":"a","recipient_session_id":"b","body":"x","idempotency_key":"{}"}}"#,
            "k".repeat(129)
        );
        assert_eq!(field(&refused("/v1/message/send", &key)), "idempotency_key");
    }

    #[test]
    fn a_receive_has_bounds_and_defaults() {
        let Command::MessageReceive(receive) =
            Command::parse("/v1/message/receive", br#"{"session_id":"s"}"#).unwrap()
        else {
            panic!("not a receive");
        };
        assert_eq!(
            (receive.limit, receive.wait_seconds, receive.after),
            (50, 0, None)
        );
        for (extra, named) in [
            ("\"limit\":0", "limit"),
            ("\"limit\":201", "limit"),
            ("\"limit\":\"ten\"", "limit"),
            ("\"wait_seconds\":31", "wait_seconds"),
            ("\"wait_seconds\":-1", "wait_seconds"),
        ] {
            let error = refused(
                "/v1/message/receive",
                &format!("{{\"session_id\":\"s\",{extra}}}"),
            );
            assert_eq!((field(&error), reason(&error)), (named, "out_of_range"));
        }
    }

    #[test]
    fn an_ack_needs_ids() {
        assert_eq!(
            field(&refused("/v1/message/ack", r#"{"session_id":"s"}"#)),
            "message_ids"
        );
        assert_eq!(
            field(&refused(
                "/v1/message/ack",
                r#"{"session_id":"s","message_ids":[]}"#
            )),
            "message_ids"
        );
        assert_eq!(
            field(&refused("/v1/message/ack", r#"{"message_ids":["m"]}"#)),
            "session_id"
        );
    }

    #[test]
    fn only_create_and_join_run_without_a_credential() {
        assert!(Command::open_path("/v1/team/create"));
        assert!(Command::open_path("/v1/team/join"));
        assert!(!Command::open_path("/v1/credential/revoke"));
        assert!(Command::team_scoped_path("/v1/session/register"));
        assert!(!Command::team_scoped_path("/v1/team/leave"));
    }

    #[test]
    fn the_three_administration_paths_are_the_owners_and_need_a_credential() {
        for path in [
            "/v1/team/rotate-secret",
            "/v1/team/revoke-member",
            "/v1/team/transfer",
        ] {
            assert!(Command::known_path(path), "{path}");
            assert!(Command::administration_path(path), "{path}");
            assert!(!Command::open_path(path), "{path}");
            assert_eq!(field(&refused(path, "{}")), "team_ref", "{path}");
        }
        assert!(!Command::administration_path("/v1/team/members"));
        assert!(!Command::administration_path("/v1/team/leave"));
        assert_eq!(
            Command::parse("/v1/team/rotate-secret", br#"{"team_ref":"t","more":1}"#).unwrap(),
            Command::TeamRotateSecret("t".into())
        );
        assert_eq!(
            Command::parse("/v1/team/transfer", br#"{"team_ref":"t","principal_ref":"p"}"#).unwrap(),
            Command::TeamTransfer(TeamTransfer {
                team_ref: "t".into(),
                principal_ref: "p".into(),
            })
        );
        for body in [
            r#"{"team_ref":"t"}"#,
            r#"{"team_ref":"t","principal_ref":""}"#,
            r#"{"team_ref":"t","principal_ref":null}"#,
        ] {
            let error = refused("/v1/team/transfer", body);
            assert_eq!(
                (field(&error), reason(&error)),
                ("principal_ref", "required"),
                "{body}"
            );
        }
        let error = refused("/v1/team/transfer", r#"{"team_ref":"t","principal_ref":7}"#);
        assert_eq!(
            (field(&error), reason(&error)),
            ("principal_ref", "invalid_value")
        );
    }

    #[test]
    fn a_revocation_is_one_of_two_forms_and_never_both() {
        let read = |members: &str| {
            let body = format!("{{\"team_ref\":\"t\"{members}}}");
            match Command::parse("/v1/team/revoke-member", body.as_bytes()) {
                Ok(Command::TeamRevokeMember(TeamRevoke { team_ref, whom })) => {
                    assert_eq!(team_ref, "t");
                    Ok(whom)
                }
                Ok(other) => panic!("not a revocation: {other:?}"),
                Err(error) => Err(error),
            }
        };
        let one = |ban: bool| Revocation::Principal {
            principal_ref: "p".into(),
            ban,
        };
        // One member, with a ban or without one; a ban that is absent, `null` or `false` is none.
        assert_eq!(read(r#","principal_ref":"p""#).unwrap(), one(false));
        assert_eq!(read(r#","principal_ref":"p","ban":null"#).unwrap(), one(false));
        assert_eq!(read(r#","principal_ref":"p","ban":false"#).unwrap(), one(false));
        assert_eq!(read(r#","principal_ref":"p","ban":true"#).unwrap(), one(true));
        // Every member up to a version, from zero to the highest the reference backend holds.
        for (version, max) in [("0", 0), ("1", 1), ("2147483647", 2_147_483_647)] {
            let members = format!(",\"joined_secret_version_lte\":{version}");
            assert_eq!(read(&members).unwrap(), Revocation::UpToVersion(max));
        }
        // An empty or `null` principal is an absent one, and a ban that is not asked for is no ban.
        assert_eq!(
            read(r#","principal_ref":"","joined_secret_version_lte":2,"ban":false"#).unwrap(),
            Revocation::UpToVersion(2)
        );
        assert_eq!(
            read(r#","principal_ref":null,"joined_secret_version_lte":2"#).unwrap(),
            Revocation::UpToVersion(2)
        );

        for (members, named, why) in [
            // Both forms, a ban beside a version, and neither form.
            (
                r#","principal_ref":"p","joined_secret_version_lte":1"#,
                "principal_ref",
                "invalid_value",
            ),
            (
                r#","joined_secret_version_lte":1,"ban":true"#,
                "ban",
                "invalid_value",
            ),
            ("", "principal_ref", "required"),
            (r#","principal_ref":"""#, "principal_ref", "required"),
            (r#","ban":true"#, "principal_ref", "required"),
            (
                r#","joined_secret_version_lte":null"#,
                "principal_ref",
                "required",
            ),
            // A version outside its range, or that is no whole number.
            (
                r#","joined_secret_version_lte":-1"#,
                "joined_secret_version_lte",
                "out_of_range",
            ),
            (
                r#","joined_secret_version_lte":2147483648"#,
                "joined_secret_version_lte",
                "out_of_range",
            ),
            (
                r#","joined_secret_version_lte":"1""#,
                "joined_secret_version_lte",
                "out_of_range",
            ),
            (
                r#","joined_secret_version_lte":1.5"#,
                "joined_secret_version_lte",
                "out_of_range",
            ),
            // A member of the wrong type is named.
            (r#","principal_ref":7"#, "principal_ref", "invalid_value"),
            (r#","principal_ref":"p","ban":"yes""#, "ban", "invalid_value"),
        ] {
            let error = read(members).unwrap_err();
            assert_eq!(error.code, Code::InvalidInput, "{members}");
            assert_eq!((field(&error), reason(&error)), (named, why), "{members}");
        }
        // Both forms win over a ban, and the range is named with its bounds.
        let error = read(r#","principal_ref":"p","joined_secret_version_lte":1,"ban":true"#).unwrap_err();
        assert_eq!(field(&error), "principal_ref");
        let error = read(r#","joined_secret_version_lte":-1"#).unwrap_err();
        assert_eq!(
            (error.details["min"].as_str(), error.details["max"].as_str()),
            ("0", "2147483647")
        );
    }
}
