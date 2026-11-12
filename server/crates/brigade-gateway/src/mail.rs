//! Mail shapes and the text rules every provider shares (`supabase/functions/_shared/mail.ts`): address
//! parsing, header lookup, Message-ID tokens, the split between what a person typed and what their client
//! quoted, and a last-resort HTML-to-text.

use std::sync::LazyLock;

use regex::Regex;

use crate::js;
use crate::json::Json;

/// A mail a provider received, in the gateway's own terms.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InboundMail {
    /// The provider's own id for the received mail.
    pub provider_id: String,
    /// The sender's address, lowercase; unverified text like every label in Brigade.
    pub from: String,
    /// The sender's display name, when the mail gave one.
    pub from_name: Option<String>,
    /// Every address the mail was delivered to, lowercase.
    pub to: Vec<String>,
    /// The subject, as the mail has it.
    pub subject: String,
    /// The RFC 5322 Message-ID of the mail, with its angle brackets, when the provider exposes it.
    pub message_id: Option<String>,
    /// The Message-ID this mail answers, with its angle brackets.
    pub in_reply_to: Option<String>,
    /// The Message-IDs of the thread, oldest first.
    pub references: Vec<String>,
    /// The plain-text body; the HTML reduced to text when there was no text part.
    pub text: String,
    /// When the provider received the mail, as the provider or the connector wrote it.
    pub received_at: String,
}

/// A mail for a provider to send.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OutboundMail {
    /// The From header: an address, or `Name <address>`.
    pub from: String,
    /// The one address the mail goes to.
    pub to: String,
    /// The Reply-To address.
    pub reply_to: Option<String>,
    /// The subject, on one line.
    pub subject: String,
    /// The plain-text body. A gateway sends no HTML.
    pub text: String,
    /// The extra headers by name, each name once, in the order they are sent.
    pub headers: Vec<(String, String)>,
}

/// An address and the display name written beside it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Address {
    /// The address, lowercase.
    pub address: String,
    /// The display name, `None` when there is none or it is empty.
    pub name: Option<String>,
}

/// A reply, split into the part the person typed and the part their client quoted under it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Reply {
    /// What the person typed, without the white space at its end.
    pub body: String,
    /// What the client quoted, with one level of `> ` removed from each line.
    pub quoted: String,
}

/// `/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/`.
static ADDRESS: LazyLock<Regex> =
    LazyLock::new(|| js::regex(&format!(r"^[^{s}@<>]+@[^{s}@<>]+\.[^{s}@<>]+$", s = js::SPACE)));

/// `/^\s*(?:"?([^"<]*?)"?\s*)?<([^<>]+)>\s*$/`.
static NAMED_ADDRESS: LazyLock<Regex> = LazyLock::new(|| {
    js::regex(&format!(
        r#"^[{s}]*(?:"?([^"<]*?)"?[{s}]*)?<([^<>]+)>[{s}]*$"#,
        s = js::SPACE
    ))
});

/// `/<[^<>\s]+>/g`.
static MESSAGE_ID: LazyLock<Regex> = LazyLock::new(|| js::regex(&format!(r"<[^<>{s}]+>", s = js::SPACE)));

/// `/^-----\s*Original Message\s*-----/i`.
static ORIGINAL_MESSAGE: LazyLock<Regex> = LazyLock::new(|| {
    js::regex(&format!(
        r"^-----[{s}]*(?i-u:Original Message)[{s}]*-----",
        s = js::SPACE
    ))
});

/// `/<(script|style)[\s\S]*?<\/\1>/gi`. The `regex` crate has no back-reference, and with two tags there is
/// no need of one: a tag that opened as `script` closes as `script`, in any case, and likewise `style`.
static SCRIPT_OR_STYLE: LazyLock<Regex> =
    LazyLock::new(|| js::regex(r"(?s:<(?i-u:script).*?</(?i-u:script)>|<(?i-u:style).*?</(?i-u:style)>)"));

/// `/<br\s*\/?>/gi`.
static LINE_BREAK: LazyLock<Regex> =
    LazyLock::new(|| js::regex(&format!(r"<(?i-u:br)[{s}]*/?>", s = js::SPACE)));

/// `/<\/(p|div|li|tr|h[1-6]|blockquote|pre)>/gi`.
static BLOCK_END: LazyLock<Regex> =
    LazyLock::new(|| js::regex(r"</(?i-u:p|div|li|tr|h[1-6]|blockquote|pre)>"));

/// `/<blockquote[^>]*>/gi`.
static BLOCKQUOTE: LazyLock<Regex> = LazyLock::new(|| js::regex(r"<(?i-u:blockquote)[^>]*>"));

/// `/<[^>]+>/g`.
static TAG: LazyLock<Regex> = LazyLock::new(|| js::regex(r"<[^>]+>"));

/// `/[ \t]+\n/g`.
static SPACE_BEFORE_BREAK: LazyLock<Regex> = LazyLock::new(|| js::regex(r"[ \t]+\n"));

/// `/\n{3,}/g`.
static BLANK_LINES: LazyLock<Regex> = LazyLock::new(|| js::regex(r"\n{3,}"));

/// Whether `text` is a plausible bare email address: one `@`, a dot inside what follows it, and no white
/// space or angle bracket anywhere.
#[must_use]
pub fn is_address(text: &str) -> bool {
    ADDRESS.is_match(text)
}

/// Reads `Name <addr>` or a bare address. The address comes back lowercase.
#[must_use]
pub fn parse_address(text: &str) -> Address {
    match NAMED_ADDRESS.captures(text) {
        Some(found) => Address {
            address: js::trim(&found[2]).to_lowercase(),
            name: found
                .get(1)
                .map(|name| js::trim(name.as_str()))
                .filter(|name| !name.is_empty())
                .map(str::to_owned),
        },
        None => Address {
            address: js::trim(text).to_lowercase(),
            name: None,
        },
    }
}

/// Finds a header by name, in any case, among the members of a JSON object; a header given as a list
/// answers its first value. A header whose value is an empty list answers `undefined`, which is what
/// `String([][0])` is.
pub(crate) fn header_value<'a>(
    headers: impl IntoIterator<Item = (&'a str, &'a Json)>,
    name: &str,
) -> Option<String> {
    let want = name.to_lowercase();
    for (key, value) in headers {
        if key.to_lowercase() != want || *value == Json::Null {
            continue;
        }
        return Some(match value {
            Json::List(items) => items.first().map_or_else(|| "undefined".to_owned(), Json::text),
            other => other.text(),
        });
    }
    None
}

/// The `<…>` tokens of a Message-ID, In-Reply-To or References header, in order. A header that is not there
/// has none.
#[must_use]
pub fn parse_message_ids(header: Option<&str>) -> Vec<String> {
    MESSAGE_ID
        .find_iter(header.unwrap_or(""))
        .map(|found| found.as_str().to_owned())
        .collect()
}

/// Splits a reply into the part the person typed and the part their client quoted under it. The quoted part
/// begins at the first line that starts with `>`, at an "On … wrote:" attribution (one line, or wrapped
/// onto two), at an Outlook "-----Original Message-----" rule, or at a `-- ` signature separator. The
/// quoted half comes back with one level of `> ` removed from each line, so a block inside it can still be
/// read.
#[must_use]
pub fn strip_quoted(text: &str) -> Reply {
    let unix = js::unix_lines(text);
    let lines: Vec<&str> = unix.split('\n').collect();
    let cut = (0..lines.len())
        .find(|&at| {
            let line = lines[at];
            line.starts_with('>')
                || line == "-- "
                || ORIGINAL_MESSAGE.is_match(line)
                || is_attribution(line, 300)
                || (opens_attribution(line)
                    && is_attribution(
                        &format!("{line} {}", lines.get(at + 1).copied().unwrap_or("")),
                        400,
                    ))
        })
        .unwrap_or(lines.len());
    Reply {
        body: js::trim_end(&lines[..cut].join("\n")).to_owned(),
        quoted: lines[cut..]
            .iter()
            .map(|line| unquote(line))
            .collect::<Vec<_>>()
            .join("\n"),
    }
}

/// `/^On .{3,N}wrote:\s*$/`, by hand. Without the `u` flag JavaScript's `.{3,N}` counts UTF-16 code units,
/// which no pattern of the `regex` crate can: a line of emoji is twice as long there as its characters.
/// The pattern is anchored at both ends, so it matches when the line, without the white space at its end,
/// is `On `, then 3 to `most` code units with no line separator among them, then `wrote:`.
fn is_attribution(line: &str, most: usize) -> bool {
    let Some(between) = js::trim_end(line)
        .strip_prefix("On ")
        .and_then(|rest| rest.strip_suffix("wrote:"))
    else {
        return false;
    };
    (3..=most).contains(&js::utf16_len(between)) && !between.chars().any(js::is_line_terminator)
}

/// `/^On .{3,300}$/`: the first line of an attribution that wraps onto a second.
fn opens_attribution(line: &str) -> bool {
    line.strip_prefix("On ").is_some_and(|rest| {
        (3..=300).contains(&js::utf16_len(rest)) && !rest.chars().any(js::is_line_terminator)
    })
}

/// `replace(/^>\s?/, "")`: one level of quoting off a line.
fn unquote(line: &str) -> &str {
    let Some(rest) = line.strip_prefix('>') else {
        return line;
    };
    match rest.chars().next() {
        Some(c) if js::is_space(c) => &rest[c.len_utf8()..],
        _ => rest,
    }
}

/// The fallback for a mail with no text part: block tags become line breaks, every other tag is dropped,
/// the common entities are decoded, and runs of blank lines are folded. Not a renderer.
#[must_use]
pub fn html_to_text(html: &str) -> String {
    let text = SCRIPT_OR_STYLE.replace_all(html, "");
    let text = LINE_BREAK.replace_all(&text, "\n");
    let text = BLOCK_END.replace_all(&text, "\n");
    let text = BLOCKQUOTE.replace_all(&text, "\n> ");
    let text = TAG.replace_all(&text, "");
    let text = text
        .replace("&nbsp;", " ")
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'")
        .replace("&apos;", "'")
        .replace("&amp;", "&");
    let text = SPACE_BEFORE_BREAK.replace_all(&text, "\n");
    let text = BLANK_LINES.replace_all(&text, "\n\n");
    js::trim(&text).to_owned()
}

/// The part of an address after its last `@`, lowercase; empty when it has none.
#[must_use]
pub fn domain_of(address: &str) -> String {
    address
        .rfind('@')
        .map(|at| address[at + 1..].to_lowercase())
        .unwrap_or_default()
}

/// The part of an address before its last `@`; the whole of it when it has none.
#[must_use]
pub fn local_of(address: &str) -> &str {
    address.rfind('@').map_or(address, |at| &address[..at])
}

#[cfg(test)]
mod tests {
    use super::{header_value, html_to_text, is_address, parse_address, parse_message_ids, strip_quoted};
    use crate::json::Json;

    fn header(headers: &str, name: &str) -> Option<String> {
        let headers = Json::parse(headers).unwrap();
        header_value(
            headers.entries().iter().map(|(key, value)| (key.as_str(), value)),
            name,
        )
    }

    /// `mail_test.ts`: "headerValue is case-insensitive and takes the first of a repeated header". It is
    /// here and not in `tests/mail.rs` because the JSON it reads is this crate's own.
    #[test]
    fn header_value_is_case_insensitive_and_takes_the_first_of_a_repeated_header() {
        assert_eq!(
            header(r#"{"In-Reply-To": "<a@x>"}"#, "in-reply-to").as_deref(),
            Some("<a@x>")
        );
        assert_eq!(
            header(r#"{"received": ["one", "two"]}"#, "Received").as_deref(),
            Some("one")
        );
        assert_eq!(header("{}", "x"), None);
    }

    #[test]
    fn header_value_reads_the_first_member_that_has_a_value_in_the_documents_order() {
        let headers = r#"{"x-a": null, "message-id": "<lower@x>", "Message-ID": "<upper@x>", "X-A": 5}"#;
        assert_eq!(header(headers, "MESSAGE-ID").as_deref(), Some("<lower@x>"));
        assert_eq!(header(headers, "x-a").as_deref(), Some("5"));
        assert_eq!(header(r#"{"a": []}"#, "a").as_deref(), Some("undefined"));
        assert_eq!(header(r#"{"a": [null]}"#, "a").as_deref(), Some("null"));
    }

    #[test]
    fn an_address_has_one_at_sign_and_a_dot_inside_its_domain() {
        for address in ["a@b.c", "a.b+tag@c.d.e", "a@b..c", "a@.b.c", "é@ü.de"] {
            assert!(is_address(address), "{address}");
        }
        for not_one in [
            "",
            "a@b",
            "a@b.",
            "a@.b",
            "@b.c",
            "a@@b.c",
            "a@b@c.d",
            "a b@c.d",
            "a@b.c ",
            "<a@b.c>",
            "a@b\u{A0}.c",
            "a@b.c\n",
            "a@b\u{FEFF}.c",
        ] {
            assert!(!is_address(not_one), "{not_one:?}");
        }
        // U+0085 is white space to Rust and not to JavaScript, so it may stand in an address.
        assert!(is_address("a\u{85}@b.c"));
    }

    #[test]
    fn a_display_name_is_read_with_or_without_quotes_and_an_odd_one_is_no_name() {
        let parsed = |text: &str| {
            let address = parse_address(text);
            (address.address, address.name)
        };
        assert_eq!(parsed("  <A@B.C>  "), ("a@b.c".to_owned(), None));
        assert_eq!(parsed("\"\" <a@b.c>"), ("a@b.c".to_owned(), None));
        assert_eq!(
            parsed("Alice Example<a@b.c>"),
            ("a@b.c".to_owned(), Some("Alice Example".to_owned()))
        );
        assert_eq!(
            parsed("\"Alice <a@b.c>"),
            ("a@b.c".to_owned(), Some("Alice".to_owned()))
        );
        assert_eq!(
            parsed("Alice\" <a@b.c>"),
            ("a@b.c".to_owned(), Some("Alice".to_owned()))
        );
        assert_eq!(parsed("< a@b.c >"), ("a@b.c".to_owned(), None));
        // A name that holds a quote or an angle bracket does not fit the form: the whole text is the address.
        assert_eq!(
            parsed("A \"B\" C <a@b.c>"),
            ("a \"b\" c <a@b.c>".to_owned(), None)
        );
        assert_eq!(parsed("a < b <c@d.e>"), ("a < b <c@d.e>".to_owned(), None));
        assert_eq!(parsed("Alice <a@b.c> x"), ("alice <a@b.c> x".to_owned(), None));
        assert_eq!(parsed("Alice <>"), ("alice <>".to_owned(), None));
        assert_eq!(
            parsed("\u{FEFF}Alice\u{A0}<a@b.c>\u{2028}"),
            ("a@b.c".to_owned(), Some("Alice".to_owned()))
        );
    }

    #[test]
    fn a_message_id_has_no_white_space_or_bracket_inside() {
        assert_eq!(parse_message_ids(Some("")), Vec::<String>::new());
        assert_eq!(
            parse_message_ids(Some("<a b> <c@d><<e@f>> <> <g\u{A0}h>")),
            ["<c@d>", "<e@f>"]
        );
    }

    #[test]
    fn an_attribution_is_measured_in_utf16_code_units() {
        let body = |text: &str| strip_quoted(text).body;
        // Three to 300 code units stand between "On " and "wrote:", the space before "wrote:" among them.
        assert_eq!(body("Yes.\nOn a wrote:\n> q"), "Yes.\nOn a wrote:");
        assert_eq!(body("Yes.\nOn ab wrote:\n> q"), "Yes.");
        assert_eq!(body("Yes.\nOn abcwrote:\n> q"), "Yes.");
        assert_eq!(body(&format!("Yes.\nOn {}wrote:", "x".repeat(300))), "Yes.");
        assert_eq!(
            body(&format!("Yes.\nOn {}wrote:", "x".repeat(301))),
            format!("Yes.\nOn {}wrote:", "x".repeat(301))
        );
        // 150 emoji are 150 characters and 300 code units; 151 are one unit pair too many.
        assert_eq!(body(&format!("Yes.\nOn {}wrote:", "🙂".repeat(150))), "Yes.");
        assert_eq!(
            body(&format!("Yes.\nOn {}wrote:", "🙂".repeat(151))),
            format!("Yes.\nOn {}wrote:", "🙂".repeat(151))
        );
        // Wrapped onto two lines, the two together may be 400.
        let wrapped = format!("Yes.\nOn {}\n{} wrote:", "x".repeat(300), "y".repeat(95));
        assert_eq!(body(&wrapped), "Yes.");
        let too_long = format!("Yes.\nOn {}\n{} wrote:", "x".repeat(300), "y".repeat(100));
        assert_eq!(body(&too_long), too_long);
        // White space may follow "wrote:", nothing else may, and "On" is matched in this case only.
        assert_eq!(body("Yes.\nOn Friday Brigade wrote: \t\u{A0}"), "Yes.");
        assert_eq!(
            body("Yes.\nOn Friday Brigade wrote: so"),
            "Yes.\nOn Friday Brigade wrote: so"
        );
        assert_eq!(
            body("Yes.\non Friday Brigade wrote:"),
            "Yes.\non Friday Brigade wrote:"
        );
        // A line separator is not matched by `.`.
        assert_eq!(
            body("Yes.\nOn Fri\u{2028}day Brigade wrote:"),
            "Yes.\nOn Fri\u{2028}day Brigade wrote:"
        );
    }

    #[test]
    fn the_other_cuts_and_the_quoted_half() {
        let reply = strip_quoted("Hi\r\n>one\r\n> two\n>\u{A0}three\n>> four\nfive");
        assert_eq!(reply.body, "Hi");
        assert_eq!(reply.quoted, "one\ntwo\nthree\n> four\nfive");
        assert_eq!(strip_quoted("a\n--\nb").body, "a\n--\nb");
        assert_eq!(strip_quoted("a\n--  \nb").body, "a\n--  \nb");
        assert_eq!(strip_quoted("a\n-----original message-----\nb").body, "a");
        assert_eq!(
            strip_quoted("a\n-----  ORIGINAL MESSAGE\u{A0}-----Von: x").body,
            "a"
        );
        assert_eq!(
            strip_quoted("a\n ----- Original Message -----").body,
            "a\n ----- Original Message -----"
        );
        assert_eq!(strip_quoted("a \n\n\u{FEFF}\n> q").body, "a");
        let whole = strip_quoted("> q");
        assert_eq!((whole.body.as_str(), whole.quoted.as_str()), ("", "q"));
        let none = strip_quoted("");
        assert_eq!((none.body.as_str(), none.quoted.as_str()), ("", ""));
    }

    #[test]
    fn script_and_style_go_with_their_content_and_close_only_as_they_opened() {
        // The back-reference of the TypeScript's pattern: a script ends at </script>, in any case, never
        // at </style>.
        assert_eq!(html_to_text("a<script>x</style>y</SCRIPT>b"), "ab");
        assert_eq!(
            html_to_text("a<STYLE type=\"x\">\n p{} \n</style>b<Script>1</sCRIPT>c"),
            "abc"
        );
        // Lazy: the first closing tag ends it.
        assert_eq!(html_to_text("<script>1</script>a<script>2</script>b"), "ab");
        // One that never closes is only a tag, and its content stays.
        assert_eq!(html_to_text("a<script>x"), "ax");
        assert_eq!(html_to_text("a<style>x</script>b"), "axb");
        // The opening tag is a prefix: <scripted> opens a script.
        assert_eq!(html_to_text("a<scripted>x</script>b"), "ab");
        // U+017F is not `s` to JavaScript without the `u` flag.
        assert_eq!(html_to_text("a<\u{17F}cript>x</script>b"), "axb");
    }

    #[test]
    fn tags_entities_and_blank_lines() {
        assert_eq!(html_to_text("a<BR>b<br/>c<br />d<br\n>e"), "a\nb\nc\nd\ne");
        assert_eq!(
            html_to_text("<h1>T</h1><ul><li>one</li><li>two</li></ul><pre>p</pre>"),
            "T\none\ntwo\np"
        );
        assert_eq!(
            html_to_text("a<blockquote class=\"q\">b</blockquote>c"),
            "a\n> b\nc"
        );
        assert_eq!(html_to_text("a<p>b</p>   \n\n\n\n<p>c</p>"), "ab\n\nc");
        assert_eq!(
            html_to_text("&amp;lt; &nbsp;&quot;x&quot; &#39;y&apos; &amp;amp;"),
            "&lt;  \"x\" 'y' &amp;"
        );
        assert_eq!(html_to_text("1 < 2 and 3 > 2"), "1  2");
        assert_eq!(html_to_text("a <> b"), "a <> b");
        assert_eq!(html_to_text("\u{FEFF} x \u{A0}"), "x");
    }
}
