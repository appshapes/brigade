//! Dates as JavaScript's `Date` writes and reads them.
//!
//! The TypeScript stamped a mail with `new Date().toISOString()` and read a provider's date with
//! `new Date(text)`. Nothing here reads a clock: the caller hands in the time. Reading a date is the work
//! of this module, because JavaScript leaves it to the engine. The Edge Functions ran on V8, whose reader
//! takes the ISO 8601 form first and then almost anything a mail's `Date` header has ever held, so
//! [`parse`] follows V8's reader rule for rule (`src/date/dateparser-inl.h`), with one thing fixed: a date
//! that names no zone is local time to V8, and the local time of an Edge Function is UTC.

use brigade_core::Timestamp;

use crate::js;

const MS_PER_DAY: i64 = 86_400_000;
/// The latest and earliest time a `Date` holds, in milliseconds from the epoch.
const MAX_TIME_MS: i64 = 8_640_000_000_000_000;

/// `new Date().toISOString()` at `now`: UTC, always with three digits of milliseconds.
pub(crate) fn now_iso(now: Timestamp) -> String {
    iso(now.0.div_euclid(1_000_000))
}

/// `Math.floor(Date.now() / 1000)` at `now`.
pub(crate) fn now_seconds(now: Timestamp) -> i64 {
    now.0.div_euclid(1_000_000_000)
}

/// `new Date(ms).toISOString()`. A year outside 0000 to 9999 is written with a sign and six digits.
pub(crate) fn iso(ms: i64) -> String {
    let (year, month, day) = civil_from_days(ms.div_euclid(MS_PER_DAY));
    let of_day = ms.rem_euclid(MS_PER_DAY);
    let year = if (0..=9999).contains(&year) {
        format!("{year:04}")
    } else {
        format!("{}{:06}", if year < 0 { '-' } else { '+' }, year.abs())
    };
    format!(
        "{year}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

/// `new Date(text).getTime()`: the milliseconds from the epoch `text` names, `None` where JavaScript has
/// an invalid date.
pub(crate) fn parse(text: &str) -> Option<i64> {
    // V8 reads a string up to its first NUL.
    let text = text.split('\0').next().unwrap_or("");
    let mut scanner = Scanner {
        tokens: tokens(text),
        at: 0,
    };
    let (mut day, mut time, mut zone) = (Day::default(), Time::default(), Zone::default());

    let mut token = iso_date_time(&mut scanner, &mut day, &mut time, &mut zone)?;
    let mut has_read_number = !day.parts.is_empty();
    // What the ISO form did not take is read the older way: an unknown word before the first number is
    // ignored, a number before `:` is a part of the time, any other number is a part of the date, and a
    // sign after the time or after a UTC marker opens an offset.
    while token != Token::End {
        match token {
            Token::Number { value, .. } => {
                has_read_number = true;
                if scanner.skip_symbol(':') {
                    if scanner.skip_symbol(':') {
                        if !time.parts.is_empty() {
                            return None;
                        }
                        time.add(value);
                        time.add(0);
                    } else {
                        if !time.add(value) {
                            return None;
                        }
                        scanner.skip_symbol('.');
                    }
                } else if scanner.skip_symbol('.') && time.is_expecting(value) {
                    time.add(value);
                    let fraction @ Token::Number { .. } = scanner.next() else {
                        return None;
                    };
                    time.add_final(milliseconds(fraction));
                } else if zone.is_expecting(value) {
                    zone.minute = Some(value);
                } else if time.is_expecting(value) {
                    time.add_final(value);
                    // After the last part of a time comes the end, white space, `Z` or a sign.
                    let after = scanner.peek();
                    if !(matches!(after, Token::End | Token::Space | Token::Symbol('+' | '-'))
                        || after.is_z())
                    {
                        return None;
                    }
                } else {
                    if !day.add(value) {
                        return None;
                    }
                    scanner.skip_symbol('-');
                }
            }
            Token::Word { kind, value, .. } => {
                if kind == Kind::AmPm && !time.parts.is_empty() {
                    time.hour_offset = Some(value);
                } else if kind == Kind::Month {
                    day.named_month = Some(value);
                    scanner.skip_symbol('-');
                } else if kind == Kind::Zone && has_read_number {
                    zone.set(value);
                } else if has_read_number || matches!(scanner.peek(), Token::Number { .. }) {
                    // A word that means nothing is refused once a number was read, and so is one that
                    // runs straight into the first number.
                    return None;
                }
            }
            Token::Symbol(sign @ ('+' | '-')) if zone.is_utc() || !time.parts.is_empty() => {
                zone.sign = Some(if sign == '-' { -1 } else { 1 });
                let (mut number, mut length) = (0, 0);
                if let Token::Number {
                    value,
                    length: digits,
                } = scanner.peek()
                {
                    scanner.next();
                    (number, length) = (value, digits);
                }
                has_read_number = true;
                if scanner.peek() == Token::Symbol(':') {
                    zone.hour = Some(number);
                    zone.minute = None;
                } else if length == 1 || length == 2 {
                    zone.hour = Some(number);
                    zone.minute = Some(0);
                } else if length == 3 || length == 4 {
                    zone.hour = Some(number / 100);
                    zone.minute = Some(number % 100);
                } else {
                    return None;
                }
            }
            Token::Symbol('+' | '-' | ')') if has_read_number => return None,
            _ => {}
        }
        token = scanner.next();
    }

    let (year, month, day_of_month) = day.finish()?;
    let of_day = time.finish()?;
    let offset_ms = zone.finish()?;
    if !(-1_000_000..=1_000_000).contains(&year) {
        return None;
    }
    // A day past the end of its month runs on into the next, as `MakeDay` has it.
    let date = (days_from_civil(year, month, 1) + day_of_month - 1) * MS_PER_DAY + of_day - offset_ms;
    (date.abs() <= MAX_TIME_MS).then_some(date)
}

/// What a word is to the reader.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Kind {
    Month,
    AmPm,
    Zone,
    /// The `T` between an ISO date and its time.
    TimeSeparator,
    Unknown,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Token {
    End,
    /// A run of digits: its value, read from at most nine digits after its leading zeros, and how many
    /// digits it had.
    Number {
        value: i64,
        length: usize,
    },
    /// One of `:`, `-`, `+`, `.` and `)`.
    Symbol(char),
    Word {
        kind: Kind,
        value: i64,
        length: usize,
    },
    Space,
    /// Anything else, a parenthesised comment included. It is skipped.
    Other,
}

impl Token {
    fn is_fixed_number(self, digits: usize) -> bool {
        matches!(self, Self::Number { length, .. } if length == digits)
    }

    fn number(self) -> i64 {
        match self {
            Self::Number { value, .. } => value,
            _ => 0,
        }
    }

    fn is_z(self) -> bool {
        matches!(
            self,
            Self::Word {
                kind: Kind::Zone,
                length: 1,
                ..
            }
        )
    }
}

/// The words the reader knows, by their first three letters in lowercase. A month may run on past them
/// (`October`); every other word is its three letters or fewer and nothing more.
const WORDS: [(&str, Kind, i64); 27] = [
    ("jan", Kind::Month, 1),
    ("feb", Kind::Month, 2),
    ("mar", Kind::Month, 3),
    ("apr", Kind::Month, 4),
    ("may", Kind::Month, 5),
    ("jun", Kind::Month, 6),
    ("jul", Kind::Month, 7),
    ("aug", Kind::Month, 8),
    ("sep", Kind::Month, 9),
    ("oct", Kind::Month, 10),
    ("nov", Kind::Month, 11),
    ("dec", Kind::Month, 12),
    ("am", Kind::AmPm, 0),
    ("pm", Kind::AmPm, 12),
    ("ut", Kind::Zone, 0),
    ("utc", Kind::Zone, 0),
    ("z", Kind::Zone, 0),
    ("gmt", Kind::Zone, 0),
    ("cdt", Kind::Zone, -5),
    ("cst", Kind::Zone, -6),
    ("edt", Kind::Zone, -4),
    ("est", Kind::Zone, -5),
    ("mdt", Kind::Zone, -6),
    ("mst", Kind::Zone, -7),
    ("pdt", Kind::Zone, -7),
    ("pst", Kind::Zone, -8),
    ("t", Kind::TimeSeparator, 0),
];

fn word(letters: &[char]) -> Token {
    let prefix: String = letters.iter().take(3).map(char::to_ascii_lowercase).collect();
    let length = letters.len();
    for (name, kind, value) in WORDS {
        if prefix == name && (length <= 3 || kind == Kind::Month) {
            return Token::Word { kind, value, length };
        }
    }
    Token::Word {
        kind: Kind::Unknown,
        value: 0,
        length,
    }
}

fn tokens(text: &str) -> Vec<Token> {
    let chars: Vec<char> = text.chars().collect();
    // A word is any run of characters from `A` up that are not white space, so `é` is a word too.
    let in_word = |c: char| c >= 'A' && !js::is_space(c);
    let mut tokens = Vec::new();
    let mut at = 0;
    while at < chars.len() {
        let start = at;
        let c = chars[at];
        if c.is_ascii_digit() {
            while at < chars.len() && chars[at] == '0' {
                at += 1;
            }
            let (mut value, mut significant) = (0_i64, 0);
            while at < chars.len() && chars[at].is_ascii_digit() {
                if significant < 9 {
                    value = value * 10 + i64::from(chars[at].to_digit(10).unwrap_or(0));
                }
                significant += 1;
                at += 1;
            }
            tokens.push(Token::Number {
                value,
                length: at - start,
            });
        } else if matches!(c, ':' | '-' | '+' | '.' | ')') {
            tokens.push(Token::Symbol(c));
            at += 1;
        } else if in_word(c) {
            while at < chars.len() && in_word(chars[at]) {
                at += 1;
            }
            tokens.push(word(&chars[start..at]));
        } else if js::is_space(c) {
            tokens.push(Token::Space);
            at += 1;
        } else if c == '(' {
            // A comment runs to the parenthesis that closes it, or to the end.
            let mut depth = 0;
            loop {
                match chars[at] {
                    '(' => depth += 1,
                    ')' => depth -= 1,
                    _ => {}
                }
                at += 1;
                if depth <= 0 || at >= chars.len() {
                    break;
                }
            }
            tokens.push(Token::Other);
        } else {
            tokens.push(Token::Other);
            at += 1;
        }
    }
    tokens
}

struct Scanner {
    tokens: Vec<Token>,
    at: usize,
}

impl Scanner {
    fn peek(&self) -> Token {
        self.tokens.get(self.at).copied().unwrap_or(Token::End)
    }

    fn next(&mut self) -> Token {
        let token = self.peek();
        if self.at < self.tokens.len() {
            self.at += 1;
        }
        token
    }

    fn skip_symbol(&mut self, symbol: char) -> bool {
        let found = self.peek() == Token::Symbol(symbol);
        if found {
            self.at += 1;
        }
        found
    }
}

/// The parts of a date in the order they were read, at most three.
#[derive(Default)]
struct Day {
    parts: Vec<i64>,
    named_month: Option<i64>,
    is_iso: bool,
}

impl Day {
    fn add(&mut self, part: i64) -> bool {
        let room = self.parts.len() < 3;
        if room {
            self.parts.push(part);
        }
        room
    }

    /// The year, the month from one and the day. A part that was not given is one, which is why `4 Oct`
    /// is in 2001. Without the ISO form a year below 50 is in this century and one below 100 in the last.
    fn finish(mut self) -> Option<(i64, i64, i64)> {
        if self.parts.is_empty() {
            return None;
        }
        self.parts.resize(3, 1);
        let is_day = |part: i64| (1..=31).contains(&part);
        let (mut year, month, day) = match self.named_month {
            None if self.is_iso || !is_day(self.parts[0]) => (self.parts[0], self.parts[1], self.parts[2]),
            None => (self.parts[2], self.parts[0], self.parts[1]),
            Some(month) if !is_day(self.parts[0]) => (self.parts[0], month, self.parts[1]),
            Some(month) => (self.parts[1], month, self.parts[0]),
        };
        if !self.is_iso {
            if (0..=49).contains(&year) {
                year += 2000;
            } else if (50..=99).contains(&year) {
                year += 1900;
            }
        }
        ((1..=12).contains(&month) && is_day(day)).then_some((year, month, day))
    }
}

/// The parts of a time in the order they were read: hour, minute, second, millisecond.
#[derive(Default)]
struct Time {
    parts: Vec<i64>,
    /// What `am` or `pm` adds to the hour.
    hour_offset: Option<i64>,
}

impl Time {
    fn add(&mut self, part: i64) -> bool {
        let room = self.parts.len() < 4;
        if room {
            self.parts.push(part);
        }
        room
    }

    fn add_final(&mut self, part: i64) {
        self.add(part);
        self.parts.resize(4, 0);
    }

    /// Whether `part` can be the next part of a time that has begun. A second of 60 cannot, so V8 reads it
    /// as a part of the date instead, where it is one number too many and is dropped.
    fn is_expecting(&self, part: i64) -> bool {
        match self.parts.len() {
            1 | 2 => (0..=59).contains(&part),
            3 => (0..=999).contains(&part),
            _ => false,
        }
    }

    /// The milliseconds into the day. 24:00:00 is the one time past 23:59:59.999.
    fn finish(mut self) -> Option<i64> {
        self.parts.resize(4, 0);
        let (mut hour, minute, second, ms) = (self.parts[0], self.parts[1], self.parts[2], self.parts[3]);
        if let Some(offset) = self.hour_offset {
            if !(0..=12).contains(&hour) {
                return None;
            }
            hour = hour % 12 + offset;
        }
        let in_range = (0..=23).contains(&hour)
            && (0..=59).contains(&minute)
            && (0..=59).contains(&second)
            && (0..=999).contains(&ms);
        if !in_range && (hour, minute, second, ms) != (24, 0, 0, 0) {
            return None;
        }
        Some(hour * 3_600_000 + minute * 60_000 + second * 1000 + ms)
    }
}

#[derive(Default)]
struct Zone {
    sign: Option<i64>,
    hour: Option<i64>,
    minute: Option<i64>,
}

impl Zone {
    fn set(&mut self, hours: i64) {
        self.sign = Some(if hours < 0 { -1 } else { 1 });
        self.hour = Some(hours.abs());
        self.minute = Some(0);
    }

    fn is_utc(&self) -> bool {
        self.hour == Some(0) && self.minute == Some(0)
    }

    fn is_expecting(&self, part: i64) -> bool {
        self.hour.is_some() && self.minute.is_none() && (0..=59).contains(&part)
    }

    /// The offset from UTC in milliseconds. A date that names no zone is local time, and local time is UTC.
    ///
    /// V8 adds the offset up in 32 bits and refuses a sum above 2^31 - 1, so an hour of seven digits wraps
    /// around to an offset it accepts. The sum here is the same one.
    fn finish(self) -> Option<i64> {
        let Some(sign) = self.sign else {
            return Some(0);
        };
        let part = |part: Option<i64>| u32::try_from(part.unwrap_or(0)).unwrap_or(0);
        let seconds = part(self.hour)
            .wrapping_mul(3600)
            .wrapping_add(part(self.minute).wrapping_mul(60));
        i32::try_from(seconds)
            .ok()
            .map(|seconds| sign * i64::from(seconds) * 1000)
    }
}

/// The ISO 8601 form ECMAScript defines: `YYYY[-MM[-DD]]`, or a signed year of six digits, then an
/// optional `THH:mm[:ss[.sss]]` and `Z` or an offset. It returns the first token it did not take, for the
/// older reading to go on from, [`Token::End`] when it took the whole text, and `None` when a time began
/// and was not this form.
fn iso_date_time(scanner: &mut Scanner, day: &mut Day, time: &mut Time, zone: &mut Zone) -> Option<Token> {
    if let sign @ Token::Symbol('+' | '-') = scanner.peek() {
        scanner.next();
        if !scanner.peek().is_fixed_number(6) {
            return Some(sign);
        }
        let year = scanner.next().number();
        if sign == Token::Symbol('-') {
            if year == 0 {
                return Some(sign);
            }
            day.add(-year);
        } else {
            day.add(year);
        }
    } else if scanner.peek().is_fixed_number(4) {
        day.add(scanner.next().number());
    } else {
        return Some(scanner.next());
    }
    if scanner.skip_symbol('-') {
        if !scanner.peek().is_fixed_number(2) || !(1..=12).contains(&scanner.peek().number()) {
            return Some(scanner.next());
        }
        day.add(scanner.next().number());
        if scanner.skip_symbol('-') {
            if !scanner.peek().is_fixed_number(2) || !(1..=31).contains(&scanner.peek().number()) {
                return Some(scanner.next());
            }
            day.add(scanner.next().number());
        }
    }
    let is_separator = matches!(
        scanner.peek(),
        Token::Word {
            kind: Kind::TimeSeparator,
            ..
        }
    );
    if !is_separator {
        if scanner.peek() != Token::End {
            return Some(scanner.next());
        }
    } else {
        scanner.next();
        let two_digits =
            |token: Token, most: i64| token.is_fixed_number(2) && (0..=most).contains(&token.number());
        if !two_digits(scanner.peek(), 24) {
            return None;
        }
        // 24:00:00.000 is allowed and no other time that begins with 24.
        let is_24 = scanner.peek().number() == 24;
        let within = |token: Token| !is_24 || token.number() == 0;
        time.add(scanner.next().number());
        if !scanner.skip_symbol(':') || !two_digits(scanner.peek(), 59) || !within(scanner.peek()) {
            return None;
        }
        time.add(scanner.next().number());
        if scanner.skip_symbol(':') {
            if !two_digits(scanner.peek(), 59) || !within(scanner.peek()) {
                return None;
            }
            time.add(scanner.next().number());
            if scanner.skip_symbol('.') {
                if !matches!(scanner.peek(), Token::Number { .. }) || !within(scanner.peek()) {
                    return None;
                }
                time.add(milliseconds(scanner.next()));
            }
        }
        if scanner.peek().is_z() {
            scanner.next();
            zone.set(0);
        } else if let Token::Symbol(sign @ ('+' | '-')) = scanner.peek() {
            scanner.next();
            zone.sign = Some(if sign == '-' { -1 } else { 1 });
            if scanner.peek().is_fixed_number(4) {
                let hour_minute = scanner.next().number();
                if hour_minute / 100 > 23 || hour_minute % 100 > 59 {
                    return None;
                }
                zone.hour = Some(hour_minute / 100);
                zone.minute = Some(hour_minute % 100);
            } else {
                if !two_digits(scanner.peek(), 23) {
                    return None;
                }
                zone.hour = Some(scanner.next().number());
                if !scanner.skip_symbol(':') || !two_digits(scanner.peek(), 59) {
                    return None;
                }
                zone.minute = Some(scanner.next().number());
            }
        }
        if scanner.peek() != Token::End {
            return None;
        }
    }
    // A date alone is UTC; a date with a time and no zone is local time.
    if zone.hour.is_none() && time.parts.is_empty() {
        zone.set(0);
    }
    day.is_iso = true;
    Some(Token::End)
}

/// The fraction of a second as milliseconds: its first three digits, the rest cut off.
fn milliseconds(token: Token) -> i64 {
    let Token::Number { value, length } = token else {
        return 0;
    };
    match length {
        1 => value * 100,
        2 => value * 10,
        3 => value,
        _ => value / 10_i64.pow(u32::try_from(length.min(9) - 3).unwrap_or(0)),
    }
}

/// The days from 1970-01-01 to a civil date, in the proleptic Gregorian calendar.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let year_of_era = year.rem_euclid(400);
    let day_of_year = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

/// The civil date of a day counted from 1970-01-01.
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
    (year_of_era + era * 400 + i64::from(month <= 2), month, day)
}

#[cfg(test)]
mod tests {
    use brigade_core::Timestamp;

    use super::{iso, now_iso, now_seconds, parse};

    #[test]
    fn a_time_is_written_with_three_digits_of_milliseconds() {
        assert_eq!(iso(0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso(1_791_122_400_000), "2026-10-04T14:00:00.000Z");
        assert_eq!(iso(1_709_251_199_999), "2024-02-29T23:59:59.999Z");
        assert_eq!(iso(-1), "1969-12-31T23:59:59.999Z");
        assert_eq!(iso(8_640_000_000_000_000), "+275760-09-13T00:00:00.000Z");
        assert_eq!(iso(-62_198_755_200_000), "-000001-01-01T00:00:00.000Z");
        assert_eq!(
            now_iso(Timestamp(1_791_122_400_123_999_999)),
            "2026-10-04T14:00:00.123Z"
        );
        assert_eq!(now_seconds(Timestamp(1_700_000_010_999_999_999)), 1_700_000_010);
    }

    /// Each pair was read from `new Date(text).toISOString()` in V8 with `TZ=UTC`; `None` is an invalid
    /// date there.
    #[test]
    fn a_date_is_read_as_v8_reads_it() {
        let cases: &[(&str, Option<&str>)] = &[
            // What a mail's Date header holds.
            ("Sat, 4 Oct 2026 10:00:00 -0400", Some("2026-10-04T14:00:00.000Z")),
            ("Thu, 5 Apr 2012 16:59:01 +0200", Some("2012-04-05T14:59:01.000Z")),
            ("4 Oct 2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 GMT", Some("2026-10-04T10:00:00.000Z")),
            (
                "Sat, 4 Oct 2026 10:00:00 -0400 (EDT)",
                Some("2026-10-04T14:00:00.000Z"),
            ),
            ("Sat, 04 Oct 2026 10:00 +0200", Some("2026-10-04T08:00:00.000Z")),
            ("Sat, 4 Oct 26 10:00:00 EST", Some("2026-10-04T15:00:00.000Z")),
            ("Mon, 2 Jan 2006 15:04:05 MST", Some("2006-01-02T22:04:05.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 PDT", Some("2026-10-04T17:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 UT", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 Z", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 -0000", Some("2026-10-04T10:00:00.000Z")),
            (
                "Sun, 4 Oct 2026 10:00:00 +0000 (UTC)",
                Some("2026-10-04T10:00:00.000Z"),
            ),
            (
                "4 Oct 2026 10:00:00 +0000 (a (nested) comment)",
                Some("2026-10-04T10:00:00.000Z"),
            ),
            (
                "4 Oct 2026 10:00:00 (unterminated",
                Some("2026-10-04T10:00:00.000Z"),
            ),
            (
                "(comment) 4 Oct 2026 10:00:00 +0000",
                Some("2026-10-04T10:00:00.000Z"),
            ),
            (
                "  Sat, 4 Oct 2026 10:00:00 -0400  ",
                Some("2026-10-04T14:00:00.000Z"),
            ),
            ("Sat,4 Oct 2026 10:00:00 -0400", Some("2026-10-04T14:00:00.000Z")),
            (
                "Saturday, 4 Oct 2026 10:00:00 +0000",
                Some("2026-10-04T10:00:00.000Z"),
            ),
            (
                "Sat, 4 October 2026 10:00:00 +0100",
                Some("2026-10-04T09:00:00.000Z"),
            ),
            ("1 Sept 2026 00:00:00 Z", Some("2026-09-01T00:00:00.000Z")),
            ("4 oct 2026 10:00:00 gmt", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 1:02:03 -0400", Some("2026-10-04T05:02:03.000Z")),
            ("1 Mar 2024 00:00:00 +1400", Some("2024-02-29T10:00:00.000Z")),
            ("31 Dec 1969 23:59:59 +0000", Some("1969-12-31T23:59:59.000Z")),
            // No zone is local time, and local time is UTC.
            ("Sat, 4 Oct 2026 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            // Zones in their other spellings.
            (
                "Sat, 4 Oct 2026 10:00:00 +01:00",
                Some("2026-10-04T09:00:00.000Z"),
            ),
            (
                "Sat, 4 Oct 2026 10:00:00 GMT+0200",
                Some("2026-10-04T08:00:00.000Z"),
            ),
            ("4 Oct 2026 10:00:00 GMT+02:30", Some("2026-10-04T07:30:00.000Z")),
            ("4 Oct 2026 10:00:00 UTC-0130", Some("2026-10-04T11:30:00.000Z")),
            ("4 Oct 2026 10:00:00 EST+0100", Some("2026-10-04T09:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 +2", Some("2026-10-04T08:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 -08", Some("2026-10-04T18:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 +123", Some("2026-10-04T08:37:00.000Z")),
            ("4 Oct 2026 10:00:00+0000", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:00:00Z", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:00:00 +0000 GMT", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 +12345", None),
            // An offset's hour before a colon may be any number, and V8 adds it up in 32 bits.
            ("1970z10OctSat,+1234524:00:00", Some("1966-01-15T18:28:16.000Z")),
            ("Sat, 4 Oct 2026 10:00:00 - 0400", None),
            ("Sat, 4 Oct 2026 10:00:00 CEST", None),
            ("4 Oct 2026 10:00:00GMT", None),
            // The other orders of a date with a named month, and other separators.
            ("Oct 4 2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("Oct 4, 2026 10:00:00 UTC", Some("2026-10-04T10:00:00.000Z")),
            ("Sat Oct 4 10:00:00 2026", Some("2026-10-04T10:00:00.000Z")),
            (
                "Sat Oct 04 2026 10:00:00 GMT-0400 (Eastern Daylight Time)",
                Some("2026-10-04T14:00:00.000Z"),
            ),
            ("2026 Oct 4 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4-Oct-2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4.Oct.2026 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            ("4/Oct/2026 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            ("Oct-04-2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026, 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            // Dates without a month's name.
            ("10/4/2026 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            ("2026/10/04 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            ("2026-1-4", Some("2026-01-04T00:00:00.000Z")),
            // Years, and the parts that default to one.
            ("4 Oct 99 00:00:00 +0000", Some("1999-10-04T00:00:00.000Z")),
            ("4 Oct 49 00:00:00 +0000", Some("2049-10-04T00:00:00.000Z")),
            ("4 Oct 50 00:00:00 +0000", Some("1950-10-04T00:00:00.000Z")),
            ("4 Oct 0026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("1 Jan 100 00:00:00 +0000", Some("0100-01-01T00:00:00.000Z")),
            ("4 Oct 12026 10:00:00 +0000", Some("+012026-10-04T10:00:00.000Z")),
            ("1 Jan 275760 00:00:00 +0000", Some("+275760-01-01T00:00:00.000Z")),
            ("1 Jan 275761 00:00:00 +0000", None),
            ("4 Oct 2026", Some("2026-10-04T00:00:00.000Z")),
            ("Oct 2026", Some("2026-10-01T00:00:00.000Z")),
            ("4 Oct", Some("2001-10-04T00:00:00.000Z")),
            ("4", Some("2001-04-01T00:00:00.000Z")),
            ("4 4 Oct 2026", Some("2004-10-04T00:00:00.000Z")),
            ("Oct Oct 4 2026", Some("2026-10-04T00:00:00.000Z")),
            // Days and times at and past their ends.
            (
                "Sat, 31 Feb 2026 10:00:00 +0000",
                Some("2026-03-03T10:00:00.000Z"),
            ),
            ("32 Oct 2026 10:00:00 +0000", None),
            ("0 Oct 2026 10:00:00 +0000", None),
            ("Sat, 4 Oct 2026 24:00:00 +0000", Some("2026-10-05T00:00:00.000Z")),
            ("Sat, 4 Oct 2026 25:00:00 +0000", None),
            ("4 Oct 2026 10:60:00 +0000", None),
            ("4 Oct 2026 10:00:60 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:59:60 +0000", Some("2026-10-04T10:59:00.000Z")),
            ("4 Oct 2026 2027 10:00:00", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:00:00 10:00:00", None),
            ("4 Oct 2026 10 +0000", None),
            ("4 Oct 2026 :10", Some("2026-10-04T00:00:00.000Z")),
            ("4 Oct 2026 10:", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:00:", Some("2026-10-04T10:00:00.000Z")),
            ("Sat, 4 Oct 2026 10:0:0 -0400", Some("2026-10-04T14:00:00.000Z")),
            ("4 Oct 2026 10:00:00.5 +0000", Some("2026-10-04T10:00:00.500Z")),
            ("4 Oct 2026 10:00:00.1234 +0000", Some("2026-10-04T10:00:00.123Z")),
            ("4 Oct 2026 10:00:00 AM +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oct 2026 10:00:00 PM", Some("2026-10-04T22:00:00.000Z")),
            // Words that mean nothing.
            ("Foo, 4 Oct 2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("é 4 Oct 2026 10:00:00 +0000", Some("2026-10-04T10:00:00.000Z")),
            ("4 Octx 2026 10:00:00 GMT", Some("2026-10-04T10:00:00.000Z")),
            ("4 Oc 2026 10:00:00 GMT", None),
            ("4 Oct 2026 10:00:00 +0000 trailing", None),
            ("4 Oct 2026 10:00:00 Sat", None),
            ("4 Oct 2026 T10:00:00", None),
            ("4 Oct 2026T10:00:00Z", None),
            ("not a date", None),
            ("Sat", None),
            ("Oct", None),
            ("10:00:00", None),
            ("", None),
            // The ISO 8601 form.
            ("2026-10-04T14:00:00Z", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04t14:00:00z", Some("2026-10-04T14:00:00.000Z")),
            (
                "2026-10-04T14:00:00.8054242-05:00",
                Some("2026-10-04T19:00:00.805Z"),
            ),
            ("2026-10-04T14:00:00.123456789Z", Some("2026-10-04T14:00:00.123Z")),
            ("2026-10-04T14:00:00.1Z", Some("2026-10-04T14:00:00.100Z")),
            ("2026-10-04T14:00:00+0530", Some("2026-10-04T08:30:00.000Z")),
            ("2026-10-04T14:00:00-00:00", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04T14:00", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04T14:00Z", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04T24:00:00Z", Some("2026-10-05T00:00:00.000Z")),
            ("2026-10-04", Some("2026-10-04T00:00:00.000Z")),
            ("2026-10", Some("2026-10-01T00:00:00.000Z")),
            ("2026", Some("2026-01-01T00:00:00.000Z")),
            ("2026-02-31", Some("2026-03-03T00:00:00.000Z")),
            ("+002026-10-04T14:00:00Z", Some("2026-10-04T14:00:00.000Z")),
            ("-000001-01-01T00:00:00Z", Some("-000001-01-01T00:00:00.000Z")),
            ("+275760-09-13T00:00:00Z", Some("+275760-09-13T00:00:00.000Z")),
            ("+275760-09-13T00:00:01Z", None),
            ("-000000-01-01T00:00:00Z", None),
            ("2026-13-01", None),
            ("2026-10-04T14", None),
            ("2026-10-04T10:00:60Z", None),
            ("2026-10-04T14:00:00,5Z", None),
            ("2026-10-04T14:00:00.Z", None),
            ("2026-10-04T14:00:00+05", None),
            ("2026-10-04T14:00:00+5:30", None),
            ("2026-10-4T10:00:00Z", None),
            ("2026-10-04T14:00:00 +0000", None),
            ("2026-10-04T14:00:00 UTC", None),
            ("20261004T140000Z", None),
            // An ISO date with its time the older way.
            ("2026-10-04 14:00:00 +0000", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04 14:00:00", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04 14:00", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04 14:00:00Z", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04 14:00:00 UTC", Some("2026-10-04T14:00:00.000Z")),
            ("2026-10-04 14:00:00 GMT+0200", Some("2026-10-04T12:00:00.000Z")),
            ("2026-Oct-04 10:00:00", Some("2026-10-04T10:00:00.000Z")),
        ];
        for (text, expected) in cases {
            assert_eq!(parse(text).map(iso).as_deref(), *expected, "{text}");
        }
    }
}
