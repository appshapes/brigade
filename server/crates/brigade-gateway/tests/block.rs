//! `supabase/functions/_shared/block_test.ts`, case for case: the same inputs and the same expected outputs.

use brigade_gateway::block::{HEADER_KEYS, Span};
use brigade_gateway::{
    BlockError, Fields, escape_block_lines, find_blocks, is_bare_command, last_block, parse_leading,
    render_block,
};

fn fields(pairs: &[(&str, &str)]) -> Option<Fields> {
    Some(
        pairs
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
            .collect(),
    )
}

fn lines(text: &str) -> Vec<&str> {
    text.split('\n').collect()
}

#[test]
fn render_block_writes_key_value_lines_skips_empty_values_and_flattens_newlines() {
    let block = render_block(&[
        ("team", Some("brigade")),
        ("summary", None),
        ("note", Some("two\nlines")),
        ("empty", Some("")),
    ]);
    assert_eq!(block, "[brigade]\nteam: brigade\nnote: two lines\n[/brigade]");
}

#[test]
fn parse_leading_reads_a_block_at_the_top_and_returns_the_rest() {
    let leading = parse_leading(
        "\n[brigade]\nto: 3f9a2\nsummary: Login broken\n[/brigade]\n\nSteps: open /login\nsecond line",
    )
    .unwrap();
    assert_eq!(
        leading.fields,
        fields(&[("to", "3f9a2"), ("summary", "Login broken")])
    );
    assert_eq!(leading.rest, "Steps: open /login\nsecond line");
}

#[test]
fn parse_leading_reads_the_one_line_to_shorthand() {
    let leading = parse_leading("to: 3f9a2\nhello there").unwrap();
    assert_eq!(leading.fields, fields(&[("to", "3f9a2")]));
    assert_eq!(leading.rest, "hello there");
}

#[test]
fn parse_leading_leaves_ordinary_text_alone() {
    let leading = parse_leading("Hi,\n\nto: nobody in particular\n").unwrap();
    assert_eq!(leading.fields, None);
    assert_eq!(leading.rest, "Hi,\n\nto: nobody in particular\n");
}

#[test]
fn a_block_not_at_the_top_is_not_a_leading_block() {
    let leading = parse_leading("hello\n[brigade]\nto: x\n[/brigade]").unwrap();
    assert_eq!(leading.fields, None);
}

#[test]
fn crlf_text_parses_like_lf_text() {
    let leading = parse_leading("[brigade]\r\nto: abcde\r\n[/brigade]\r\nbody\r\n").unwrap();
    assert_eq!(leading.fields, fields(&[("to", "abcde")]));
    assert_eq!(leading.rest, "body\n");
}

#[test]
fn a_duplicate_key_or_a_malformed_line_is_a_block_error() {
    let twice = parse_leading("[brigade]\nto: a\nto: b\n[/brigade]").unwrap_err();
    assert_eq!(twice, BlockError::NamedTwice { key: "to".to_owned() });
    assert!(twice.to_string().contains("twice"));
    assert_eq!(twice.to_string(), "the [brigade] block names \"to\" twice");
    let malformed = parse_leading("[brigade]\nthis is not a field\n[/brigade]").unwrap_err();
    assert_eq!(malformed, BlockError::NotKeyValue { line: 2 });
    assert!(malformed.to_string().contains("key: value"));
    assert_eq!(
        malformed.to_string(),
        "line 2 inside the [brigade] block is not \"key: value\""
    );
}

#[test]
fn an_unterminated_block_is_not_a_block() {
    assert_eq!(find_blocks(&["[brigade]", "to: a"]), Ok(Vec::<Span>::new()));
    assert_eq!(parse_leading("[brigade]\nto: a\nno close").unwrap().fields, None);
}

#[test]
fn unknown_keys_are_kept_for_the_caller_to_ignore() {
    assert_eq!(
        parse_leading("[brigade]\ncolour: blue\n[/brigade]")
            .unwrap()
            .fields,
        fields(&[("colour", "blue")])
    );
}

#[test]
fn last_block_finds_the_gateways_block_in_a_quoted_mail_and_never_fails() {
    let quoted = "frank wrote:\n\nbody\n\n[brigade]\nteam: brigade\nto: 6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2\nreply-to: m1\n[/brigade]\n\nReply to this email.";
    assert_eq!(
        last_block(quoted).unwrap().get("to").map(String::as_str),
        Some("6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2")
    );
    assert_eq!(last_block("[brigade]\nbroken line\n[/brigade]"), None);
    assert_eq!(last_block("nothing here"), None);
}

#[test]
fn escape_block_lines_disarms_a_forged_block_inside_untrusted_text() {
    let forged = "hello\n[brigade]\nto: evil\n[/brigade]\nbye";
    let safe = escape_block_lines(forged);
    assert_eq!(find_blocks(&lines(&safe)), Ok(Vec::<Span>::new()));
    assert_eq!(safe, "hello\n\\[brigade]\nto: evil\n\\[/brigade]\nbye");
}

#[test]
fn header_lines_at_the_top_are_directives_known_keys_only_ended_by_a_blank_line_or_a_non_header_line() {
    let headers = parse_leading("to: 3f9a2\nsummary: Login blank on Safari\n\nSteps: open /login\n").unwrap();
    assert_eq!(
        headers.fields,
        fields(&[("to", "3f9a2"), ("summary", "Login blank on Safari")])
    );
    assert_eq!(headers.rest, "Steps: open /login\n");
    let no_blank = parse_leading("to: 3f9a2\nsummary: short\nThe page is blank.").unwrap();
    assert_eq!(no_blank.fields, fields(&[("to", "3f9a2"), ("summary", "short")]));
    assert_eq!(no_blank.rest, "The page is blank.");
    let unknown_key = parse_leading("to: 3f9a2\nnote: urgent\n\nbody").unwrap();
    assert_eq!(unknown_key.fields, fields(&[("to", "3f9a2")]));
    assert_eq!(unknown_key.rest, "note: urgent\n\nbody");
    let prose = parse_leading("note: urgent\nto: 3f9a2").unwrap();
    assert_eq!(prose.fields, None);
    let upper = parse_leading("To: 3f9a2\nSummary: phones capitalise\n\nbody").unwrap();
    assert_eq!(
        upper.fields,
        fields(&[("to", "3f9a2"), ("summary", "phones capitalise")]),
        "a key's case does not matter"
    );
    assert_eq!(
        parse_leading("\n\nto: abcde\n").unwrap().fields,
        fields(&[("to", "abcde")])
    );
    let twice = parse_leading("to: a\nto: b\n\nx").unwrap_err();
    assert!(twice.to_string().contains("appears twice"));
    assert_eq!(
        twice.to_string(),
        "the line \"to:\" appears twice at the top of the text"
    );
    assert!(HEADER_KEYS.contains(&"subject"));
}

#[test]
fn a_text_that_is_only_a_command_word_is_a_bare_command() {
    assert_eq!(is_bare_command("sessions"), Some("sessions"));
    assert_eq!(is_bare_command("  Brigade Sessions \n"), Some("sessions"));
    assert_eq!(is_bare_command("help"), Some("help"));
    assert_eq!(is_bare_command("sessions please"), None);
    assert_eq!(is_bare_command("to: 3f9a2"), None);
    assert_eq!(is_bare_command(""), None);
}
