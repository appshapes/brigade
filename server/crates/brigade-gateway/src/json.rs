//! A JSON document as JavaScript holds one.
//!
//! The provider payloads and the connector's two shapes were read in TypeScript as plain objects: a member
//! was looked up by name, a missing one fell back with `??`, and a value of the wrong type was turned into
//! text with `String()`. This is that reading. An object keeps its members in the order the document names
//! them, which `serde_json`'s own value does not, because the order of a mail's headers is what a provider
//! is sent. A member named twice keeps its first place and its last value, as `JSON.parse` leaves it.
//!
//! Reading a document takes time in proportion to its length, whatever it holds: an object of very many
//! members finds a name it has seen by a table of the names and not by looking through them, so a document
//! made to be slow is not.
//!
//! Three things `JSON.parse` accepts are not JSON here: a `\u` escape that is half of a surrogate pair, a
//! number too large for a double (JavaScript reads `Infinity`) and nesting deeper than `serde_json` follows.
//! One order is not kept: JavaScript lists the members whose names are whole numbers first. And a number
//! written with a fraction, an exponent or more digits than a 64-bit integer holds is read by `serde_json`,
//! which can be one unit in the last place away from the double JavaScript reads.

use std::collections::HashMap;
use std::fmt;

use serde::Deserialize;
use serde::de::{self, MapAccess, SeqAccess, Visitor};

use crate::js;

/// How many members an object may have and still be looked through for a name it has seen. Nearly every
/// object a provider sends is smaller, and needs no table.
const LOOKED_THROUGH: usize = 16;

/// One JSON value.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Json {
    Null,
    Bool(bool),
    /// Every number is a double, as in JavaScript.
    Number(f64),
    Text(String),
    List(Vec<Json>),
    /// The members in the document's order, each name once.
    Object(Vec<(String, Json)>),
}

impl Json {
    /// `JSON.parse`: the value `raw` spells, `None` where it spells none.
    pub(crate) fn parse(raw: &str) -> Option<Self> {
        serde_json::from_str(raw).ok()
    }

    /// The member `name` of an object, where `null` reads as a missing member. That is how nearly every
    /// lookup in the TypeScript reads one: with `??`, or with a test against both.
    pub(crate) fn get(&self, name: &str) -> Option<&Self> {
        self.member(name).filter(|value| **value != Self::Null)
    }

    /// The member `name` of an object as the document has it, `null` included. Anything that is not an
    /// object has no members.
    pub(crate) fn member(&self, name: &str) -> Option<&Self> {
        self.entries()
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value)
    }

    /// `Object.entries` of an object; nothing for any other value.
    pub(crate) fn entries(&self) -> &[(String, Self)] {
        match self {
            Self::Object(members) => members,
            _ => &[],
        }
    }

    /// The elements of an array; nothing for any other value.
    pub(crate) fn items(&self) -> &[Self] {
        match self {
            Self::List(items) => items,
            _ => &[],
        }
    }

    /// The text of a string value.
    pub(crate) fn as_str(&self) -> Option<&str> {
        match self {
            Self::Text(text) => Some(text),
            _ => None,
        }
    }

    /// Whether the value is true in a JavaScript condition: everything but `null`, `false`, zero and the
    /// empty string.
    pub(crate) fn truthy(&self) -> bool {
        match self {
            Self::Null => false,
            Self::Bool(value) => *value,
            Self::Number(number) => *number != 0.0 && !number.is_nan(),
            Self::Text(text) => !text.is_empty(),
            Self::List(_) | Self::Object(_) => true,
        }
    }

    /// `String(value)`: a string as it is, a number as JavaScript writes it, an array as its elements
    /// joined by commas and an object as `[object Object]`.
    pub(crate) fn text(&self) -> String {
        match self {
            Self::Null => "null".to_owned(),
            Self::Bool(value) => value.to_string(),
            Self::Number(number) => js::number_text(*number),
            Self::Text(text) => text.clone(),
            Self::List(items) => items
                .iter()
                .map(|item| match item {
                    Self::Null => String::new(),
                    other => other.text(),
                })
                .collect::<Vec<_>>()
                .join(","),
            Self::Object(_) => "[object Object]".to_owned(),
        }
    }
}

/// `String(value ?? "")`: the text of a member, empty when it is missing or `null`.
pub(crate) fn text_of(value: Option<&Json>) -> String {
    value.map(Json::text).unwrap_or_default()
}

/// `typeof value === "string" && value !== "" ? value : null`.
pub(crate) fn filled(value: Option<&Json>) -> Option<&str> {
    value.and_then(Json::as_str).filter(|text| !text.is_empty())
}

impl<'de> Deserialize<'de> for Json {
    fn deserialize<D: de::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct JsonVisitor;

        impl<'de> Visitor<'de> for JsonVisitor {
            type Value = Json;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("a JSON value")
            }

            fn visit_unit<E: de::Error>(self) -> Result<Json, E> {
                Ok(Json::Null)
            }

            fn visit_bool<E: de::Error>(self, value: bool) -> Result<Json, E> {
                Ok(Json::Bool(value))
            }

            // An integer beyond 2^53 rounds to the nearest double, as it does when JavaScript reads it.
            fn visit_i64<E: de::Error>(self, value: i64) -> Result<Json, E> {
                Ok(Json::Number(value as f64))
            }

            fn visit_u64<E: de::Error>(self, value: u64) -> Result<Json, E> {
                Ok(Json::Number(value as f64))
            }

            fn visit_f64<E: de::Error>(self, value: f64) -> Result<Json, E> {
                Ok(Json::Number(value))
            }

            fn visit_str<E: de::Error>(self, value: &str) -> Result<Json, E> {
                Ok(Json::Text(value.to_owned()))
            }

            fn visit_string<E: de::Error>(self, value: String) -> Result<Json, E> {
                Ok(Json::Text(value))
            }

            fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Json, A::Error> {
                let mut items = Vec::new();
                while let Some(item) = seq.next_element()? {
                    items.push(item);
                }
                Ok(Json::List(items))
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Json, A::Error> {
                let mut members: Vec<(String, Json)> = Vec::new();
                // Where each name is among the members, once there are too many to look through. The
                // table's hashes are keyed at random, so no document can be written to defeat it.
                let mut places: Option<HashMap<String, usize>> = None;
                while let Some(name) = map.next_key::<String>()? {
                    let value = map.next_value()?;
                    let seen = match &places {
                        Some(places) => places.get(&name).copied(),
                        None => members.iter().position(|(key, _)| *key == name),
                    };
                    match seen {
                        // A name that comes again keeps its first place and takes the later value.
                        Some(at) => members[at].1 = value,
                        None => {
                            if let Some(places) = &mut places {
                                places.insert(name.clone(), members.len());
                            }
                            members.push((name, value));
                            if places.is_none() && members.len() > LOOKED_THROUGH {
                                places = Some(
                                    members
                                        .iter()
                                        .enumerate()
                                        .map(|(at, (key, _))| (key.clone(), at))
                                        .collect(),
                                );
                            }
                        }
                    }
                }
                Ok(Json::Object(members))
            }
        }

        deserializer.deserialize_any(JsonVisitor)
    }
}

#[cfg(test)]
mod tests {
    use super::{Json, filled, text_of};

    #[test]
    fn an_object_keeps_the_documents_order_and_the_last_value_of_a_repeated_name() {
        let document = Json::parse(r#"{"b": 1, "a": 2, "b": 3}"#).unwrap();
        assert_eq!(
            document.entries(),
            [
                ("b".to_owned(), Json::Number(3.0)),
                ("a".to_owned(), Json::Number(2.0))
            ]
        );
    }

    #[test]
    fn an_object_of_very_many_members_is_read_in_time_that_grows_with_its_length() {
        // Two hundred thousand names, each once: what a parser that looks through the members it has for
        // every new one takes minutes over.
        let distinct: Vec<String> = (0..200_000).map(|at| format!("\"{at}\":{at}")).collect();
        let began = std::time::Instant::now();
        let document = Json::parse(&format!("{{{}}}", distinct.join(","))).unwrap();
        let members = document.entries();
        assert_eq!(members.len(), 200_000);
        assert_eq!(members[0], ("0".to_owned(), Json::Number(0.0)));
        assert_eq!(members[17], ("17".to_owned(), Json::Number(17.0)));
        assert_eq!(members[199_999], ("199999".to_owned(), Json::Number(199_999.0)));
        assert_eq!(document.get("123456"), Some(&Json::Number(123_456.0)));

        // As many members under a thousand names, over and over: each name keeps the place it first had
        // and has the value it was last given.
        let repeated: Vec<String> = (0..200_000)
            .map(|at| format!("\"n{}\":{at}", at % 1000))
            .collect();
        let document = Json::parse(&format!("{{{}}}", repeated.join(","))).unwrap();
        let members = document.entries();
        assert_eq!(members.len(), 1000);
        for (at, (name, value)) in members.iter().enumerate() {
            assert_eq!(name, &format!("n{at}"));
            assert_eq!(value, &Json::Number((199_000 + at) as f64));
        }
        // One name alone, two hundred thousand times, is one member with the last value.
        let one = vec!["\"same\":1"; 199_999].join(",");
        let document = Json::parse(&format!("{{{one},\"same\":2}}")).unwrap();
        assert_eq!(document.entries(), [("same".to_owned(), Json::Number(2.0))]);
        assert!(
            began.elapsed() < std::time::Duration::from_secs(5),
            "{:?}",
            began.elapsed()
        );
    }

    #[test]
    fn a_repeated_name_keeps_its_first_place_on_either_side_of_the_table() {
        // The same rule holds where an object is looked through and where it has its table: the name
        // that comes again is found, whether it came before the table was made or after.
        let names: Vec<String> = (0..40).map(|at| format!("\"k{at}\":{at}")).collect();
        let raw = format!(
            "{{{},\"k3\":\"early\",\"k30\":\"late\",\"new\":true}}",
            names.join(",")
        );
        let document = Json::parse(&raw).unwrap();
        let members = document.entries();
        assert_eq!(members.len(), 41);
        assert_eq!(members[3], ("k3".to_owned(), Json::Text("early".to_owned())));
        assert_eq!(members[30], ("k30".to_owned(), Json::Text("late".to_owned())));
        assert_eq!(members[40], ("new".to_owned(), Json::Bool(true)));
        // At the very size where the table is made.
        for size in [15, 16, 17, 18] {
            let names: Vec<String> = (0..size).map(|at| format!("\"k{at}\":{at}")).collect();
            let raw = format!("{{{},\"k0\":-1,\"k{}\":-2}}", names.join(","), size - 1);
            let document = Json::parse(&raw).unwrap();
            let members = document.entries();
            assert_eq!(members.len(), size, "{size}");
            assert_eq!(members[0].1, Json::Number(-1.0), "{size}");
            assert_eq!(members[size - 1].1, Json::Number(-2.0), "{size}");
        }
    }

    #[test]
    fn null_reads_as_a_missing_member_and_only_an_object_has_members() {
        let document = Json::parse(r#"{"a": null, "b": "x", "c": ""}"#).unwrap();
        assert_eq!(document.get("a"), None);
        assert_eq!(document.member("a"), Some(&Json::Null));
        assert_eq!(document.get("missing"), None);
        assert_eq!(document.member("missing"), None);
        assert_eq!(document.get("b").and_then(Json::as_str), Some("x"));
        assert_eq!(filled(document.get("b")), Some("x"));
        assert_eq!(filled(document.get("c")), None);
        assert_eq!(Json::parse(r#"["a"]"#).unwrap().get("0"), None);
        assert_eq!(Json::parse(r#""text""#).unwrap().get("length"), None);
    }

    #[test]
    fn what_is_not_json_is_none() {
        for raw in ["", "nope", "{", "{} {}", "{'a': 1}", "\u{FEFF}{}"] {
            assert_eq!(Json::parse(raw), None, "{raw}");
        }
        assert_eq!(Json::parse(" [] "), Some(Json::List(Vec::new())));
    }

    #[test]
    fn a_value_becomes_text_as_string_makes_it() {
        let text = |raw: &str| Json::parse(raw).unwrap().text();
        assert_eq!(text(r#""as it is""#), "as it is");
        assert_eq!(text("5"), "5");
        assert_eq!(text("5.0"), "5");
        assert_eq!(text("-0.5"), "-0.5");
        assert_eq!(text("12345678901234567890"), "12345678901234567000");
        assert_eq!(text("true"), "true");
        assert_eq!(text("null"), "null");
        assert_eq!(text(r#"[1, null, "a", [2, 3]]"#), "1,,a,2,3");
        assert_eq!(text("[]"), "");
        assert_eq!(text(r#"{"a": 1}"#), "[object Object]");
        assert_eq!(text_of(None), "");
    }

    #[test]
    fn truth_is_javascripts() {
        let truthy = |raw: &str| Json::parse(raw).unwrap().truthy();
        for raw in ["1", "-1", r#""0""#, "true", "[]", "{}"] {
            assert!(truthy(raw), "{raw}");
        }
        for raw in ["0", "0.0", r#""""#, "false", "null"] {
            assert!(!truthy(raw), "{raw}");
        }
    }
}
