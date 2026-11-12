//! The directives at the top of a text for a gateway, and the envelope block a gateway writes
//! (`supabase/functions/_shared/block.ts`).
//!
//! Three forms are read, all at the very top of the text (blank lines before them are fine):
//!
//! ```text
//! to: 3f9a2                        one line, the common case
//!
//! to: 3f9a2                        header lines: `key: value` for the keys of HEADER_KEYS, ended by a
//! summary: Login blank on Safari   blank line or by the first line that is not one of them
//!
//! [brigade]                        the block: `key: value` lines between `[brigade]` and `[/brigade]`.
//! to: 3f9a2                        Any key is allowed and an unknown one is ignored. The gateway writes
//! [/brigade]                       this form on outbound mail, where it must survive quoting and forwarding
//! ```
//!
//! In every form a value runs to the end of its line, and a key named twice is an error the gateway reports
//! back to whoever wrote the text. A key reads in any case: a phone capitalises the first letter of a line,
//! so `To: 3f9a2` must read as `to:`.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::LazyLock;

use regex::Regex;

use crate::js;
use crate::text::one_line;

/// The line that opens a block.
pub const OPEN: &str = "[brigade]";
/// The line that closes a block.
pub const CLOSE: &str = "[/brigade]";

/// The keys a header line may use, in any case. Only these count, so a word with a colon at the start of
/// someone's prose (`note: urgent`) ends the headers and stays in the body.
pub const HEADER_KEYS: [&str; 5] = ["to", "command", "summary", "subject", "reply-to"];

/// The fields of a block or of header lines, by key in lowercase.
pub type Fields = BTreeMap<String, String>;

/// `/^([A-Za-z][A-Za-z0-9-]*):\s?(.*)$/`, over one trimmed line.
static KEY: LazyLock<Regex> = LazyLock::new(|| {
    js::regex(&format!(
        r"^([A-Za-z][A-Za-z0-9-]*):[{s}]?({dot}*)$",
        s = js::SPACE,
        dot = js::DOT
    ))
});

/// `/^\s*(?:brigade\s+)?(sessions|help)\s*$/i`.
static BARE_COMMAND: LazyLock<Regex> = LazyLock::new(|| {
    js::regex(&format!(
        r"^[{s}]*(?:(?i-u:brigade)[{s}]+)?(?i-u:(sessions|help))[{s}]*$",
        s = js::SPACE
    ))
});

/// A malformed block or malformed header lines. Its text is what the gateway tells the writer.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BlockError {
    /// A line inside a block that is not `key: value`. `line` counts from one at the first line of the text
    /// that is not blank, so the blank lines before a block are not counted.
    NotKeyValue {
        /// The number of the line.
        line: usize,
    },
    /// A block that names one key twice.
    NamedTwice {
        /// The key, in lowercase.
        key: String,
    },
    /// Header lines that name one key twice.
    HeaderTwice {
        /// The key, in lowercase.
        key: String,
    },
}

impl fmt::Display for BlockError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotKeyValue { line } => {
                write!(
                    formatter,
                    "line {line} inside the [brigade] block is not \"key: value\""
                )
            }
            Self::NamedTwice { key } => write!(formatter, "the [brigade] block names \"{key}\" twice"),
            Self::HeaderTwice { key } => {
                write!(
                    formatter,
                    "the line \"{key}:\" appears twice at the top of the text"
                )
            }
        }
    }
}

impl std::error::Error for BlockError {}

/// One block found in a text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Span {
    /// The block's fields.
    pub fields: Fields,
    /// The index of the opening line.
    pub start: usize,
    /// The index of the closing line.
    pub end: usize,
}

/// The directives at the top of a text and what is left below them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Leading {
    /// The directives, `None` when the text has none.
    pub fields: Option<Fields>,
    /// The text below the directives, its line endings made `\n`; the whole text, untouched, when it has
    /// no directives.
    pub rest: String,
}

/// Writes a block. A value is made one line, and a field with no value or an empty one is left out.
#[must_use]
pub fn render_block(fields: &[(&str, Option<&str>)]) -> String {
    let mut lines = vec![OPEN.to_owned()];
    for (key, value) in fields {
        if let Some(value) = value.filter(|value| !value.is_empty()) {
            lines.push(format!("{key}: {}", one_line(value)));
        }
    }
    lines.push(CLOSE.to_owned());
    lines.join("\n")
}

/// Puts a backslash before every line of untrusted text that would open or close a block, so a body can
/// never forge one (the frame sanitises its body for the same reason).
#[must_use]
pub fn escape_block_lines(text: &str) -> String {
    text.split('\n')
        .map(|line| {
            let trimmed = js::trim(line);
            if trimmed == OPEN || trimmed == CLOSE {
                format!("\\{line}")
            } else {
                line.to_owned()
            }
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Every well-formed block in `lines`, in order. A block that is never closed is not a block, and nothing
/// after it is read.
///
/// # Errors
/// A line inside a block that is not `key: value`, or a key the block names twice. The key `constructor`
/// is refused as named twice at its first use: the TypeScript kept the fields in a plain object and asked
/// `key in fields`, which is also true of what every object inherits, and `constructor` is the one such
/// name a key can spell.
pub fn find_blocks(lines: &[&str]) -> Result<Vec<Span>, BlockError> {
    let mut blocks = Vec::new();
    let mut at = 0;
    while at < lines.len() {
        if js::trim(lines[at]) != OPEN {
            at += 1;
            continue;
        }
        let Some(end) = (at + 1..lines.len()).find(|&index| js::trim(lines[index]) == CLOSE) else {
            break;
        };
        let mut fields = Fields::new();
        for (index, line) in lines.iter().enumerate().take(end).skip(at + 1) {
            let line = js::trim(line);
            if line.is_empty() {
                continue;
            }
            let Some((key, value)) = key_value(line) else {
                return Err(BlockError::NotKeyValue { line: index + 1 });
            };
            if key == "constructor" || fields.contains_key(&key) {
                return Err(BlockError::NamedTwice { key });
            }
            fields.insert(key, value);
        }
        blocks.push(Span {
            fields,
            start: at,
            end,
        });
        at = end + 1;
    }
    Ok(blocks)
}

/// Reads the directives at the top of a text: a block as the first thing in it, else one or more header
/// lines ([`HEADER_KEYS`] only), ended by a blank line or by the first line that is not a header.
///
/// # Errors
/// A key named twice, or a malformed line inside a block. Every block of the text is read before the first
/// is returned, so a malformed block further down fails the text too.
pub fn parse_leading(text: &str) -> Result<Leading, BlockError> {
    let unix = js::unix_lines(text);
    let lines: Vec<&str> = unix.split('\n').collect();
    let untouched = || Leading {
        fields: None,
        rest: text.to_owned(),
    };
    let Some(first) = lines.iter().position(|line| !js::trim(line).is_empty()) else {
        return Ok(untouched());
    };
    if js::trim(lines[first]) == OPEN {
        let blocks = find_blocks(&lines[first..])?;
        let Some(block) = blocks.into_iter().next().filter(|block| block.start == 0) else {
            return Ok(untouched());
        };
        return Ok(Leading {
            fields: Some(block.fields),
            rest: trim_leading_blank(&lines[first + block.end + 1..]),
        });
    }
    let mut fields = Fields::new();
    let mut at = first;
    while at < lines.len() {
        let Some((key, value)) = key_value(js::trim(lines[at])) else {
            break;
        };
        if !HEADER_KEYS.contains(&key.as_str()) {
            break;
        }
        if fields.contains_key(&key) {
            return Err(BlockError::HeaderTwice { key });
        }
        fields.insert(key, value);
        at += 1;
    }
    if at == first {
        return Ok(untouched());
    }
    Ok(Leading {
        fields: Some(fields),
        rest: trim_leading_blank(&lines[at..]),
    })
}

/// Reads a text that is only a command word: `sessions` or `help`, with or without `brigade` in front, in
/// any case. It returns the command in lowercase.
#[must_use]
pub fn is_bare_command(text: &str) -> Option<&'static str> {
    let word = BARE_COMMAND.captures(text)?.get(1)?.as_str();
    Some(if word.eq_ignore_ascii_case("sessions") {
        "sessions"
    } else {
        "help"
    })
}

/// The fields of the last well-formed block in a text. It never fails: the text is the quoted copy of a
/// mail the gateway sent, and a block damaged in quoting is simply not there.
#[must_use]
pub fn last_block(text: &str) -> Option<Fields> {
    let unix = js::unix_lines(text);
    let lines: Vec<&str> = unix.split('\n').collect();
    find_blocks(&lines).ok()?.pop().map(|block| block.fields)
}

/// The key, in lowercase, and the trimmed value of a trimmed `key: value` line.
fn key_value(line: &str) -> Option<(String, String)> {
    let found = KEY.captures(line)?;
    Some((found[1].to_ascii_lowercase(), js::trim(&found[2]).to_owned()))
}

fn trim_leading_blank(lines: &[&str]) -> String {
    let first = lines
        .iter()
        .position(|line| !js::trim(line).is_empty())
        .unwrap_or(lines.len());
    lines[first..].join("\n")
}

#[cfg(test)]
mod tests {
    use super::{BlockError, find_blocks, is_bare_command, last_block, parse_leading};

    #[test]
    fn white_space_around_a_directive_is_javascripts() {
        // A mail's text can begin with a byte order mark, which JavaScript trims and Rust does not.
        let bom = parse_leading("\u{FEFF}to: 3f9a2\nhello").unwrap();
        assert_eq!(bom.fields.unwrap()["to"], "3f9a2");
        assert_eq!(bom.rest, "hello");
        // U+0085 is white space to Rust and not to JavaScript: the line is not a header.
        assert_eq!(parse_leading("\u{85}to: 3f9a2\nhello").unwrap().fields, None);
        // One white space after the colon is the separator, of any kind; the value is trimmed after it.
        let spaced = parse_leading("to:\u{A0}\u{A0}3f9a2 \t\nhello").unwrap();
        assert_eq!(spaced.fields.unwrap()["to"], "3f9a2");
        assert_eq!(parse_leading("to:3f9a2").unwrap().fields.unwrap()["to"], "3f9a2");
    }

    #[test]
    fn a_value_ends_at_its_line_and_a_line_separator_inside_one_is_not_a_value() {
        // `.` does not match U+2028 in JavaScript: the line is not `key: value`.
        assert_eq!(parse_leading("to: abc\u{2028}def\nhello").unwrap().fields, None);
        assert_eq!(
            find_blocks(&["[brigade]", "to: abc\u{2029}def", "[/brigade]"]),
            Err(BlockError::NotKeyValue { line: 2 })
        );
        // Right after the colon it is the one white space the pattern allows there.
        assert_eq!(
            parse_leading("to:\u{2028}abc\nhello").unwrap().fields.unwrap()["to"],
            "abc"
        );
        // A lone carriage return is a line ending to parse_leading and stays inside the line for find_blocks.
        assert_eq!(
            parse_leading("to: a\rsummary: b\rtext")
                .unwrap()
                .fields
                .unwrap()
                .len(),
            2
        );
        assert_eq!(
            find_blocks(&["[brigade]", "to: a\rb", "[/brigade]"]),
            Err(BlockError::NotKeyValue { line: 2 })
        );
    }

    #[test]
    fn a_key_is_ascii_letters_digits_and_hyphens_after_a_letter() {
        for line in ["1to: x", "-to: x", "t o: x", "tö: x", "to : x", ": x", "to"] {
            assert_eq!(parse_leading(line).unwrap().fields, None, "{line}");
        }
        let fields = parse_leading("[brigade]\nReply-To: m1\nx-1: y\nurl: http://a:b\nempty:\n[/brigade]")
            .unwrap()
            .fields
            .unwrap();
        assert_eq!(fields["reply-to"], "m1");
        assert_eq!(fields["x-1"], "y");
        assert_eq!(fields["url"], "http://a:b");
        assert_eq!(fields["empty"], "");
    }

    #[test]
    fn the_key_constructor_is_refused_as_the_typescript_refuses_it() {
        let error = parse_leading("[brigade]\nConstructor: x\n[/brigade]").unwrap_err();
        assert_eq!(
            error.to_string(),
            "the [brigade] block names \"constructor\" twice"
        );
        assert_eq!(last_block("[brigade]\nconstructor: x\n[/brigade]"), None);
        // As a header line it is no header key, so it is prose.
        assert_eq!(parse_leading("constructor: x").unwrap().fields, None);
    }

    #[test]
    fn the_line_number_of_a_malformed_line_counts_from_the_blocks_first_line() {
        let error = parse_leading("\n\n[brigade]\nto: a\nnot a field\n[/brigade]").unwrap_err();
        assert_eq!(error, BlockError::NotKeyValue { line: 3 });
        assert_eq!(
            error.to_string(),
            "line 3 inside the [brigade] block is not \"key: value\""
        );
    }

    #[test]
    fn a_malformed_block_further_down_fails_the_whole_text() {
        let text = "[brigade]\nto: a\n[/brigade]\nbody\n[brigade]\nnot a field\n[/brigade]";
        assert_eq!(parse_leading(text), Err(BlockError::NotKeyValue { line: 6 }));
        // After header lines the body is not read for blocks.
        let headers = parse_leading("to: a\n\n[brigade]\nnot a field\n[/brigade]").unwrap();
        assert_eq!(headers.rest, "[brigade]\nnot a field\n[/brigade]");
    }

    #[test]
    fn a_bare_command_is_matched_in_ascii_case_only() {
        assert_eq!(is_bare_command("BRIGADE\u{A0}\thelp\u{FEFF}"), Some("help"));
        assert_eq!(is_bare_command("brigadesessions"), None);
        assert_eq!(is_bare_command("brigade"), None);
        assert_eq!(is_bare_command("brigade brigade help"), None);
        // U+017F folds to `s` under Unicode's rules and not under JavaScript's without the `u` flag.
        assert_eq!(is_bare_command("\u{17F}essions"), None);
        assert_eq!(is_bare_command("help\u{85}"), None);
    }
}
