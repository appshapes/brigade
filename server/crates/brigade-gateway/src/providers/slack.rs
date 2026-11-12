//! The Slack Web API, as much of it as the gateway uses (`supabase/functions/_shared/providers/slack.ts`):
//! posting, opening a direct message, looking people and channels up, joining a public channel, a
//! reaction, a permalink, and `auth.test` for the bot's own identity.
//!
//! Every call is one HTTPS request with the bot token, and this module is its two ends: a `…_request`
//! builds the request for the caller to make, and the `…_answer` beside it reads what Slack answered, a
//! status and a body. A search through a paged list is a [`UserSearch`] or a [`ChannelSearch`], which the
//! caller only loops over. A body is the text the Fetch standard's `text()` gives: UTF-8, lossily decoded,
//! without a byte order mark.
//!
//! An answer Slack marks `ok: false`, and one whose status is not a 2xx, is a [`SlackError`] carrying
//! Slack's error code. The TypeScript had two more ways to fail, neither a `SlackError` there: an answer
//! that could not be read (a 2xx that is not JSON, or lacks the member a call reads), and no answer at
//! all. Both failed with an error of the runtime's, which no caller took for one that a later attempt
//! mends. Here they are a [`SlackError`] too, with a code and a text of this crate's
//! ([`UNREADABLE_ANSWER`], [`NO_ANSWER`]) and the same standing: not retryable.

use std::fmt;

use serde::Serialize;
use serde::ser::{SerializeStruct, Serializer};

use crate::connector::{HeadersObject, HttpRequest, is_ok, stringify};
use crate::js;
use crate::json::{Json, text_of};

/// Where Slack's Web API is.
pub const API: &str = "https://slack.com/api";
/// How many users or channels one page of a search asks for.
pub const PAGE_SIZE: usize = 200;
/// How many pages a search reads before it gives up: a name beyond them is not found.
pub const MAX_PAGES: usize = 20;
/// The code of a [`SlackError`] for an answer that could not be read.
pub const UNREADABLE_ANSWER: &str = "unreadable_answer";
/// The code of a [`SlackError`] for a request that got no answer.
pub const NO_ANSWER: &str = "no_answer";

/// A failed call.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SlackError {
    /// Slack's error code; `http_<status>` for an answer that is not a 2xx.
    pub code: String,
    /// What went wrong, as the sending session is told. It names the method with its query, so the
    /// failure of a lookup by email carries the address.
    pub message: String,
}

impl SlackError {
    /// A failure with Slack's code, and `slack: <code>` for its text when none is given.
    #[must_use]
    pub fn new(code: impl Into<String>, message: Option<String>) -> Self {
        let code = code.into();
        Self {
            message: message.unwrap_or_else(|| format!("slack: {code}")),
            code,
        }
    }

    /// The failure of a request that got no answer. `why` is what the HTTP layer says of it, and is the
    /// whole text, as the runtime's own error was in the TypeScript.
    #[must_use]
    pub fn unanswered(why: &str) -> Self {
        Self::new(NO_ANSWER, Some(why.to_owned()))
    }

    fn unreadable(method: &str) -> Self {
        Self::new(
            UNREADABLE_ANSWER,
            Some(format!("slack: {method}: the answer could not be read")),
        )
    }

    /// Whether a later attempt can succeed: Slack asked for a pause, or the platform failed; everything
    /// else is this request's fault.
    ///
    /// A 429 that comes as a status and not as Slack's `ratelimited` has the code `http_429`, which is not
    /// one of these: it is final, as it was in the TypeScript.
    #[must_use]
    pub fn retryable(&self) -> bool {
        matches!(
            self.code.as_str(),
            "ratelimited" | "internal_error" | "service_unavailable" | "fatal_error"
        ) || self.code.starts_with("http_5")
    }
}

impl fmt::Display for SlackError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for SlackError {}

/// A person on Slack.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SlackUser {
    /// The user id.
    pub id: String,
    /// The name to show: the profile's display name, else its real name, else the user's.
    pub name: Option<String>,
    /// The profile's email address, lowercase.
    pub email: Option<String>,
    /// Whether the user is a bot.
    pub is_bot: bool,
    /// Whether the account was deactivated.
    pub deleted: bool,
}

/// A channel on Slack.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SlackChannel {
    /// The channel id.
    pub id: String,
    /// The channel's name.
    pub name: Option<String>,
    /// Whether it is a direct message.
    pub is_im: bool,
    /// Whether it is a private channel.
    pub is_private: bool,
    /// Whether the bot is in it.
    pub is_member: bool,
}

/// What `auth.test` says of the bot.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AuthTest {
    /// The bot's own user id.
    pub user_id: String,
    /// The bot's bot id.
    pub bot_id: Option<String>,
    /// The workspace's name.
    pub team: String,
    /// The workspace's host: its URL without the scheme and the slash that ends it.
    pub team_domain: String,
    /// The workspace's URL.
    pub url: String,
}

/// A posted message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Posted {
    /// The message's timestamp, which is its id in its channel.
    pub ts: String,
    /// The channel it is in: for a post to a person, the direct message Slack opened.
    pub channel: String,
}

/// What a post carries beside its text, for whoever reads the channel's history by program.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Metadata {
    /// The kind of event the post is.
    pub event_type: String,
    /// The event's own fields, name and value, in the order they are sent.
    pub event_payload: Vec<(String, String)>,
}

impl Serialize for Metadata {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut shape = serializer.serialize_struct("Metadata", 2)?;
        shape.serialize_field("event_type", &self.event_type)?;
        shape.serialize_field("event_payload", &HeadersObject(&self.event_payload))?;
        shape.end()
    }
}

/// A message to post.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PostMessage {
    /// The channel to post in.
    pub channel: String,
    /// The text, in Slack's mrkdwn.
    pub text: String,
    /// The timestamp of the root of the thread to post into. `None`, or an empty one, is a new post.
    pub thread_ts: Option<String>,
    /// What the post carries beside its text.
    pub metadata: Option<Metadata>,
}

/// A message only one person in a channel sees.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PostEphemeral {
    /// The channel to post in.
    pub channel: String,
    /// The person who sees it.
    pub user: String,
    /// The text, in Slack's mrkdwn.
    pub text: String,
    /// The timestamp of the root of the thread to post into. `None`, or an empty one, is the channel.
    pub thread_ts: Option<String>,
}

#[derive(Serialize)]
struct PostMessageBody<'a> {
    channel: &'a str,
    text: &'a str,
    unfurl_links: bool,
    unfurl_media: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    thread_ts: Option<&'a str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    metadata: Option<&'a Metadata>,
}

#[derive(Serialize)]
struct PostEphemeralBody<'a> {
    channel: &'a str,
    user: &'a str,
    text: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    thread_ts: Option<&'a str>,
}

#[derive(Serialize)]
struct OpenBody<'a> {
    users: &'a str,
}

#[derive(Serialize)]
struct JoinBody<'a> {
    channel: &'a str,
}

#[derive(Serialize)]
struct ReactionBody<'a> {
    channel: &'a str,
    timestamp: &'a str,
    name: &'a str,
}

/// One Slack app's bot, as the gateway holds it. It has no `Debug`: it holds the bot's token.
#[derive(Clone)]
pub struct Slack {
    /// The bot token.
    pub bot_token: String,
    /// The API's base URL: [`API`], or a stand-in's.
    pub base_url: String,
}

/// The scopes a bot token carries, as Slack's `x-oauth-scopes` header on an answer lists them. `None`
/// when the answer has no such header. `header` is the header's value; where it came more than once, its
/// values joined by commas.
#[must_use]
pub fn granted_scopes(header: Option<&str>) -> Option<Vec<String>> {
    Some(
        header?
            .split(',')
            .map(js::trim)
            .filter(|scope| !scope.is_empty())
            .map(str::to_owned)
            .collect(),
    )
}

impl Slack {
    /// A bot at Slack's own API.
    #[must_use]
    pub fn new(bot_token: impl Into<String>) -> Self {
        Self {
            bot_token: bot_token.into(),
            base_url: API.to_owned(),
        }
    }

    fn authorization(&self) -> (&'static str, String) {
        ("Authorization", format!("Bearer {}", self.bot_token))
    }

    /// A lookup: a GET whose arguments are in the query. `method` is the method's name and its query.
    fn get(&self, method: &str) -> HttpRequest {
        HttpRequest {
            method: "GET",
            url: format!("{}/{method}", self.base_url),
            headers: vec![self.authorization()],
            body: None,
        }
    }

    /// A call with a JSON body.
    fn post(&self, method: &str, body: &impl Serialize) -> HttpRequest {
        HttpRequest {
            method: "POST",
            url: format!("{}/{method}", self.base_url),
            headers: vec![
                self.authorization(),
                ("Content-Type", "application/json; charset=utf-8".to_owned()),
            ],
            body: Some(stringify(body)),
        }
    }

    /// `auth.test`: who the bot is. Its answer's `x-oauth-scopes` header is also where the gateway reads
    /// the token's scopes ([`granted_scopes`]).
    #[must_use]
    pub fn auth_test_request(&self) -> HttpRequest {
        #[derive(Serialize)]
        struct Nothing {}
        self.post("auth.test", &Nothing {})
    }

    /// Reads the answer to [`Slack::auth_test_request`]. An answer that names no user has the user id
    /// `undefined`: the TypeScript's `String(d.user_id)`.
    ///
    /// # Errors
    /// Slack refused the call, or its answer could not be read.
    pub fn auth_test_answer(status: u16, body: &str) -> Result<AuthTest, SlackError> {
        let answer = read("auth.test", status, body)?;
        let url = text_of(answer.get("url"));
        let host = url
            .strip_prefix("https://")
            .or_else(|| url.strip_prefix("http://"))
            .unwrap_or(&url);
        let team_domain = host.strip_suffix('/').unwrap_or(host).to_owned();
        Ok(AuthTest {
            user_id: string_of(answer.member("user_id")),
            bot_id: answer.member("bot_id").and_then(Json::as_str).map(str::to_owned),
            team: text_of(answer.get("team")),
            team_domain,
            url,
        })
    }

    /// `chat.postMessage`. Links and media in the text are not unfurled.
    #[must_use]
    pub fn post_message_request(&self, message: &PostMessage) -> HttpRequest {
        self.post(
            "chat.postMessage",
            &PostMessageBody {
                channel: &message.channel,
                text: &message.text,
                unfurl_links: false,
                unfurl_media: false,
                thread_ts: message.thread_ts.as_deref().filter(|ts| !ts.is_empty()),
                metadata: message.metadata.as_ref(),
            },
        )
    }

    /// Reads the answer to [`Slack::post_message_request`].
    ///
    /// # Errors
    /// Slack refused the post, or its answer could not be read. The code `not_in_channel` is the one a
    /// join and a second post answer.
    pub fn post_message_answer(status: u16, body: &str) -> Result<Posted, SlackError> {
        let answer = read("chat.postMessage", status, body)?;
        Ok(Posted {
            ts: string_of(answer.member("ts")),
            channel: string_of(answer.member("channel")),
        })
    }

    /// `chat.postEphemeral`.
    #[must_use]
    pub fn post_ephemeral_request(&self, message: &PostEphemeral) -> HttpRequest {
        self.post(
            "chat.postEphemeral",
            &PostEphemeralBody {
                channel: &message.channel,
                user: &message.user,
                text: &message.text,
                thread_ts: message.thread_ts.as_deref().filter(|ts| !ts.is_empty()),
            },
        )
    }

    /// Reads the answer to [`Slack::post_ephemeral_request`].
    ///
    /// # Errors
    /// Slack refused the post, or its answer could not be read.
    pub fn post_ephemeral_answer(status: u16, body: &str) -> Result<(), SlackError> {
        read("chat.postEphemeral", status, body).map(|_| ())
    }

    /// `conversations.open`: the direct message with one person.
    #[must_use]
    pub fn open_dm_request(&self, user: &str) -> HttpRequest {
        self.post("conversations.open", &OpenBody { users: user })
    }

    /// Reads the answer to [`Slack::open_dm_request`] as the id of the direct message's channel.
    ///
    /// # Errors
    /// Slack refused the call, or its answer names no channel.
    pub fn open_dm_answer(status: u16, body: &str) -> Result<String, SlackError> {
        let method = "conversations.open";
        let answer = read(method, status, body)?;
        let channel = answer
            .get("channel")
            .ok_or_else(|| SlackError::unreadable(method))?;
        Ok(string_of(channel.member("id")))
    }

    /// `users.info`: one person, by their user id.
    #[must_use]
    pub fn user_info_request(&self, user: &str) -> HttpRequest {
        self.get(&user_info(user))
    }

    /// Reads the answer to [`Slack::user_info_request`] for `user`.
    ///
    /// # Errors
    /// Slack refused the call, or its answer names no user.
    pub fn user_info_answer(user: &str, status: u16, body: &str) -> Result<SlackUser, SlackError> {
        let method = user_info(user);
        user_of(&method, read(&method, status, body)?.get("user"))
    }

    /// `users.lookupByEmail`: one person, by their email address.
    #[must_use]
    pub fn lookup_by_email_request(&self, email: &str) -> HttpRequest {
        self.get(&lookup_by_email(email))
    }

    /// Reads the answer to [`Slack::lookup_by_email_request`] for `email`. `None` when nobody on the
    /// workspace has the address.
    ///
    /// # Errors
    /// Slack refused the call for another reason, or its answer names no user.
    pub fn lookup_by_email_answer(
        email: &str,
        status: u16,
        body: &str,
    ) -> Result<Option<SlackUser>, SlackError> {
        let method = lookup_by_email(email);
        match read(&method, status, body) {
            Ok(answer) => user_of(&method, answer.get("user")).map(Some),
            Err(error) if error.code == "users_not_found" => Ok(None),
            Err(error) => Err(error),
        }
    }

    /// `conversations.info`: one channel, by its id.
    #[must_use]
    pub fn channel_info_request(&self, channel: &str) -> HttpRequest {
        self.get(&channel_info(channel))
    }

    /// Reads the answer to [`Slack::channel_info_request`] for `channel`. `None` when Slack knows no such
    /// channel, or the token may not read it.
    ///
    /// # Errors
    /// Slack refused the call for another reason, or its answer names no channel.
    pub fn channel_info_answer(
        channel: &str,
        status: u16,
        body: &str,
    ) -> Result<Option<SlackChannel>, SlackError> {
        let method = channel_info(channel);
        match read(&method, status, body) {
            Ok(answer) => channel_of(&method, answer.get("channel")).map(Some),
            Err(error) if error.code == "channel_not_found" || error.code == "missing_scope" => Ok(None),
            Err(error) => Err(error),
        }
    }

    /// `conversations.join`: the bot joins a public channel.
    #[must_use]
    pub fn join_channel_request(&self, channel: &str) -> HttpRequest {
        self.post("conversations.join", &JoinBody { channel })
    }

    /// Reads the answer to [`Slack::join_channel_request`].
    ///
    /// # Errors
    /// Slack refused the join, or its answer could not be read.
    pub fn join_channel_answer(status: u16, body: &str) -> Result<(), SlackError> {
        read("conversations.join", status, body).map(|_| ())
    }

    /// `reactions.add`: the bot reacts to a message.
    #[must_use]
    pub fn add_reaction_request(&self, channel: &str, ts: &str, name: &str) -> HttpRequest {
        self.post(
            "reactions.add",
            &ReactionBody {
                channel,
                timestamp: ts,
                name,
            },
        )
    }

    /// Reads the answer to [`Slack::add_reaction_request`]. A reaction that is there already, and one the
    /// token may not add, are no failure.
    ///
    /// # Errors
    /// Slack refused the reaction for another reason, or its answer could not be read.
    pub fn add_reaction_answer(status: u16, body: &str) -> Result<(), SlackError> {
        match read("reactions.add", status, body) {
            Err(error) if error.code != "already_reacted" && error.code != "missing_scope" => Err(error),
            _ => Ok(()),
        }
    }

    /// `chat.getPermalink`: a link to a message.
    #[must_use]
    pub fn permalink_request(&self, channel: &str, ts: &str) -> HttpRequest {
        self.get(&format!(
            "chat.getPermalink?channel={}&message_ts={}",
            js::encode_uri_component(channel),
            js::encode_uri_component(ts)
        ))
    }

    /// Reads the answer to [`Slack::permalink_request`]. A failure of any kind is `None`, and so is a
    /// request that got no answer: a message without its link is still carried.
    #[must_use]
    pub fn permalink_answer(status: u16, body: &str) -> Option<String> {
        read("chat.getPermalink", status, body)
            .ok()?
            .member("permalink")
            .and_then(Json::as_str)
            .map(str::to_owned)
    }
}

fn user_info(user: &str) -> String {
    format!("users.info?user={}", js::encode_uri_component(user))
}

fn lookup_by_email(email: &str) -> String {
    format!("users.lookupByEmail?email={}", js::encode_uri_component(email))
}

fn channel_info(channel: &str) -> String {
    format!("conversations.info?channel={}", js::encode_uri_component(channel))
}

/// Where a search through a paged list is: the page it asks for next, and how many it has asked for.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Paging {
    cursor: String,
    asked: usize,
    over: bool,
}

impl Paging {
    fn new() -> Self {
        Self {
            cursor: String::new(),
            asked: 0,
            over: false,
        }
    }

    /// `&cursor=…` for every page but the first.
    fn query(&self) -> String {
        if self.cursor.is_empty() {
            String::new()
        } else {
            format!("&cursor={}", js::encode_uri_component(&self.cursor))
        }
    }

    /// Moves to the page an answer names. The search is over when it names none, and after
    /// [`MAX_PAGES`] pages.
    fn turn(&mut self, answer: &Json) {
        self.cursor = text_of(
            answer
                .get("response_metadata")
                .and_then(|metadata| metadata.get("next_cursor")),
        );
        self.asked += 1;
        self.over = self.cursor.is_empty() || self.asked >= MAX_PAGES;
    }
}

/// A search of the workspace's users for one by name, a page of `users.list` at a time.
///
/// ```text
/// let mut search = UserSearch::new(name);
/// while let Some(request) = search.request(&slack) {
///     let reply = send(request)?;
///     if let Some(user) = search.answer(reply.status, &reply.body)? {
///         return Ok(Some(user));
///     }
/// }
/// Ok(None)
/// ```
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UserSearch {
    want: String,
    paging: Paging,
}

impl UserSearch {
    /// A search for `name`, in any case, among a user's name, display name and real name and the
    /// normalized forms of the last two. Deactivated users and bots are passed over, the gateway's own
    /// bot among them.
    #[must_use]
    pub fn new(name: &str) -> Self {
        Self {
            want: name.to_lowercase(),
            paging: Paging::new(),
        }
    }

    fn method(&self) -> String {
        format!("users.list?limit={PAGE_SIZE}{}", self.paging.query())
    }

    /// The request for the next page; `None` once the search is over.
    #[must_use]
    pub fn request(&self, slack: &Slack) -> Option<HttpRequest> {
        (!self.paging.over).then(|| slack.get(&self.method()))
    }

    /// Reads a page's answer: the first user on it with the name, or `None` when the page has none.
    ///
    /// # Errors
    /// Slack refused the call, or its answer could not be read. The search is over.
    pub fn answer(&mut self, status: u16, body: &str) -> Result<Option<SlackUser>, SlackError> {
        let method = self.method();
        self.paging.over = true;
        let answer = read(&method, status, body)?;
        for member in listed(&method, answer.get("members"))? {
            let user = user_of(&method, Some(member))?;
            if user.deleted || user.is_bot {
                continue;
            }
            let profile = member.get("profile");
            let named = [
                member.member("name"),
                profile.and_then(|profile| profile.member("display_name")),
                profile.and_then(|profile| profile.member("real_name")),
                profile.and_then(|profile| profile.member("display_name_normalized")),
                profile.and_then(|profile| profile.member("real_name_normalized")),
            ]
            .into_iter()
            .flatten()
            .filter_map(Json::as_str)
            .any(|name| name.to_lowercase() == self.want);
            if named {
                return Ok(Some(user));
            }
        }
        self.paging.turn(&answer);
        Ok(None)
    }
}

/// A search of the workspace's public and private channels, the archived ones left out, for one by name,
/// a page of `conversations.list` at a time. It is driven as a [`UserSearch`] is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChannelSearch {
    want: String,
    paging: Paging,
}

impl ChannelSearch {
    /// A search for the channel named `name`, in any case.
    #[must_use]
    pub fn new(name: &str) -> Self {
        Self {
            want: name.to_lowercase(),
            paging: Paging::new(),
        }
    }

    fn method(&self) -> String {
        format!(
            "conversations.list?limit={PAGE_SIZE}&types=public_channel,private_channel&exclude_archived=true{}",
            self.paging.query()
        )
    }

    /// The request for the next page; `None` once the search is over.
    #[must_use]
    pub fn request(&self, slack: &Slack) -> Option<HttpRequest> {
        (!self.paging.over).then(|| slack.get(&self.method()))
    }

    /// Reads a page's answer: the first channel on it with the name, or `None` when the page has none.
    ///
    /// # Errors
    /// Slack refused the call, or its answer could not be read. The search is over.
    pub fn answer(&mut self, status: u16, body: &str) -> Result<Option<SlackChannel>, SlackError> {
        let method = self.method();
        self.paging.over = true;
        let answer = read(&method, status, body)?;
        for channel in listed(&method, answer.get("channels"))? {
            if *channel == Json::Null {
                return Err(SlackError::unreadable(&method));
            }
            let named = channel
                .member("name")
                .and_then(Json::as_str)
                .is_some_and(|name| name.to_lowercase() == self.want);
            if named {
                return channel_of(&method, Some(channel)).map(Some);
            }
        }
        self.paging.turn(&answer);
        Ok(None)
    }
}

/// Reads an answer as the TypeScript's `call` does: a status that is not a 2xx fails with the code
/// `http_<status>`, and a body whose `ok` is not `true` with Slack's `error`, or `unknown_error` where it
/// names none. `method` is the method's name and its query, which the failure's text repeats.
fn read(method: &str, status: u16, body: &str) -> Result<Json, SlackError> {
    if !is_ok(status) {
        return Err(SlackError::new(
            format!("http_{status}"),
            Some(format!("slack: {method} answered {status}")),
        ));
    }
    let Some(answer) = Json::parse(body).filter(|answer| *answer != Json::Null) else {
        return Err(SlackError::unreadable(method));
    };
    if answer.member("ok") != Some(&Json::Bool(true)) {
        let code = answer
            .get("error")
            .map_or_else(|| "unknown_error".to_owned(), Json::text);
        let message = format!("slack: {method}: {code}");
        return Err(SlackError::new(code, Some(message)));
    }
    Ok(answer)
}

/// `String(value)` of a member that may be missing: `undefined` for one that is.
fn string_of(value: Option<&Json>) -> String {
    value.map_or_else(|| "undefined".to_owned(), Json::text)
}

/// The elements of a list in an answer. A list that is missing has none; neither has one that is a string,
/// whose characters the TypeScript walked and found no name in.
fn listed<'a>(method: &str, list: Option<&'a Json>) -> Result<&'a [Json], SlackError> {
    match list {
        None | Some(Json::Text(_)) => Ok(&[]),
        Some(Json::List(items)) => Ok(items),
        Some(_) => Err(SlackError::unreadable(method)),
    }
}

fn user_of(method: &str, user: Option<&Json>) -> Result<SlackUser, SlackError> {
    let user = user
        .filter(|user| **user != Json::Null)
        .ok_or_else(|| SlackError::unreadable(method))?;
    let profile = user.get("profile");
    let of_profile = |name: &str| profile.and_then(|profile| profile.member(name));
    // The first of the four that JavaScript takes for true, as text; a name that is empty text is none.
    let name = [
        of_profile("display_name"),
        of_profile("real_name"),
        user.member("real_name"),
        user.member("name"),
    ]
    .into_iter()
    .flatten()
    .find(|value| value.truthy())
    .map(Json::text)
    .filter(|name| !name.is_empty());
    Ok(SlackUser {
        id: string_of(user.member("id")),
        name,
        email: of_profile("email").and_then(Json::as_str).map(str::to_lowercase),
        is_bot: user.member("is_bot").is_some_and(Json::truthy),
        deleted: user.member("deleted").is_some_and(Json::truthy),
    })
}

fn channel_of(method: &str, channel: Option<&Json>) -> Result<SlackChannel, SlackError> {
    let channel = channel
        .filter(|channel| **channel != Json::Null)
        .ok_or_else(|| SlackError::unreadable(method))?;
    let truthy = |name: &str| channel.member(name).is_some_and(Json::truthy);
    Ok(SlackChannel {
        id: string_of(channel.member("id")),
        name: channel.member("name").and_then(Json::as_str).map(str::to_owned),
        is_im: truthy("is_im"),
        is_private: truthy("is_private"),
        is_member: truthy("is_member"),
    })
}
