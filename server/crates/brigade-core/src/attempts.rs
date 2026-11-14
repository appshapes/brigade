//! The joins a principal failed, which no record holds.
//!
//! A request that is refused makes no entry ([`crate::execute`]), and the state is built from entries alone,
//! so the state cannot count a refusal. The failed joins of a principal are therefore counted beside the
//! state, in the memory of the process that answers, and [`crate::answer`] is handed both. A restart forgets
//! them, and a server that takes over from another starts with none. That is enough for what the bound is
//! for: one installation that tries secret after secret, or that repeats a secret which no longer works, is
//! told to wait.
//!
//! What is counted is what the reference backend counts: a join refused for its secret (a wrong secret, a
//! team that does not exist, a principal the team banned), by a caller whose credential the server knows,
//! over all teams. A join that is let in clears its principal's failures. Three callers are counted for
//! nothing. One with no credential the server knows has no principal until its join succeeds: what holds a
//! stranger is a limit by address, and that is the proxy's in front of the server. One whose request could
//! not be read made no attempt on a secret. And a gateway, whose join is made inside the server, tries
//! again each minute with the file its operator keeps, and is let in the minute that file is right.

use std::collections::{BTreeMap, VecDeque};

use crate::error::Error;
use crate::execute::within_budget;
use crate::limits;
use crate::time::Timestamp;

/// The times of each principal's failed joins, by the principal's reference.
#[derive(Debug, Default)]
pub struct JoinFailures {
    failed_at: BTreeMap<String, VecDeque<Timestamp>>,
    /// When every principal was last looked at for failures that have left the window.
    swept_at: Option<Timestamp>,
}

impl JoinFailures {
    /// Nobody has failed a join.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Whether the principal's next join is heard at `now`.
    ///
    /// # Errors
    /// `rate_limited` with the reason `join_attempts` for a principal that has failed
    /// [`limits::FAILED_JOINS_PER_WINDOW`] joins in the last [`limits::FAILED_JOIN_WINDOW_SECONDS`], and
    /// the wait until the oldest of them leaves the window, never under
    /// [`limits::FAILED_JOIN_RETRY_FLOOR_MS`].
    pub(crate) fn admit(&mut self, principal_ref: &str, now: Timestamp) -> Result<(), Error> {
        let Some(times) = self.failed_at.get_mut(principal_ref) else {
            return Ok(());
        };
        in_window(times, now);
        if times.is_empty() {
            self.failed_at.remove(principal_ref);
            return Ok(());
        }
        within_budget(
            times.iter(),
            now,
            limits::FAILED_JOIN_WINDOW_SECONDS,
            limits::FAILED_JOINS_PER_WINDOW,
            "join_attempts",
        )
        .map_err(|mut limited| {
            limited.retry_after_ms = limited
                .retry_after_ms
                .map(|wait| wait.max(limits::FAILED_JOIN_RETRY_FLOOR_MS));
            limited
        })
    }

    /// The refusal of a join for its secret, counted against `principal_ref` when the join is one that
    /// counts.
    pub(crate) fn rejected(&mut self, principal_ref: Option<&str>, now: Timestamp) -> Error {
        if let Some(principal_ref) = principal_ref {
            self.sweep(now);
            let times = self.failed_at.entry(principal_ref.to_owned()).or_default();
            in_window(times, now);
            times.push_back(now);
        }
        Error::secret_rejected()
    }

    /// A join of the principal's was let in: what it failed before is forgotten.
    pub(crate) fn succeeded(&mut self, principal_ref: &str) {
        self.failed_at.remove(principal_ref);
    }

    /// Forgets every principal whose failures have all left the window, once in a window at most, so that
    /// what is kept is the principals that failed a join within the last two windows and a failed join costs
    /// the look at one principal, not at all of them.
    fn sweep(&mut self, now: Timestamp) {
        let due = self.swept_at.is_none_or(|swept_at| {
            now >= swept_at.plus_seconds(limits::FAILED_JOIN_WINDOW_SECONDS) || now < swept_at
        });
        if due {
            self.swept_at = Some(now);
            self.failed_at.retain(|_, times| {
                in_window(times, now);
                !times.is_empty()
            });
        }
    }
}

/// Keeps the failures of the window that ends at `now`. One stamped after `now` is let go of with the ones
/// that are too old: no record holds these times, so the clock they were read from may have been set back
/// since, and a failure that lies ahead would otherwise hold its principal for as long as the clock went
/// back.
fn in_window(times: &mut VecDeque<Timestamp>, now: Timestamp) {
    let window_start = now.minus_seconds(limits::FAILED_JOIN_WINDOW_SECONDS);
    times.retain(|at| *at > window_start && *at <= now);
}

#[cfg(test)]
mod tests {
    use super::*;

    const SECOND: i64 = 1_000_000_000;

    #[test]
    fn what_is_kept_is_the_principals_that_failed_lately_and_a_failure_looks_at_its_own() {
        let mut failures = JoinFailures::new();
        let start = Timestamp(1_800_000_000 * SECOND);
        for n in 0..100 {
            failures.rejected(Some(&format!("p{n}")), start);
        }
        // A caller that is not counted leaves nothing.
        failures.rejected(None, start);
        assert_eq!(failures.failed_at.len(), 100);
        // Inside the window a failure adds its own principal and looks at no other.
        failures.rejected(Some("late"), start.plus_seconds(60));
        assert_eq!((failures.failed_at.len(), failures.swept_at), (101, Some(start)));
        // A window after the last look, every principal whose failures have all left it is forgotten.
        let later = start.plus_seconds(limits::FAILED_JOIN_WINDOW_SECONDS + 61);
        failures.rejected(Some("later"), later);
        assert_eq!(failures.failed_at.keys().collect::<Vec<_>>(), ["later"]);
        assert_eq!(failures.swept_at, Some(later));
        // A principal that is let in is forgotten at once.
        failures.succeeded("later");
        assert!(failures.failed_at.is_empty());
    }
}
