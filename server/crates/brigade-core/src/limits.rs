//! The constants of BAP/1's section 4.4.1.
//!
//! The contract's section 5 has a version 1 server implement these exactly: the bundled adapter advertises
//! them in `describe` without asking the server, and the conformance suite checks a value at each cap.

/// The cap on a message body, in bytes of UTF-8.
pub const MAX_BODY_BYTES: usize = 16_384;
/// The cap on a message summary, in code points.
pub const MAX_SUMMARY_CHARS: usize = 200;
/// The cap on a session name, in code points.
pub const MAX_SESSION_NAME_CODEPOINTS: usize = 64;
/// The cap on a team name, in code points.
pub const MAX_TEAM_NAME_CODEPOINTS: usize = 64;
/// The cap on a session description, in code points.
pub const MAX_DESCRIPTION_CHARS: usize = 256;
/// The cap on a human label, in code points.
pub const MAX_HUMAN_LABEL_CHARS: usize = 128;
/// The cap on a workspace label, in code points.
pub const MAX_WORKSPACE_LABEL_CHARS: usize = 128;
/// The cap on a model identity, in code points.
pub const MAX_MODEL_CHARS: usize = 128;
/// The cap on an idempotency key, in code points.
pub const MAX_IDEMPOTENCY_KEY_CHARS: usize = 128;
/// The cap on a Brigade version, in code points. A bound of the wire format, with no `limits` member.
pub const MAX_BRIGADE_VERSION_CHARS: usize = 64;
/// The cap on a sync peer descriptor, in code points. A bound of the wire format, with no `limits` member.
pub const MAX_SYNC_PEER_CHARS: usize = 256;
/// The cap on a harness's name and on its version, in code points. BAP/1 gives these two no cap, and a
/// server that kept whatever it was sent would write a record as long as the request: this bound is the
/// server's own, far above any name a harness has.
pub const MAX_HARNESS_CHARS: usize = 256;
/// The largest `context_used_tokens`: 2^53 - 1, the largest integer JSON carries exactly.
pub const MAX_CONTEXT_USED_TOKENS: i64 = (1 << 53) - 1;

/// The highest secret version a revocation may name: what the reference backend's integer holds (BAP/1
/// 4.4.10). A team's own count of secrets starts at one and never comes near it.
pub const MAX_SECRET_VERSION: i64 = 2_147_483_647;

/// One sender session's sends in a minute.
pub const SEND_RATE_PER_MINUTE: usize = 20;
/// One sender session's sends in an hour.
pub const SEND_RATE_PER_HOUR: usize = 200;
/// One principal's sends in a minute, over all its sessions.
pub const PRINCIPAL_SEND_RATE_PER_MINUTE: usize = 60;
/// One principal's sends in an hour, over all its sessions.
pub const PRINCIPAL_SEND_RATE_PER_HOUR: usize = 600;
/// The unacknowledged messages one recipient session may hold.
pub const MAX_UNACKED_PER_RECIPIENT: usize = 60;
/// The unacknowledged messages from one sender session to one recipient session.
pub const MAX_UNACKED_PER_SENDER_RECIPIENT: usize = 15;
/// The hop count beyond which a send is `loop_detected`.
pub const MAX_HOP_COUNT: u32 = 32;
/// The window in which an unlabelled answer counts as a reply, in seconds.
pub const IMPLICIT_REPLY_WINDOW_SECONDS: i64 = 600;

/// The window one principal's teams and sessions are counted over, in seconds.
pub const PRINCIPAL_RATE_WINDOW_SECONDS: i64 = 3600;
/// The teams one principal may create in an hour: the reference backend's number. A caller with no
/// credential the server knows is a new principal with each request, and no count of a principal's holds
/// it: that is a limit by address, which the proxy in front of a server keeps.
pub const TEAMS_CREATED_PER_HOUR: usize = 5;
/// The sessions one principal may open in an hour, over all its teams: the reference backend's number. A
/// resume opens no session and is not counted.
pub const SESSIONS_OPENED_PER_HOUR: usize = 120;
/// The joins one principal may fail in [`FAILED_JOIN_WINDOW_SECONDS`], over all teams, before its next
/// join is refused unheard: the reference backend's number.
pub const FAILED_JOINS_PER_WINDOW: usize = 5;
/// The window one principal's failed joins are counted over, in seconds.
pub const FAILED_JOIN_WINDOW_SECONDS: i64 = 900;
/// The shortest wait a join refused for its principal's failures is told, in milliseconds.
pub const FAILED_JOIN_RETRY_FLOOR_MS: u64 = 60_000;

/// The lease granted when a registration names none, in seconds.
pub const LEASE_DEFAULT_SECONDS: i64 = 90;
/// The shortest lease, in seconds.
pub const LEASE_MIN_SECONDS: i64 = 30;
/// The longest lease, in seconds.
pub const LEASE_MAX_SECONDS: i64 = 600;

/// How long an unacknowledged message is kept at least, in seconds.
pub const RETENTION_UNACKED_MESSAGE_SECONDS: i64 = 604_800;
/// How long an acknowledged message is kept, in seconds.
pub const RETENTION_ACKED_MESSAGE_SECONDS: i64 = 86_400;
/// How long a closed or expired session is kept, with its messages, in seconds.
pub const RETENTION_CLOSED_SESSION_SECONDS: i64 = 604_800;

/// The messages a receive returns when the request names no limit.
pub const RECEIVE_DEFAULT_LIMIT: usize = 50;
/// The most messages a receive returns.
pub const RECEIVE_MAX_LIMIT: usize = 200;
/// The longest a receive waits, in seconds (the contract's section 4).
pub const RECEIVE_MAX_WAIT_SECONDS: i64 = 30;

/// The retry hint for the two unacknowledged-message caps. A cap clears when the recipient acknowledges,
/// which the server cannot predict; the protocol asks only for a value above zero.
pub const CAP_RETRY_AFTER_MS: u64 = 5000;

/// The longest request body, in bytes (the contract's section 1).
pub const MAX_REQUEST_BYTES: usize = 1 << 20;
