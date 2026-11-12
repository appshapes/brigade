//! The text rules every gateway shares: one line, an excerpt, a cut to a number of characters and a cut to
//! a number of bytes.
//!
//! They come from `supabase/functions/_shared/gateway.ts`, whose `excerpt` the Slack gateway imports and
//! whose `oneLine`, `cutChars` and `truncateUTF8` it keeps copies of. Each limit counts what the protocol
//! counts: a summary is capped in code points and a body in bytes of UTF-8 (BAP/1 4.4.1).

use crate::js;

/// How long an excerpt is when the caller has no reason for another length: a subject line's worth.
pub const EXCERPT_CHARS: usize = 60;

/// `text` on one line: every run of line breaks becomes one space, and the ends are trimmed. A subject, a
/// description and a block's value are written this way, so none of them can start a new line.
#[must_use]
pub fn one_line(text: &str) -> String {
    let mut line = String::with_capacity(text.len());
    let mut in_break = false;
    for c in text.chars() {
        let is_break = c == '\r' || c == '\n';
        if !is_break {
            line.push(c);
        } else if !in_break {
            line.push(' ');
        }
        in_break = is_break;
    }
    js::trim(&line).to_owned()
}

/// `text` cut to at most `limit` code points; a text that is cut ends in `…`, which is one of the `limit`.
#[must_use]
pub fn cut_chars(text: &str, limit: usize) -> String {
    let count = text.chars().count();
    if count <= limit {
        return text.to_owned();
    }
    // A limit of zero keeps all but the last character: that is what JavaScript's `slice(0, -1)` does, and
    // no caller asks for it.
    let kept = if limit == 0 { count - 1 } else { limit - 1 };
    let mut cut: String = text.chars().take(kept).collect();
    cut.push('…');
    cut
}

/// The first line of `text` that is not blank, trimmed and cut to `limit` code points; `message` when there
/// is no such line. It is what a mail's subject and a thread's label are made from.
#[must_use]
pub fn excerpt(text: &str, limit: usize) -> String {
    let first = text
        .split('\n')
        .map(js::trim)
        .find(|line| !line.is_empty())
        .unwrap_or("");
    let cut = cut_chars(first, limit);
    if cut.is_empty() { "message".to_owned() } else { cut }
}

/// `text` if it fits `budget` bytes of UTF-8, else as many whole characters of it as fit beside `marker`,
/// then `marker`. A budget of nothing is the empty string.
///
/// The marker itself is never cut: a budget smaller than the marker returns the marker whole, which is
/// longer than the budget. The TypeScript does the same, and a mail whose subject alone fills the body cap
/// is the only way to get there.
#[must_use]
pub fn truncate_utf8(text: &str, budget: usize, marker: &str) -> String {
    if budget == 0 {
        return String::new();
    }
    if text.len() <= budget {
        return text.to_owned();
    }
    let room = budget.saturating_sub(marker.len());
    let mut end = 0;
    for (at, c) in text.char_indices() {
        if at + c.len_utf8() > room {
            break;
        }
        end = at + c.len_utf8();
    }
    format!("{}{marker}", &text[..end])
}

#[cfg(test)]
mod tests {
    use super::{cut_chars, excerpt, one_line, truncate_utf8};

    #[test]
    fn one_line_folds_each_run_of_line_breaks_into_a_space() {
        assert_eq!(one_line("two\nlines"), "two lines");
        assert_eq!(one_line(" a\r\n\r\nb\rc \n"), "a b c");
        assert_eq!(one_line("\u{FEFF}x\u{2028}y"), "x\u{2028}y");
        assert_eq!(one_line("\n"), "");
    }

    #[test]
    fn a_cut_counts_code_points_and_the_ellipsis_is_one_of_them() {
        assert_eq!(cut_chars("abcdef", 6), "abcdef");
        assert_eq!(cut_chars("abcdef", 5), "abcd…");
        // Four code points, eight UTF-16 units, sixteen bytes: the limit is in code points.
        assert_eq!(cut_chars("🙂🙂🙂🙂", 4), "🙂🙂🙂🙂");
        assert_eq!(cut_chars("🙂🙂🙂🙂", 3), "🙂🙂…");
        assert_eq!(cut_chars("abc", 0), "ab…");
        assert_eq!(cut_chars("", 0), "");
    }

    #[test]
    fn an_excerpt_is_the_first_line_with_something_on_it() {
        assert_eq!(excerpt("\n  \n  Fix merged  \nmore", 60), "Fix merged");
        assert_eq!(excerpt("", 60), "message");
        assert_eq!(excerpt(" \n\t", 60), "message");
        assert_eq!(excerpt("abcdef", 4), "abc…");
        assert_eq!(excerpt("a\rb", 60), "a\rb");
    }

    #[test]
    fn a_text_is_cut_to_bytes_at_a_character_and_the_marker_is_whole() {
        let marker = "\n[cut]";
        assert_eq!(truncate_utf8("short", 10, marker), "short");
        assert_eq!(truncate_utf8("exactly ten", 11, marker), "exactly ten");
        // Twelve bytes of room beside the marker hold six two-byte characters, not six and a half.
        assert_eq!(
            truncate_utf8(&"é".repeat(20), 19, marker),
            format!("{}{marker}", "é".repeat(6))
        );
        assert_eq!(truncate_utf8("anything", 0, marker), "");
        // A budget smaller than the marker still returns the marker whole.
        assert_eq!(truncate_utf8("anything at all", 3, marker), marker);
    }
}
