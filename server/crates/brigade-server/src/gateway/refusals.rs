//! What the server says of the requests its gateways turn away.
//!
//! Anyone can send a request to a gateway's endpoint, and a mail to a team's inbox, so a line for each one
//! that is refused, dropped or failed on would let a stranger fill the server's own lines. A refusal of one
//! kind is therefore said at most once a minute: the first one at once, and after that one line a minute
//! for as long as they come, with how many there were since the kind was last said.
//!
//! The kinds are few and the server's own: an endpoint and a reason, or a team. Nothing a request carried
//! is ever one.

use std::sync::Mutex;

use brigade_core::Timestamp;

/// How long a kind of refusal is not said again after it was said.
const SAID_EVERY_SECONDS: i64 = 60;

/// One kind of refusal, and what is still to say of it.
struct Counted {
    /// The sentence that names the kind.
    what: String,
    /// When the kind was last said.
    said: Timestamp,
    /// How many refusals of the kind came since.
    unsaid: u32,
}

impl Counted {
    /// Whether the kind may be said again at `now`: a minute has gone by, or the clock went back.
    fn due(&self, now: Timestamp) -> bool {
        now < self.said || self.said.seconds_until(now) >= SAID_EVERY_SECONDS
    }

    /// The line for what came since the kind was last said, which is then nothing.
    fn say(&mut self, now: Timestamp) -> String {
        let line = match self.unsaid {
            0 | 1 => self.what.clone(),
            count => format!("{}, {count} times since it was last said", self.what),
        };
        self.said = now;
        self.unsaid = 0;
        line
    }
}

/// The refusals of every kind, counted.
#[derive(Default)]
pub struct Refusals {
    kinds: Mutex<Vec<Counted>>,
}

impl Refusals {
    /// Counts one refusal of the kind `what` names, at `now`, and answers the line to write for it: the
    /// sentence itself for a kind that is new, the sentence and the count for one that was last said a
    /// minute ago or longer, and nothing for one that was said within the minute.
    pub fn refused(&self, what: &str, now: Timestamp) -> Option<String> {
        let mut kinds = self
            .kinds
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let Some(kind) = kinds.iter_mut().find(|kind| kind.what == what) else {
            kinds.push(Counted {
                what: what.to_owned(),
                said: now,
                unsaid: 0,
            });
            return Some(what.to_owned());
        };
        kind.unsaid += 1;
        kind.due(now).then(|| kind.say(now))
    }

    /// The lines that are due at `now`: one for every kind that was refused since it was last said, a
    /// minute ago or longer. The loop asks, so that the last refusals of a burst are said though nothing
    /// comes after them.
    pub fn due(&self, now: Timestamp) -> Vec<String> {
        let mut kinds = self
            .kinds
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        kinds
            .iter_mut()
            .filter(|kind| kind.unsaid > 0 && kind.due(now))
            .map(|kind| kind.say(now))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const UNSIGNED: &str = "a request to the Slack endpoint was refused: unsigned";
    const POSTMARK: &str = "a webhook of postmark was refused: 403";

    fn at(seconds: i64) -> Timestamp {
        Timestamp(seconds * 1_000_000_000)
    }

    #[test]
    fn a_kind_of_refusal_is_said_at_most_once_a_minute_with_how_many_there_were() {
        let refusals = Refusals::default();
        // The first is said at once, as it is.
        assert_eq!(refusals.refused(UNSIGNED, at(1000)).as_deref(), Some(UNSIGNED));
        // A thousand more within the minute are counted and not said.
        for second in 0..1000 {
            assert_eq!(refusals.refused(UNSIGNED, at(1000 + second % 60)), None);
        }
        assert!(refusals.due(at(1059)).is_empty());
        // The first one after the minute is said, with all that came since the last line.
        assert_eq!(
            refusals.refused(UNSIGNED, at(1060)).as_deref(),
            Some("a request to the Slack endpoint was refused: unsigned, 1001 times since it was last said")
        );
        // And the count begins again.
        assert_eq!(refusals.refused(UNSIGNED, at(1061)), None);
        assert_eq!(refusals.refused(UNSIGNED, at(1119)), None);
        assert_eq!(
            refusals.refused(UNSIGNED, at(1120)).as_deref(),
            Some("a request to the Slack endpoint was refused: unsigned, 3 times since it was last said")
        );
        // One refusal long after the last line is one refusal.
        assert_eq!(refusals.refused(UNSIGNED, at(9000)).as_deref(), Some(UNSIGNED));
    }

    #[test]
    fn each_kind_has_its_own_minute_and_what_a_burst_ends_with_is_said_when_the_minute_is_over() {
        let refusals = Refusals::default();
        assert_eq!(refusals.refused(UNSIGNED, at(0)).as_deref(), Some(UNSIGNED));
        // Another kind is said at once, whatever was said of the first.
        assert_eq!(refusals.refused(POSTMARK, at(30)).as_deref(), Some(POSTMARK));
        assert_eq!(refusals.refused(UNSIGNED, at(31)), None);
        assert_eq!(refusals.refused(UNSIGNED, at(32)), None);
        assert_eq!(refusals.refused(POSTMARK, at(33)), None);
        // Nothing more comes. Each kind's rest is said when its own minute is over, and once.
        assert!(refusals.due(at(59)).is_empty());
        assert_eq!(
            refusals.due(at(60)),
            ["a request to the Slack endpoint was refused: unsigned, 2 times since it was last said"]
        );
        assert!(refusals.due(at(89)).is_empty());
        assert_eq!(refusals.due(at(90)), [POSTMARK]);
        assert!(refusals.due(at(100_000)).is_empty());
        // A clock that went back does not silence a kind until it has caught up.
        assert_eq!(refusals.refused(POSTMARK, at(10)).as_deref(), Some(POSTMARK));
    }
}
