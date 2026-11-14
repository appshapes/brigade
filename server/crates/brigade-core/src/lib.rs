//! The rules of a Brigade backend.
//!
//! A server that implements the backend REST contract (`docs/backend-rest-v1.md`) keeps every rule of
//! BAP/1's section 4.5 that names "the adapter" as an authority: it stamps identity, ids and time, checks
//! ownership and membership, enforces the limits and the quotas, and keeps the leases and the retention.
//! This crate is those rules and nothing else. It does no I/O and reads no clock: the caller hands
//! [`answer`] the request, the time and a source of randomness, and gets back the answer and the entry to make
//! durable before the answer is sent.
//!
//! - [`wire`] reads and checks a request.
//! - [`answer`] answers it against the [`state::State`] and makes its [`record::Entry`] of records.
//! - [`JoinFailures`] is the one count no record holds, a principal's failed joins: [`answer`] is handed it
//!   beside the state.
//! - [`import_team`] takes in a team that exists elsewhere, [`set_owner`] names a team's owner,
//!   [`take_back`] takes a team from whoever holds it, and [`open_move`] and [`close_move`] open and close
//!   the move of a team's members from another backend, for the operator and for no request.
//! - [`state::State::apply`] is what a restart and a standby run over the same records.

pub mod attempts;
pub mod error;
pub mod execute;
pub mod hash;
pub mod json;
pub mod limits;
pub mod record;
pub mod state;
pub mod time;
pub mod wire;

pub use attempts::JoinFailures;
pub use error::{Code, Error, envelope_error, envelope_ok};
pub use execute::{
    MOVE_TICKET_DEFAULT_DAYS, MOVE_TICKET_MAX_DAYS, Outcome, Request, answer, close_move, execute,
    import_team, open_move, set_owner, take_back,
};
pub use hash::Entropy;
pub use record::{Entry, Record};
pub use state::State;
pub use time::Timestamp;
pub use wire::Command;
