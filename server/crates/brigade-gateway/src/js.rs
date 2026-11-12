//! What the TypeScript took from its runtime, kept as JavaScript has it.
//!
//! The gateway's texts reach people, and the port keeps them byte for byte. That includes the places where
//! JavaScript's idea of a string is not Rust's: which characters are white space, how long a string is, how
//! a number is read and written, and how base64 and URI escapes are decoded. Nothing in this module is a
//! rule of the gateway; each function names the JavaScript it stands for.

use regex::Regex;

/// Whether `c` is white space to JavaScript: what `\s` matches in a regular expression and what `trim`
/// removes. Rust's `char::is_whitespace` differs by two characters. It has U+0085, which JavaScript does
/// not, and it lacks U+FEFF, the byte order mark a mail's text can begin with.
pub(crate) fn is_space(c: char) -> bool {
    matches!(
        c,
        '\t' | '\n' | '\u{0B}' | '\u{0C}' | '\r' | ' ' | '\u{A0}' | '\u{1680}' | '\u{2000}'
            ..='\u{200A}' | '\u{2028}' | '\u{2029}' | '\u{202F}' | '\u{205F}' | '\u{3000}' | '\u{FEFF}'
    )
}

/// The characters of [`is_space`], written as the inside of a character class of a pattern: `[{SPACE}]` is
/// JavaScript's `\s` and `[^{SPACE}]` its `\S`.
pub(crate) const SPACE: &str =
    r"\t\n\x0B\x0C\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}";

/// Whether `c` ends a line to JavaScript: the four characters `.` does not match in a regular expression.
pub(crate) fn is_line_terminator(c: char) -> bool {
    matches!(c, '\n' | '\r' | '\u{2028}' | '\u{2029}')
}

/// JavaScript's `.` as a pattern: any character that does not end a line. The `regex` crate's own `.` stops
/// at `\n` alone.
pub(crate) const DOT: &str = r"[^\n\r\x{2028}\x{2029}]";

/// Compiles one of this crate's patterns. Each is a constant of the source, so one that does not compile is
/// a mistake the first test of its module finds.
pub(crate) fn regex(pattern: &str) -> Regex {
    Regex::new(pattern).expect("a pattern of this crate compiles")
}

/// `String.prototype.trim`.
pub(crate) fn trim(text: &str) -> &str {
    text.trim_matches(is_space)
}

/// `String.prototype.trimEnd`, and `replace(/\s+$/, "")`.
pub(crate) fn trim_end(text: &str) -> &str {
    text.trim_end_matches(is_space)
}

/// `replace(/\r\n?/g, "\n")`: every line ending as a line feed.
pub(crate) fn unix_lines(text: &str) -> String {
    text.replace("\r\n", "\n").replace('\r', "\n")
}

/// `text.split(/\s+/)`: the pieces of `text` between its runs of white space. A text that begins or ends
/// with white space has an empty piece there, and an empty text is one empty piece.
pub(crate) fn split_space(text: &str) -> Vec<&str> {
    let mut pieces = Vec::new();
    let mut start = 0;
    let mut in_space = false;
    for (at, c) in text.char_indices() {
        let space = is_space(c);
        if space && !in_space {
            pieces.push(&text[start..at]);
        } else if !space && in_space {
            start = at;
        }
        in_space = space;
    }
    pieces.push(if in_space { "" } else { &text[start..] });
    pieces
}

/// JavaScript's `\w` in a pattern without the `u` and `i` flags: an ASCII letter, a digit or an underscore.
pub(crate) fn is_word(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_'
}

/// The characters `localeCompare` gives a weight of their own among the ASCII ones, lightest first; a
/// capital letter weighs what its small letter does. An ASCII character that is not here is a control
/// character, which has no weight at all.
const COLLATED: &str =
    "\t\n\u{0B}\u{0C}\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz";

/// `left.localeCompare(right)` where the runtime's language is English, whose order is the Unicode
/// collation's own: white space, then punctuation, then digits, then letters, a capital letter beside its
/// small one and after it only when nothing else tells the two texts apart.
///
/// This is that order for ASCII, which is exact. The collation's tables for everything else are not
/// here: a character outside ASCII is put after every ASCII one, by its code point, where the collation
/// would put `é` beside `e`.
pub(crate) fn locale_compare(left: &str, right: &str) -> std::cmp::Ordering {
    // A character's weight, and whether it is a capital letter.
    let weights = |text: &str| -> Vec<(usize, bool)> {
        text.chars()
            .filter_map(|c| {
                let weight = if c.is_ascii() {
                    COLLATED.find(c.to_ascii_lowercase())?
                } else {
                    COLLATED.len() + c as usize
                };
                Some((weight, c.is_ascii_uppercase()))
            })
            .collect()
    };
    let (left, right) = (weights(left), weights(right));
    let letters = left.iter().map(|w| w.0).cmp(right.iter().map(|w| w.0));
    letters.then_with(|| left.iter().map(|w| w.1).cmp(right.iter().map(|w| w.1)))
}

/// `new URLSearchParams(text)`: the names and values of a form body, in order. A `+` is a space, a percent
/// escape is a byte, and what the bytes spell is read as UTF-8, leniently; an escape that is not two
/// hexadecimal digits stays as it is written. One `?` at the very start is not part of the first name.
pub(crate) fn form_pairs(text: &str) -> Vec<(String, String)> {
    text.strip_prefix('?')
        .unwrap_or(text)
        .split('&')
        .filter(|pair| !pair.is_empty())
        .map(|pair| {
            let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
            (form_decode(name), form_decode(value))
        })
        .collect()
}

fn form_decode(text: &str) -> String {
    let source = text.as_bytes();
    let mut bytes = Vec::with_capacity(source.len());
    let mut at = 0;
    while at < source.len() {
        let escaped = source.get(at + 1..at + 3).and_then(|digits| {
            let high = char::from(digits[0]).to_digit(16)?;
            let low = char::from(digits[1]).to_digit(16)?;
            u8::try_from(high * 16 + low).ok()
        });
        match (source[at], escaped) {
            (b'%', Some(byte)) => {
                bytes.push(byte);
                at += 3;
            }
            (b'+', _) => {
                bytes.push(b' ');
                at += 1;
            }
            (byte, _) => {
                bytes.push(byte);
                at += 1;
            }
        }
    }
    String::from_utf8_lossy(&bytes).into_owned()
}

/// `String.prototype.length`: the UTF-16 code units of `text`. A character outside the Basic Multilingual
/// Plane counts twice.
pub(crate) fn utf16_len(text: &str) -> usize {
    text.chars().map(char::len_utf16).sum()
}

/// `slice(0, units)`: the start of `text`, at most `units` UTF-16 code units long. Where JavaScript would
/// cut a character outside the Basic Multilingual Plane in half, the whole character is left out: half of
/// one is not text Rust can hold.
pub(crate) fn utf16_prefix(text: &str, units: usize) -> &str {
    let mut taken = 0;
    for (at, c) in text.char_indices() {
        taken += c.len_utf16();
        if taken > units {
            return &text[..at];
        }
    }
    text
}

/// `slice(-units)`: the end of `text`, at most `units` UTF-16 code units long, with the same rule as
/// [`utf16_prefix`] for a character that would be cut in half.
pub(crate) fn utf16_suffix(text: &str, units: usize) -> &str {
    let mut taken = 0;
    for (at, c) in text.char_indices().rev() {
        taken += c.len_utf16();
        if taken > units {
            return &text[at + c.len_utf8()..];
        }
    }
    text
}

/// `Number(text)`: the number a string spells, NaN when it spells none. White space around it is ignored,
/// an empty string is zero, and `0x`, `0o` and `0b` introduce the other radixes.
pub(crate) fn number(text: &str) -> f64 {
    let text = trim(text);
    if text.is_empty() {
        return 0.0;
    }
    for (prefix, radix) in [("0x", 16), ("0X", 16), ("0o", 8), ("0O", 8), ("0b", 2), ("0B", 2)] {
        if let Some(digits) = text.strip_prefix(prefix) {
            if digits.is_empty() {
                return f64::NAN;
            }
            let mut value = 0.0_f64;
            for c in digits.chars() {
                let Some(digit) = c.to_digit(radix) else {
                    return f64::NAN;
                };
                value = value * f64::from(radix) + f64::from(digit);
            }
            return value;
        }
    }
    let (sign, digits) = match text.as_bytes()[0] {
        b'+' => (1.0, &text[1..]),
        b'-' => (-1.0, &text[1..]),
        _ => (1.0, text),
    };
    if digits == "Infinity" {
        return sign * f64::INFINITY;
    }
    if !is_decimal_literal(digits) {
        return f64::NAN;
    }
    sign * digits.parse::<f64>().unwrap_or(f64::NAN)
}

/// Whether `text` is JavaScript's unsigned decimal literal: digits with an optional fraction and exponent,
/// and at least one digit before the exponent. Rust's own parser also reads `inf` and `nan`, which
/// JavaScript does not.
fn is_decimal_literal(text: &str) -> bool {
    let digits = |part: &str| part.bytes().all(|b| b.is_ascii_digit());
    let (mantissa, exponent) = match text.find(['e', 'E']) {
        Some(at) => (&text[..at], Some(&text[at + 1..])),
        None => (text, None),
    };
    let (whole, fraction) = mantissa.split_once('.').unwrap_or((mantissa, ""));
    if !digits(whole) || !digits(fraction) || (whole.is_empty() && fraction.is_empty()) {
        return false;
    }
    exponent.is_none_or(|exponent| {
        let exponent = exponent.strip_prefix(['+', '-']).unwrap_or(exponent);
        !exponent.is_empty() && digits(exponent)
    })
}

/// `String(number)`: a number as JavaScript writes it. The digits are the shortest that read back as the
/// same number, laid out plainly from 1e-6 up to 1e21 and with an exponent outside that.
pub(crate) fn number_text(number: f64) -> String {
    if number.is_nan() {
        return "NaN".to_owned();
    }
    if number == 0.0 {
        return "0".to_owned();
    }
    let sign = if number < 0.0 { "-" } else { "" };
    if number.is_infinite() {
        return format!("{sign}Infinity");
    }
    // Rust's exponent form has the same shortest digits: `1.2345e2` is the digits 12345 with the point
    // after the third.
    let exponent_form = format!("{:e}", number.abs());
    let (mantissa, exponent) = exponent_form.split_once('e').unwrap_or((&exponent_form, "0"));
    let digits: String = mantissa.chars().filter(|c| *c != '.').collect();
    let count = i32::try_from(digits.len()).unwrap_or(i32::MAX);
    let point = exponent.parse::<i32>().unwrap_or(0) + 1;
    let body = if count <= point && point <= 21 {
        format!(
            "{digits}{}",
            "0".repeat(usize::try_from(point - count).unwrap_or(0))
        )
    } else if 0 < point && point <= 21 {
        let (whole, fraction) = digits.split_at(usize::try_from(point).unwrap_or(0));
        format!("{whole}.{fraction}")
    } else if -6 < point && point <= 0 {
        format!("0.{}{digits}", "0".repeat(usize::try_from(-point).unwrap_or(0)))
    } else {
        let exponent = point - 1;
        let exponent_sign = if exponent < 0 { '-' } else { '+' };
        let (first, rest) = digits.split_at(1);
        let fraction = if rest.is_empty() {
            String::new()
        } else {
            format!(".{rest}")
        };
        format!("{first}{fraction}e{exponent_sign}{}", exponent.abs())
    };
    format!("{sign}{body}")
}

const BASE64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// `atob`: the bytes of base64 text, read as the HTML standard's forgiving decoder reads it. ASCII white
/// space is ignored, the padding may be missing, and anything else outside the alphabet is no base64.
pub(crate) fn atob(text: &str) -> Option<Vec<u8>> {
    let mut data: Vec<u8> = text
        .bytes()
        .filter(|b| !matches!(b, b'\t' | b'\n' | 0x0C | b'\r' | b' '))
        .collect();
    if data.len().is_multiple_of(4) {
        if data.ends_with(b"==") {
            data.truncate(data.len() - 2);
        } else if data.ends_with(b"=") {
            data.truncate(data.len() - 1);
        }
    }
    if data.len() % 4 == 1 {
        return None;
    }
    let mut bytes = Vec::with_capacity(data.len() / 4 * 3 + 2);
    let (mut buffer, mut bits) = (0_u32, 0_u32);
    for symbol in data {
        let value = BASE64.iter().position(|b| *b == symbol)?;
        buffer = (buffer << 6) | u32::try_from(value).ok()?;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            bytes.push(u8::try_from(buffer >> bits).ok()?);
            buffer &= (1 << bits) - 1;
        }
    }
    Some(bytes)
}

/// `btoa` over bytes: base64 with its padding.
pub(crate) fn btoa(bytes: &[u8]) -> String {
    let mut text = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let group = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let symbols = [
            group[0] >> 2,
            ((group[0] & 0x03) << 4) | (group[1] >> 4),
            ((group[1] & 0x0f) << 2) | (group[2] >> 6),
            group[2] & 0x3f,
        ];
        for (at, symbol) in symbols.iter().enumerate() {
            if at <= chunk.len() {
                text.push(char::from(BASE64[usize::from(*symbol)]));
            } else {
                text.push('=');
            }
        }
    }
    text
}

/// The string `atob` returns for `bytes`: one character per byte, the byte's value its code point. A
/// password that is not ASCII therefore reads as Latin-1, never as UTF-8.
pub(crate) fn binary_string(bytes: &[u8]) -> String {
    bytes.iter().map(|b| char::from(*b)).collect()
}

/// `decodeURIComponent`: `text` with its percent escapes read as UTF-8. `None` where JavaScript throws: an
/// escape that is not two hexadecimal digits, or escapes that are not UTF-8.
pub(crate) fn decode_uri_component(text: &str) -> Option<String> {
    let source = text.as_bytes();
    let mut bytes = Vec::with_capacity(source.len());
    let mut at = 0;
    while at < source.len() {
        if source[at] == b'%' {
            let digits = source.get(at + 1..at + 3)?;
            let high = char::from(digits[0]).to_digit(16)?;
            let low = char::from(digits[1]).to_digit(16)?;
            bytes.push(u8::try_from(high * 16 + low).ok()?);
            at += 3;
        } else {
            bytes.push(source[at]);
            at += 1;
        }
    }
    String::from_utf8(bytes).ok()
}

/// `encodeURIComponent`: `text` with every byte escaped but the letters, the digits and `-_.!~*'()`.
pub(crate) fn encode_uri_component(text: &str) -> String {
    let mut encoded = String::with_capacity(text.len());
    for byte in text.bytes() {
        if byte.is_ascii_alphanumeric() || b"-_.!~*'()".contains(&byte) {
            encoded.push(char::from(byte));
        } else {
            encoded.push_str(&format!("%{byte:02X}"));
        }
    }
    encoded
}

#[cfg(test)]
mod tests {
    use std::cmp::Ordering;

    use super::{
        atob, binary_string, btoa, decode_uri_component, encode_uri_component, form_pairs, is_space,
        locale_compare, number, number_text, split_space, trim, utf16_len, utf16_prefix, utf16_suffix,
    };

    #[test]
    fn white_space_is_javascripts_set_and_not_rusts() {
        // The byte order mark is white space to JavaScript and not to Rust; U+0085 is the reverse.
        assert!(is_space('\u{FEFF}') && !'\u{FEFF}'.is_whitespace());
        assert!(!is_space('\u{85}') && '\u{85}'.is_whitespace());
        assert_eq!(trim("\u{FEFF} to: 3f9a2\u{2028}\u{A0}"), "to: 3f9a2");
        assert_eq!(trim("\u{85}x\u{85}"), "\u{85}x\u{85}");
    }

    #[test]
    fn a_text_is_split_at_every_run_of_javascripts_white_space() {
        assert_eq!(
            split_space("send 3f9a2  hi\tthere"),
            ["send", "3f9a2", "hi", "there"]
        );
        // A run at either end leaves an empty piece there, and an empty text is one empty piece.
        assert_eq!(split_space(" a b "), ["", "a", "b", ""]);
        assert_eq!(split_space(""), [""]);
        assert_eq!(split_space(" \n"), ["", ""]);
        // The byte order mark splits and U+0085 does not.
        assert_eq!(split_space("a\u{FEFF}b\u{85}c\u{A0}d"), ["a", "b\u{85}c", "d"]);
    }

    #[test]
    fn texts_are_ordered_as_the_collation_orders_them_and_not_by_their_bytes() {
        fn sorted(mut names: Vec<&str>) -> Vec<&str> {
            names.sort_by(|x, y| locale_compare(x, y));
            names
        }
        // Letters without regard to case, and a small letter before its capital when nothing else differs.
        assert_eq!(
            sorted(vec!["Zulu", "alpha", "Mike", "Alpha", "ALPHA", "alpha"]),
            ["alpha", "alpha", "Alpha", "ALPHA", "Mike", "Zulu"]
        );
        assert_eq!(sorted(vec!["aB", "Ab", "ab", "AB"]), ["ab", "aB", "Ab", "AB"]);
        // White space, then punctuation, then digits, then letters; a shorter text before a longer one.
        assert_eq!(
            sorted(vec!["a1", "ab", "a b", "a-b", "a_b", "a", "a.b", "a$b", "a<b"]),
            ["a", "a b", "a_b", "a-b", "a.b", "a<b", "a$b", "a1", "ab"]
        );
        // Digits are characters, not numbers.
        assert_eq!(sorted(vec!["team 2", "team 10"]), ["team 10", "team 2"]);
        // A control character has no weight: the two texts are the same to the collation.
        assert_eq!(locale_compare("a\u{1}b", "ab"), Ordering::Equal);
        assert_eq!(locale_compare("", "\u{7F}"), Ordering::Equal);
        assert_eq!(locale_compare("a", "A"), Ordering::Less);
        assert_eq!(locale_compare("B", "a"), Ordering::Greater);
        // Outside ASCII the port has no table: such a character follows every ASCII one.
        assert_eq!(locale_compare("é", "z"), Ordering::Greater);
        assert_eq!(locale_compare("é", "é"), Ordering::Equal);
    }

    #[test]
    fn a_form_is_read_as_url_search_params_reads_it() {
        let pairs = |text: &str| -> Vec<(String, String)> { form_pairs(text) };
        let pair = |name: &str, value: &str| (name.to_owned(), value.to_owned());
        assert_eq!(
            pairs("command=%2Fbrigade&text=send+3f9a2+a%2Bb&x"),
            [
                pair("command", "/brigade"),
                pair("text", "send 3f9a2 a+b"),
                pair("x", "")
            ]
        );
        // One question mark at the start is not part of the first name; empty pairs are none.
        assert_eq!(pairs("?a=1&&b=2&"), [pair("a", "1"), pair("b", "2")]);
        assert_eq!(pairs("??a=1"), [pair("?a", "1")]);
        // A value keeps what follows its first equals sign.
        assert_eq!(
            pairs("a=b=c&=v&="),
            [pair("a", "b=c"), pair("", "v"), pair("", "")]
        );
        // An escape that is no escape stays as written, and bytes that are not UTF-8 are replaced.
        assert_eq!(pairs("a=%zz%4&b=%"), [pair("a", "%zz%4"), pair("b", "%")]);
        assert_eq!(
            pairs("a=%C3%A9&b=%E9&c=%F0%9F"),
            [pair("a", "é"), pair("b", "\u{FFFD}"), pair("c", "\u{FFFD}")]
        );
        assert_eq!(pairs(""), []);
    }

    #[test]
    fn a_length_is_counted_in_utf16_code_units() {
        assert_eq!(utf16_len("José"), 4);
        assert_eq!(utf16_len("a🙂"), 3);
        assert_eq!(utf16_prefix("abcdefghij", 8), "abcdefgh");
        assert_eq!(utf16_prefix("abc", 8), "abc");
        assert_eq!(utf16_prefix("abcdefg🙂", 8), "abcdefg");
        assert_eq!(utf16_suffix("0123456789", 5), "56789");
        assert_eq!(utf16_suffix("abc", 5), "abc");
        assert_eq!(utf16_suffix("🙂abcd", 5), "abcd");
        assert_eq!(utf16_suffix("a🙂bcd", 5), "🙂bcd");
    }

    #[test]
    fn a_number_is_read_as_number_reads_it() {
        assert_eq!(number("1700000000"), 1_700_000_000.0);
        assert_eq!(number(" 12.5 "), 12.5);
        assert_eq!(number(""), 0.0);
        assert_eq!(number("0x10"), 16.0);
        assert_eq!(number("0b101"), 5.0);
        assert_eq!(number("-1e3"), -1000.0);
        assert_eq!(number(".5"), 0.5);
        assert_eq!(number("5."), 5.0);
        assert_eq!(number("-Infinity"), f64::NEG_INFINITY);
        for not_a_number in [
            "abc",
            "1700000000s",
            "1 2",
            "inf",
            "nan",
            "infinity",
            "-0x10",
            ".",
            "e5",
            "1e",
            "0x",
        ] {
            assert!(number(not_a_number).is_nan(), "{not_a_number}");
        }
    }

    #[test]
    fn a_number_is_written_as_string_writes_it() {
        for (number, text) in [
            (0.0, "0"),
            (-0.0, "0"),
            (406.0, "406"),
            (-7.0, "-7"),
            (406.5, "406.5"),
            (0.1, "0.1"),
            (1e21, "1e+21"),
            (1e20, "100000000000000000000"),
            (123_456_789_012_345_680_000.0, "123456789012345680000"),
            (1.5e-7, "1.5e-7"),
            (0.000_001, "0.000001"),
            (1e-7, "1e-7"),
            (1.234e25, "1.234e+25"),
            (f64::INFINITY, "Infinity"),
        ] {
            assert_eq!(number_text(number), text);
        }
    }

    #[test]
    fn base64_is_decoded_forgivingly_and_encoded_with_padding() {
        assert_eq!(atob("YnJpZ2FkZTpzZWNyZXQ="), Some(b"brigade:secret".to_vec()));
        assert_eq!(atob("YnJpZ2FkZTpzZWNyZXQ"), Some(b"brigade:secret".to_vec()));
        assert_eq!(atob(" YQ==\n"), Some(b"a".to_vec()));
        assert_eq!(atob(""), Some(Vec::new()));
        assert_eq!(atob("not-base64!"), None);
        assert_eq!(atob("YQ=a"), None);
        assert_eq!(atob("abcde"), None);
        for bytes in [&b""[..], b"a", b"ab", b"abc", b"abcd", &[0xff, 0xfe, 0x00]] {
            assert_eq!(atob(&btoa(bytes)).as_deref(), Some(bytes));
        }
        assert_eq!(btoa(b"brigade:secret"), "YnJpZ2FkZTpzZWNyZXQ=");
        assert_eq!(btoa(b"a"), "YQ==");
        assert_eq!(binary_string(&[0x61, 0xe9]), "aé");
    }

    #[test]
    fn uri_escapes_are_utf8_and_a_broken_one_is_refused() {
        assert_eq!(decode_uri_component("a%20b%C3%A9").as_deref(), Some("a bé"));
        assert_eq!(decode_uri_component("plain").as_deref(), Some("plain"));
        assert_eq!(decode_uri_component("%"), None);
        assert_eq!(decode_uri_component("%zz"), None);
        assert_eq!(decode_uri_component("%C3"), None);
        assert_eq!(decode_uri_component("%C3é"), None);
        assert_eq!(encode_uri_component("re_1"), "re_1");
        assert_eq!(encode_uri_component("a/b c+é"), "a%2Fb%20c%2B%C3%A9");
        assert_eq!(encode_uri_component("-_.!~*'()"), "-_.!~*'()");
    }
}
