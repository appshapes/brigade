//! Time as the rules see it: a number the caller hands in.
//!
//! Nothing in this crate reads a clock. The caller reads it once per request and passes `now`; a record
//! carries the time it was made at, so a restart and a standby reach the same state from the same records.

use serde::{Deserialize, Serialize};

const NANOS_PER_SECOND: i64 = 1_000_000_000;

/// Nanoseconds since the Unix epoch, UTC.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Timestamp(pub i64);

impl Timestamp {
    /// The time `seconds` after this one.
    #[must_use]
    pub fn plus_seconds(self, seconds: i64) -> Self {
        Self(self.0.saturating_add(seconds.saturating_mul(NANOS_PER_SECOND)))
    }

    /// The time `seconds` before this one.
    #[must_use]
    pub fn minus_seconds(self, seconds: i64) -> Self {
        Self(self.0.saturating_sub(seconds.saturating_mul(NANOS_PER_SECOND)))
    }

    /// Whole milliseconds from this time until `later`; negative when `later` is earlier.
    #[must_use]
    pub fn millis_until(self, later: Self) -> i64 {
        later.0.saturating_sub(self.0) / 1_000_000
    }

    /// Whole seconds from this time until `later`; negative when `later` is earlier.
    #[must_use]
    pub fn seconds_until(self, later: Self) -> i64 {
        later.0.saturating_sub(self.0) / NANOS_PER_SECOND
    }

    /// The RFC 3339 text the wire carries (protocol JSON convention 7): UTC, with the fraction of a second
    /// only as long as it needs to be.
    #[must_use]
    pub fn rfc3339(self) -> String {
        let seconds = self.0.div_euclid(NANOS_PER_SECOND);
        let nanos = self.0.rem_euclid(NANOS_PER_SECOND);
        let days = seconds.div_euclid(86_400);
        let of_day = seconds.rem_euclid(86_400);
        let (year, month, day) = civil_from_days(days);
        let mut text = format!(
            "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}",
            of_day / 3600,
            of_day % 3600 / 60,
            of_day % 60
        );
        if nanos != 0 {
            let fraction = format!("{nanos:09}");
            text.push('.');
            text.push_str(fraction.trim_end_matches('0'));
        }
        text.push('Z');
        text
    }
}

/// The civil date of a day counted from 1970-01-01, in the proleptic Gregorian calendar.
fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let shifted = days + 719_468;
    let era = shifted.div_euclid(146_097);
    let day_of_era = shifted.rem_euclid(146_097);
    let year_of_era = (day_of_era - day_of_era / 1460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_index = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_index + 2) / 5 + 1;
    let month = if month_index < 10 {
        month_index + 3
    } else {
        month_index - 9
    };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

#[cfg(test)]
mod tests {
    use super::Timestamp;

    #[test]
    fn renders_the_epoch_and_a_known_instant() {
        assert_eq!(Timestamp(0).rfc3339(), "1970-01-01T00:00:00Z");
        // 2026-08-30T12:00:00Z, the instant of the protocol's examples.
        assert_eq!(
            Timestamp(1_788_091_200 * 1_000_000_000).rfc3339(),
            "2026-08-30T12:00:00Z"
        );
    }

    #[test]
    fn keeps_only_the_fraction_it_needs() {
        assert_eq!(Timestamp(1_500_000_000).rfc3339(), "1970-01-01T00:00:01.5Z");
        assert_eq!(
            Timestamp(1_000_000_001).rfc3339(),
            "1970-01-01T00:00:01.000000001Z"
        );
    }

    #[test]
    fn renders_a_leap_day_and_a_time_before_the_epoch() {
        // 2024-02-29T23:59:59Z
        assert_eq!(
            Timestamp(1_709_251_199 * 1_000_000_000).rfc3339(),
            "2024-02-29T23:59:59Z"
        );
        assert_eq!(Timestamp(-1_000_000_000).rfc3339(), "1969-12-31T23:59:59Z");
    }

    #[test]
    fn moves_by_whole_seconds() {
        let at = Timestamp(10 * 1_000_000_000);
        assert_eq!(at.plus_seconds(90), Timestamp(100 * 1_000_000_000));
        assert_eq!(at.minus_seconds(10), Timestamp(0));
        assert_eq!(at.millis_until(at.plus_seconds(2)), 2000);
        assert_eq!(at.seconds_until(at.plus_seconds(2)), 2);
    }
}
