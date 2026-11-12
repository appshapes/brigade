//! A request body, read strictly and lazily.
//!
//! A body is one JSON object. Its members are kept as the text they arrived as, and a member is read only
//! when a rule asks for it: what a request carries beyond what the server reads costs the server nothing
//! but its bytes, whoever sent it. Unknown members are ignored (BAP/1 JSON convention 2), `null` reads as an
//! absent member, and an object that names a member twice is refused, as the protocol's reference codec
//! does: two values for one member cannot both be honoured (JSON convention 1).
//!
//! A member of the wrong JSON type is answered in one of two ways, as the stand-in server does. A member of
//! a BAP/1 shape (a registration's, a send's) makes the document one that does not read: `malformed_json`,
//! with no member named. A member the contract itself adds (`team_ref`, `session_id`, `limit`) is named.

use std::collections::BTreeMap;
use std::fmt;

use serde::Deserialize;
use serde::de::{self, MapAccess, Visitor};
use serde_json::value::RawValue;

use crate::error::Error;

/// The members of one object, each as the text it arrived as, every one named once.
struct Members(BTreeMap<String, Box<RawValue>>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: de::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct MembersVisitor;

        impl<'de> Visitor<'de> for MembersVisitor {
            type Value = Members;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("a JSON object")
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Members, A::Error> {
                let mut members = BTreeMap::new();
                while let Some(name) = map.next_key::<String>()? {
                    let value = map.next_value::<Box<RawValue>>()?;
                    if members.insert(name, value).is_some() {
                        return Err(de::Error::custom("a member is named twice"));
                    }
                }
                Ok(Members(members))
            }
        }

        deserializer.deserialize_map(MembersVisitor)
    }
}

/// The members of one JSON object, read with the protocol's rules.
pub struct Document {
    members: BTreeMap<String, Box<RawValue>>,
    /// The dotted path of this object inside the request, for `details.field`: empty at the top.
    path: String,
}

impl Document {
    /// Reads a request body. Anything that is not one JSON object is `invalid_input` with no member named.
    ///
    /// # Errors
    /// `invalid_input` when the body is not valid UTF-8 JSON, is not an object, or names a member twice.
    pub fn parse(body: &[u8]) -> Result<Self, Error> {
        let Members(members) = serde_json::from_slice(body).map_err(|_| Error::malformed_json())?;
        Ok(Self {
            members,
            path: String::new(),
        })
    }

    fn field(&self, name: &str) -> String {
        if self.path.is_empty() {
            name.to_owned()
        } else {
            format!("{}.{name}", self.path)
        }
    }

    /// The text of a member; `None` when it is absent or `null`.
    fn raw(&self, name: &str) -> Option<&str> {
        self.members
            .get(name)
            .map(|value| value.get().trim())
            .filter(|text| *text != "null")
    }

    /// Whether the member is present with any value, `null` included. Only the forbidden members of a send
    /// are read this way (BAP/1 4.4.6).
    #[must_use]
    pub fn present(&self, name: &str) -> bool {
        self.members.contains_key(name)
    }

    /// Whether the member has a value: it is present and is not `null`.
    #[must_use]
    pub fn has_value(&self, name: &str) -> bool {
        self.raw(name).is_some()
    }

    /// Whether the member is a JSON object; `None` when it is absent. A `null` is present and is not one.
    #[must_use]
    pub fn is_object(&self, name: &str) -> Option<bool> {
        self.members
            .get(name)
            .map(|value| value.get().trim_start().starts_with('{'))
    }

    fn string(&self, name: &str, mismatch: &dyn Fn() -> Error) -> Result<Option<String>, Error> {
        match self.raw(name) {
            None => Ok(None),
            Some(text) => serde_json::from_str::<String>(text)
                .map(Some)
                .map_err(|_| mismatch()),
        }
    }

    /// An optional text member of a BAP/1 shape; absent and `null` are `None`.
    ///
    /// # Errors
    /// `malformed_json` when the member is not a string.
    pub fn text(&self, name: &str) -> Result<Option<String>, Error> {
        self.string(name, &Error::malformed_json)
    }

    /// A required text member of a BAP/1 shape: present and not empty.
    ///
    /// # Errors
    /// `invalid_input` naming the member when it is absent or empty; `malformed_json` when it is not a
    /// string.
    pub fn required_text(&self, name: &str) -> Result<String, Error> {
        self.text(name)?
            .filter(|value| !value.is_empty())
            .ok_or_else(|| Error::required(&self.field(name)))
    }

    /// An optional text member the contract adds; absent and `null` are `None`.
    ///
    /// # Errors
    /// `invalid_input` naming the member when it is not a string.
    pub fn named_text(&self, name: &str) -> Result<Option<String>, Error> {
        self.string(name, &|| Error::wrong_type(&self.field(name), "a string"))
    }

    /// A required text member the contract adds: present and not empty.
    ///
    /// # Errors
    /// `invalid_input` naming the member when it is absent, empty or not a string.
    pub fn named_required_text(&self, name: &str) -> Result<String, Error> {
        self.named_text(name)?
            .filter(|value| !value.is_empty())
            .ok_or_else(|| Error::required(&self.field(name)))
    }

    /// The member as a whole number, when its text is one. JSON writes a whole number as digits with an
    /// optional sign; anything else, a fraction and an exponent included, is not one.
    fn whole(&self, name: &str) -> Option<Result<i64, ()>> {
        let text = self.raw(name)?;
        let digits = text.strip_prefix('-').unwrap_or(text);
        let whole = !digits.is_empty() && digits.bytes().all(|byte| byte.is_ascii_digit());
        Some(if whole {
            text.parse::<i64>().map_err(|_| ())
        } else {
            Err(())
        })
    }

    /// An optional whole number of a BAP/1 shape; absent and `null` are `None`.
    ///
    /// # Errors
    /// `malformed_json` when the member is not a whole number that fits 64 bits.
    pub fn integer(&self, name: &str) -> Result<Option<i64>, Error> {
        self.whole(name).transpose().map_err(|()| Error::malformed_json())
    }

    /// A whole number the contract adds, within `low..=high`; `default` when it is absent or `null`.
    ///
    /// # Errors
    /// `invalid_input` naming the member and its bounds when it is anything else.
    pub fn named_integer(&self, name: &str, default: i64, low: i64, high: i64) -> Result<i64, Error> {
        match self.whole(name) {
            None => Ok(default),
            Some(Ok(value)) if (low..=high).contains(&value) => Ok(value),
            Some(_) => Err(Error::out_of_range(&self.field(name), low, high)),
        }
    }

    /// An optional boolean the contract adds; absent and `null` are `None`.
    ///
    /// # Errors
    /// `invalid_input` naming the member when it is not a boolean.
    pub fn named_boolean(&self, name: &str) -> Result<Option<bool>, Error> {
        match self.raw(name) {
            None => Ok(None),
            Some("true") => Ok(Some(true)),
            Some("false") => Ok(Some(false)),
            Some(_) => Err(Error::wrong_type(&self.field(name), "a boolean")),
        }
    }

    /// An optional nested object; `None` when the member is absent, `null` or not an object.
    ///
    /// # Errors
    /// `malformed_json` when the member is an object that names a member twice.
    pub fn object(&self, name: &str) -> Result<Option<Self>, Error> {
        let Some(text) = self.raw(name).filter(|text| text.starts_with('{')) else {
            return Ok(None);
        };
        let Members(members) = serde_json::from_str(text).map_err(|_| Error::malformed_json())?;
        Ok(Some(Self {
            members,
            path: self.field(name),
        }))
    }

    /// A required list of non-empty strings with at least one element, of a BAP/1 shape.
    ///
    /// # Errors
    /// `invalid_input` naming the member when it is absent, empty or holds an empty string;
    /// `malformed_json` when it is not an array of strings.
    pub fn required_ids(&self, name: &str) -> Result<Vec<String>, Error> {
        let Some(text) = self.raw(name) else {
            return Err(Error::required(&self.field(name)));
        };
        let ids = serde_json::from_str::<Vec<String>>(text).map_err(|_| Error::malformed_json())?;
        if ids.is_empty() {
            return Err(Error::required(&self.field(name)));
        }
        if ids.iter().any(String::is_empty) {
            return Err(Error::invalid_value(&self.field(name), "non-empty message ids"));
        }
        Ok(ids)
    }
}

#[cfg(test)]
mod tests {
    use super::Document;
    use crate::error::{Code, Error};

    fn details(error: &Error, key: &str) -> Option<String> {
        error.details.get(key).cloned()
    }

    fn malformed(error: &Error) -> bool {
        error.code == Code::InvalidInput
            && details(error, "reason").as_deref() == Some("malformed_json")
            && details(error, "field").is_none()
    }

    #[test]
    fn reads_members_and_ignores_the_ones_it_is_not_asked_for() {
        let document =
            Document::parse(br#"{"a": "x", "n": 7, "b": true, "extra": [1, {"k": null}]}"#).unwrap();
        assert_eq!(document.text("a").unwrap().as_deref(), Some("x"));
        assert_eq!(document.integer("n").unwrap(), Some(7));
        assert_eq!(document.named_boolean("b").unwrap(), Some(true));
        assert_eq!(document.text("missing").unwrap(), None);
    }

    #[test]
    fn null_reads_as_absent_but_is_still_present() {
        let document = Document::parse(br#"{"a": null, "o": null}"#).unwrap();
        assert_eq!(document.text("a").unwrap(), None);
        assert!(document.present("a"));
        assert!(!document.present("b"));
        let error = document.required_text("a").unwrap_err();
        assert_eq!(details(&error, "reason").as_deref(), Some("required"));
        assert_eq!(document.is_object("o"), Some(false));
        assert_eq!(document.is_object("b"), None);
        assert!(document.object("o").unwrap().is_none());
    }

    #[test]
    fn refuses_what_is_not_one_object() {
        for body in [
            &b""[..],
            b"[]",
            b"\"x\"",
            b"{",
            b"{\"a\":1}{\"b\":2}",
            b"\xff\xfe",
            b"{\"a\":\"\xff\"}",
            b"null",
        ] {
            let Err(error) = Document::parse(body) else {
                panic!("accepted {body:?}");
            };
            assert!(malformed(&error), "{body:?}");
        }
    }

    #[test]
    fn refuses_a_member_named_twice_where_it_reads() {
        assert!(Document::parse(br#"{"a": 1, "a": 2}"#).is_err());
        let document = Document::parse(br#"{"a": {"b": 1, "b": 2}}"#).unwrap();
        assert!(malformed(&document.object("a").err().unwrap()));
    }

    #[test]
    fn what_it_is_not_asked_for_is_never_read() {
        // A number no machine holds and an object that names a member twice, in members no rule reads:
        // the request is judged on what it is asked for.
        let document = Document::parse(br#"{"a": "x", "big": 1e999, "junk": {"k": 1, "k": 2}}"#).unwrap();
        assert_eq!(document.text("a").unwrap().as_deref(), Some("x"));
        // A megabyte of small values costs its bytes and no more: nothing is built from it.
        let bulk = format!("{{\"a\":\"x\",\"bulk\":[{}0]}}", "0,".repeat(400_000));
        let document = Document::parse(bulk.as_bytes()).unwrap();
        assert_eq!(document.text("a").unwrap().as_deref(), Some("x"));
    }

    #[test]
    fn a_shape_member_of_the_wrong_type_is_a_document_that_does_not_read() {
        let document = Document::parse(
            br#"{"a": 1, "n": "7", "f": 1.5, "e": 1e2, "ids": [1], "big": 99999999999999999999}"#,
        )
        .unwrap();
        assert!(malformed(&document.text("a").unwrap_err()));
        assert!(malformed(&document.integer("n").unwrap_err()));
        assert!(malformed(&document.integer("f").unwrap_err()));
        assert!(malformed(&document.integer("e").unwrap_err()));
        assert!(malformed(&document.integer("big").unwrap_err()));
        assert!(malformed(&document.required_ids("ids").unwrap_err()));
    }

    #[test]
    fn a_member_the_contract_adds_is_named_when_it_is_wrong() {
        let document =
            Document::parse(br#"{"team_ref": 1, "limit": "7", "wait": 31, "flag": "yes", "zero": -0}"#)
                .unwrap();
        let error = document.named_text("team_ref").unwrap_err();
        assert_eq!(details(&error, "field").as_deref(), Some("team_ref"));
        assert_eq!(details(&error, "reason").as_deref(), Some("invalid_value"));
        let error = document.named_integer("limit", 50, 1, 200).unwrap_err();
        assert_eq!(details(&error, "reason").as_deref(), Some("out_of_range"));
        assert_eq!(details(&error, "max").as_deref(), Some("200"));
        assert!(document.named_integer("wait", 0, 0, 30).is_err());
        assert_eq!(document.named_integer("missing", 50, 1, 200).unwrap(), 50);
        assert!(document.named_boolean("flag").is_err());
        // Minus zero is zero.
        assert_eq!(document.integer("zero").unwrap(), Some(0));
        assert_eq!(
            details(&document.named_required_text("missing").unwrap_err(), "reason").as_deref(),
            Some("required")
        );
    }

    #[test]
    fn a_nested_member_is_named_with_its_path() {
        let document = Document::parse(br#"{"o": {"x": 3}}"#).unwrap();
        let nested = document.object("o").unwrap().unwrap();
        assert_eq!(
            details(&nested.required_text("y").unwrap_err(), "field").as_deref(),
            Some("o.y")
        );
        assert_eq!(nested.integer("x").unwrap(), Some(3));
    }

    #[test]
    fn reads_a_list_of_ids() {
        let document = Document::parse(br#"{"ids": ["a", "b"], "none": [], "bad": ["a", ""]}"#).unwrap();
        assert_eq!(
            document.required_ids("ids").unwrap(),
            vec!["a".to_owned(), "b".to_owned()]
        );
        assert_eq!(
            details(&document.required_ids("none").unwrap_err(), "reason").as_deref(),
            Some("required")
        );
        assert_eq!(
            details(&document.required_ids("missing").unwrap_err(), "reason").as_deref(),
            Some("required")
        );
        assert_eq!(
            details(&document.required_ids("bad").unwrap_err(), "reason").as_deref(),
            Some("invalid_value")
        );
    }

    #[test]
    fn a_deeply_nested_body_is_refused_not_overflowed() {
        let body = format!("{{\"a\":{}}}", "[".repeat(100_000));
        assert!(Document::parse(body.as_bytes()).is_err());
    }
}
