//! `supabase/functions/_shared/mail_test.ts`, case for case: the same inputs and the same expected outputs.
//!
//! Its third case, "headerValue is case-insensitive and takes the first of a repeated header", is a unit
//! test of `src/mail.rs`: the function reads this crate's own JSON and is not public.

use brigade_gateway::mail::{Address, html_to_text};
use brigade_gateway::{is_address, parse_address, parse_message_ids, strip_quoted};

fn address(address: &str, name: Option<&str>) -> Address {
    Address {
        address: address.to_owned(),
        name: name.map(str::to_owned),
    }
}

#[test]
fn parse_address_reads_display_names_and_bare_addresses_lowercasing_the_address() {
    assert_eq!(
        parse_address("\"Alice Example\" <Alice@Example.com>"),
        address("alice@example.com", Some("Alice Example"))
    );
    assert_eq!(
        parse_address("Alice <alice@example.com>"),
        address("alice@example.com", Some("Alice"))
    );
    assert_eq!(
        parse_address("ALICE@example.com"),
        address("alice@example.com", None)
    );
}

#[test]
fn is_address_accepts_a_plausible_address_and_refuses_a_session_id() {
    assert!(is_address("qa@example.com"));
    assert!(!is_address("3f9a2"));
    assert!(!is_address("a@b"));
}

#[test]
fn parse_message_ids_extracts_angle_bracketed_ids() {
    assert_eq!(
        parse_message_ids(Some("<a@x> <b@y>\n <c@z>")),
        ["<a@x>", "<b@y>", "<c@z>"]
    );
    assert_eq!(parse_message_ids(None), Vec::<String>::new());
}

#[test]
fn strip_quoted_cuts_at_a_quoted_line_an_attribution_line_and_a_signature() {
    let gmail = "Looks good, ship it.\n\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade <brigade@appshapes.com> wrote:\n> frank wrote:\n> body\n> [brigade]\n> to: abc\n> [/brigade]";
    let reply = strip_quoted(gmail);
    assert_eq!(reply.body, "Looks good, ship it.");
    assert!(reply.quoted.contains("[brigade]\nto: abc\n[/brigade]"));

    let wrapped = "Yes.\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade\n<brigade@appshapes.com> wrote:\n> q";
    assert_eq!(strip_quoted(wrapped).body, "Yes.");

    let outlook = "Agreed\r\n\r\n-----Original Message-----\r\nFrom: x\r\n";
    assert_eq!(strip_quoted(outlook).body, "Agreed");

    let signature = "Thanks\n-- \nAlice\nQA";
    assert_eq!(strip_quoted(signature).body, "Thanks");

    assert_eq!(strip_quoted("plain\ntext").body, "plain\ntext");
}

#[test]
fn html_to_text_keeps_the_words_and_the_line_breaks() {
    let text =
        html_to_text("<div>Hello<br>there</div><p>Second &amp; last &lt;tag&gt;</p><style>p{}</style>");
    assert_eq!(text, "Hello\nthere\nSecond & last <tag>");
}
