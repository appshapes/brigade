//! What the server says about itself, on stderr.
//!
//! A line is a time, the program's name and a sentence. No line ever carries a credential, a join secret, a
//! creation token, a message's text or anything else a request held.

use std::time::{SystemTime, UNIX_EPOCH};

use brigade_core::Timestamp;

/// The time by this machine's clock.
#[must_use]
pub fn now() -> Timestamp {
    let since_epoch = SystemTime::now().duration_since(UNIX_EPOCH).unwrap_or_default();
    Timestamp(i64::try_from(since_epoch.as_nanos()).unwrap_or(i64::MAX))
}

/// Writes one line to stderr.
pub fn note(sentence: &str) {
    eprintln!("{} brigade-server: {sentence}", now().rfc3339());
}
