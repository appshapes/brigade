//! The gateways' configuration: one JSON file the operator names with `--gateway-config`.
//!
//! A server with no such file has no gateway: Slack and mail are closed until an operator opens them. The
//! file names each secret by the path of a file that holds it and never by its value, so the file itself can
//! be read by anyone who may read the server's command line, and no secret is ever an argument.
//!
//! ```json
//! {
//!   "slack": {
//!     "bot_token_file": "/etc/brigade/slack-bot-token",
//!     "signing_secret_file": "/etc/brigade/slack-signing-secret",
//!     "bot_handle": "brigade",
//!     "workspace": "example.slack.com",
//!     "teams": [{"join_secret_file": "/etc/brigade/ops.secret"}]
//!   },
//!   "mail": {
//!     "provider": "resend",
//!     "api_key_file": "/etc/brigade/resend-key",
//!     "webhook_secret_file": "/etc/brigade/resend-webhook-secret",
//!     "teams": [{
//!       "join_secret_file": "/etc/brigade/ops.secret",
//!       "inbox": "ops@in.example.com",
//!       "public_address": "ops@example.com",
//!       "from": "Ops <ops@example.com>"
//!     }]
//!   }
//! }
//! ```
//!
//! A member this build does not know is refused, so that a misspelt name never leaves a gateway half
//! configured without saying so.
//!
//! What the reader says of a file it refuses names the member that is wrong, and where in the file a text
//! that does not read stops reading. It never repeats a value from the file: a secret pasted where a path
//! belongs is a value like any other, and the server's own lines are no place for it.

use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;

/// The file as it is written.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct File {
    slack: Option<SlackFile>,
    mail: Option<MailFile>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SlackFile {
    bot_token_file: PathBuf,
    signing_secret_file: PathBuf,
    bot_handle: Option<String>,
    workspace: Option<String>,
    api: Option<String>,
    teams: Vec<TeamFile>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct MailFile {
    provider: String,
    api_key_file: PathBuf,
    webhook_secret_file: PathBuf,
    api: Option<String>,
    teams: Vec<MailTeamFile>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct TeamFile {
    join_secret_file: PathBuf,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct MailTeamFile {
    join_secret_file: PathBuf,
    inbox: String,
    public_address: String,
    from: String,
    #[serde(default)]
    allowed_senders: Vec<String>,
}

/// A secret read from its file. It has no `Debug`, and nothing prints it or the path it came from: the
/// path is a value of the configuration's, and may be a secret pasted where a path belongs.
#[derive(Clone, PartialEq, Eq)]
pub struct Secret {
    text: String,
    /// The file the secret was read from, so that it can be read again while the server runs. A secret a
    /// test made has none.
    file: Option<PathBuf>,
}

impl Secret {
    /// The secret itself, for the one place that sends or compares it.
    #[must_use]
    pub fn reveal(&self) -> &str {
        &self.text
    }

    /// What the secret's file holds now, without the white space around it: a team's join secret is
    /// replaced in its file when the team's owner has rotated it. `None` for a secret that came from no
    /// file. A file that cannot be read is told by the kind of the failure, and never by its path.
    pub fn read_again(&self) -> Option<Result<Self, std::io::ErrorKind>> {
        let file = self.file.as_ref()?;
        Some(
            fs::read_to_string(file)
                .map(|text| Self {
                    text: text.trim().to_owned(),
                    file: Some(file.clone()),
                })
                .map_err(|error| error.kind()),
        )
    }

    #[cfg(test)]
    pub fn of(text: &str) -> Self {
        Self {
            text: text.to_owned(),
            file: None,
        }
    }

    /// The secret a file holds, for a test of what follows when the file is replaced.
    #[cfg(test)]
    pub fn kept_in(file: &Path) -> Self {
        Self {
            text: fs::read_to_string(file).unwrap().trim().to_owned(),
            file: Some(file.to_path_buf()),
        }
    }
}

/// The Slack gateway as configured.
#[derive(Clone)]
pub struct Slack {
    /// The bot's token: what every call to Slack's Web API carries.
    pub bot_token: Secret,
    /// The signing secret: what a request from Slack is checked with.
    pub signing_secret: Secret,
    /// The bot's handle, as people write it after an `@`.
    pub bot_handle: String,
    /// The workspace's host, for the roster's line.
    pub workspace: String,
    /// The base of Slack's Web API.
    pub api: String,
    /// The join secret of each team the gateway serves.
    pub teams: Vec<Secret>,
}

/// Which mail provider carries the mail.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MailProvider {
    /// Resend.
    Resend,
    /// Postmark.
    Postmark,
}

/// One team's mail gateway.
#[derive(Clone)]
pub struct MailTeam {
    /// The team's join secret.
    pub join_secret: Secret,
    /// The address the provider receives mail at for this team.
    pub inbox: String,
    /// The address people are told to write to.
    pub public_address: String,
    /// The From of every mail the gateway sends for this team.
    pub from: String,
    /// Who may write in; empty admits anyone.
    pub allowed_senders: Vec<String>,
}

/// The mail gateway as configured.
#[derive(Clone)]
pub struct Mail {
    /// The provider.
    pub provider: MailProvider,
    /// The provider's API key.
    pub api_key: Secret,
    /// What the provider's webhook is checked with.
    pub webhook_secret: Secret,
    /// The base of the provider's API, when it is not the provider's own.
    pub api: Option<String>,
    /// Each team's gateway.
    pub teams: Vec<MailTeam>,
}

/// The gateways an operator configured.
#[derive(Clone, Default)]
pub struct Gateways {
    /// The Slack gateway, when there is one.
    pub slack: Option<Slack>,
    /// The mail gateway, when there is one.
    pub mail: Option<Mail>,
}

/// The default handle of the bot, and the default name of the workspace, as the Edge Functions have them.
const DEFAULT_BOT_HANDLE: &str = "brigade";
const DEFAULT_WORKSPACE: &str = "slack";

/// Slack's Web API.
const SLACK_API: &str = "https://slack.com/api";

/// How long Slack's signing secret is: thirty-two hexadecimal digits. A shorter one is a placeholder, and a
/// request signed with a placeholder is anyone's to forge.
const SIGNING_SECRET_CHARS: usize = 32;

/// Reads a secret from the file a member of the configuration names: the file's text without the white
/// space around it. `member` is the member's name, as `slack.bot_token_file`, and is all a failure says of
/// where the secret was to be: the path is a value of the file's, and may be a secret pasted in its place.
fn secret(path: &Path, member: &str, what: &str) -> Result<Secret, String> {
    let text =
        fs::read_to_string(path).map_err(|error| format!("{member} could not be read: {}", error.kind()))?;
    let text = text.trim();
    if text.is_empty() {
        return Err(format!("the file {member} names holds no {what}"));
    }
    Ok(Secret {
        text: text.to_owned(),
        file: Some(path.to_path_buf()),
    })
}

/// Where a text stops reading as a configuration, and in which way: the line, the column and the kind of
/// the failure, and nothing of the text itself.
fn unread(error: &serde_json::Error) -> String {
    let how = match error.classify() {
        serde_json::error::Category::Syntax => "it is not JSON there",
        serde_json::error::Category::Eof => "it ends too soon",
        serde_json::error::Category::Data => {
            "a member there is missing, is of another kind than it should be, or is not one this server knows"
        }
        serde_json::error::Category::Io => "it could not be read",
    };
    format!("line {}, column {}: {how}", error.line(), error.column())
}

/// Reads the configuration at `path`.
///
/// # Errors
/// A sentence for the operator. It names the file, which the command line named, and a member of it, and
/// never a value the file holds.
pub fn read(path: &Path) -> Result<Gateways, String> {
    let text = fs::read_to_string(path)
        .map_err(|error| format!("{} could not be read: {}", path.display(), error.kind()))?;
    let file: File = serde_json::from_str(&text).map_err(|error| {
        format!(
            "{} does not read as a gateway configuration: {}",
            path.display(),
            unread(&error)
        )
    })?;
    let refused = |why: &str| format!("{}: {why}", path.display());
    let slack = match file.slack {
        None => None,
        Some(slack) => {
            if slack.teams.is_empty() {
                return Err(refused("slack names no team"));
            }
            let signing_secret = secret(
                &slack.signing_secret_file,
                "slack.signing_secret_file",
                "signing secret",
            )
            .map_err(|why| refused(&why))?;
            if signing_secret.reveal().chars().count() < SIGNING_SECRET_CHARS {
                return Err(refused(
                    "the file slack.signing_secret_file names holds a signing secret shorter than Slack's \
                     thirty-two characters",
                ));
            }
            let mut teams = Vec::new();
            for (at, team) in slack.teams.iter().enumerate() {
                let member = format!("slack.teams[{at}].join_secret_file");
                teams.push(
                    secret(&team.join_secret_file, &member, "join secret").map_err(|why| refused(&why))?,
                );
            }
            Some(Slack {
                bot_token: secret(&slack.bot_token_file, "slack.bot_token_file", "bot token")
                    .map_err(|why| refused(&why))?,
                signing_secret,
                bot_handle: slack.bot_handle.unwrap_or_else(|| DEFAULT_BOT_HANDLE.to_owned()),
                workspace: slack.workspace.unwrap_or_else(|| DEFAULT_WORKSPACE.to_owned()),
                api: slack.api.unwrap_or_else(|| SLACK_API.to_owned()),
                teams,
            })
        }
    };
    let mail = match file.mail {
        None => None,
        Some(mail) => {
            let provider = match mail.provider.as_str() {
                "resend" => MailProvider::Resend,
                "postmark" => MailProvider::Postmark,
                _ => return Err(refused("mail.provider is resend or postmark")),
            };
            if mail.teams.is_empty() {
                return Err(refused("mail names no team"));
            }
            let mut teams: Vec<MailTeam> = Vec::new();
            for (at, team) in mail.teams.into_iter().enumerate() {
                for (name, value) in [("inbox", &team.inbox), ("public_address", &team.public_address)] {
                    if !brigade_gateway::is_address(value) {
                        return Err(refused(&format!("mail.teams[{at}].{name} is not an address")));
                    }
                }
                if team.from.trim().is_empty() {
                    return Err(refused(&format!("mail.teams[{at}].from is empty")));
                }
                let inbox = team.inbox.to_lowercase();
                // A mail is its team's by the inbox it was delivered to: two teams with one inbox would
                // leave every mail to the first of them.
                if teams.iter().any(|earlier| earlier.inbox == inbox) {
                    return Err(refused(&format!(
                        "mail.teams[{at}].inbox is the inbox of an earlier team as well"
                    )));
                }
                let member = format!("mail.teams[{at}].join_secret_file");
                teams.push(MailTeam {
                    join_secret: secret(&team.join_secret_file, &member, "join secret")
                        .map_err(|why| refused(&why))?,
                    inbox,
                    public_address: team.public_address,
                    from: team.from,
                    allowed_senders: team.allowed_senders,
                });
            }
            Some(Mail {
                provider,
                api_key: secret(&mail.api_key_file, "mail.api_key_file", "API key")
                    .map_err(|why| refused(&why))?,
                webhook_secret: secret(
                    &mail.webhook_secret_file,
                    "mail.webhook_secret_file",
                    "webhook secret",
                )
                .map_err(|why| refused(&why))?,
                api: mail.api,
                teams,
            })
        }
    };
    Ok(Gateways { slack, mail })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("brigade-gateway-config-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn put(dir: &Path, name: &str, text: &str) -> String {
        let path = dir.join(name);
        fs::write(&path, text).unwrap();
        path.display().to_string()
    }

    #[test]
    fn secrets_are_read_from_the_files_the_configuration_names() {
        let dir = scratch("read");
        let token = put(&dir, "token", "xoxb-not-a-real-token\n");
        let signing = put(&dir, "signing", "  0123456789abcdef0123456789abcdef  \n");
        let join = put(&dir, "join", "brg1.team.secret\n");
        let key = put(&dir, "key", "re_key\n");
        let hook = put(&dir, "hook", "whsec_c2VjcmV0\n");
        let config = put(
            &dir,
            "gateways.json",
            &format!(
                r#"{{
                  "slack": {{"bot_token_file": "{token}", "signing_secret_file": "{signing}",
                             "workspace": "example.slack.com", "teams": [{{"join_secret_file": "{join}"}}]}},
                  "mail": {{"provider": "resend", "api_key_file": "{key}", "webhook_secret_file": "{hook}",
                            "teams": [{{"join_secret_file": "{join}", "inbox": "Ops@In.Example.com",
                                        "public_address": "ops@example.com", "from": "Ops <ops@example.com>",
                                        "allowed_senders": ["@example.com"]}}]}}
                }}"#
            ),
        );
        let gateways = read(Path::new(&config)).unwrap();
        let slack = gateways.slack.unwrap();
        assert_eq!(slack.bot_token.reveal(), "xoxb-not-a-real-token");
        assert_eq!(slack.signing_secret.reveal(), "0123456789abcdef0123456789abcdef");
        assert_eq!(
            (
                slack.bot_handle.as_str(),
                slack.workspace.as_str(),
                slack.api.as_str()
            ),
            ("brigade", "example.slack.com", "https://slack.com/api")
        );
        assert_eq!(slack.teams.len(), 1);
        let mail = gateways.mail.unwrap();
        assert_eq!(mail.provider, MailProvider::Resend);
        assert_eq!(mail.teams[0].inbox, "ops@in.example.com");
        assert_eq!(mail.teams[0].allowed_senders, ["@example.com"]);
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_configuration_that_is_not_whole_is_refused_and_no_secret_is_repeated() {
        let dir = scratch("refused");
        let token = put(&dir, "token", "xoxb-MUST-NOT-LEAK\n");
        let empty = put(&dir, "empty", "\n");
        let join = put(&dir, "join", "brg1.team.secret\n");
        for (name, text, says) in [
            ("unknown", r#"{"slak": {}}"#.to_owned(), "does not read"),
            (
                "no-team",
                format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{token}", "teams": []}}}}"#
                ),
                "names no team",
            ),
            (
                "empty-secret",
                format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{empty}",
                                    "teams": [{{"join_secret_file": "{join}"}}]}}}}"#
                ),
                "holds no signing secret",
            ),
            (
                "provider",
                format!(
                    r#"{{"mail": {{"provider": "smtp", "api_key_file": "{token}", "webhook_secret_file": "{token}",
                                   "teams": []}}}}"#
                ),
                "resend or postmark",
            ),
            (
                "inbox",
                format!(
                    r#"{{"mail": {{"provider": "postmark", "api_key_file": "{token}", "webhook_secret_file": "{token}",
                                   "teams": [{{"join_secret_file": "{join}", "inbox": "not an address",
                                               "public_address": "ops@example.com", "from": "ops@example.com"}}]}}}}"#
                ),
                "inbox is not an address",
            ),
        ] {
            let config = put(&dir, &format!("{name}.json"), &text);
            let refused = read(Path::new(&config)).err().unwrap();
            assert!(refused.contains(says), "{name}: {refused}");
            assert!(!refused.contains("MUST-NOT-LEAK"), "{name}: {refused}");
        }
        assert!(read(&dir.join("missing.json")).is_err());
        // A file with neither gateway is a server with none.
        let none = put(&dir, "none.json", "{}");
        let gateways = read(Path::new(&none)).unwrap();
        assert!(gateways.slack.is_none() && gateways.mail.is_none());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_signing_secret_shorter_than_slacks_is_refused() {
        let dir = scratch("signing");
        let token = put(&dir, "token", "xoxb-not-a-real-token\n");
        let join = put(&dir, "join", "brg1.team.secret\n");
        let with = |signing: &str| {
            let signing = put(&dir, "signing", signing);
            let config = put(
                &dir,
                "gateways.json",
                &format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{signing}",
                                    "teams": [{{"join_secret_file": "{join}"}}]}}}}"#
                ),
            );
            read(Path::new(&config)).map(|gateways| gateways.slack.is_some())
        };
        // A placeholder, with which anyone could sign a request, and a secret one character short.
        for short in [
            "x\n",
            "placeholder-MUST-NOT-LEAK\n",
            "0123456789abcdef0123456789abcde\n",
        ] {
            let refused = with(short).err().unwrap();
            assert!(
                refused.contains("slack.signing_secret_file") && refused.contains("shorter"),
                "{refused}"
            );
            assert!(!refused.contains(short.trim()), "{refused}");
        }
        assert_eq!(with("0123456789abcdef0123456789abcdef\n"), Ok(true));
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn two_mail_teams_with_one_inbox_are_refused() {
        let dir = scratch("inboxes");
        let key = put(&dir, "key", "a-key\n");
        let join = put(&dir, "join", "brg1.team.secret\n");
        let other = put(&dir, "other", "brg1.other.secret\n");
        let with = |second: &str| {
            let config = put(
                &dir,
                "gateways.json",
                &format!(
                    r#"{{"mail": {{"provider": "postmark", "api_key_file": "{key}", "webhook_secret_file": "{key}",
                        "teams": [
                          {{"join_secret_file": "{join}", "inbox": "ops@in.example.com",
                            "public_address": "ops@example.com", "from": "ops@example.com"}},
                          {{"join_secret_file": "{other}", "inbox": "{second}",
                            "public_address": "web@example.com", "from": "web@example.com"}}]}}}}"#
                ),
            );
            read(Path::new(&config)).map(|gateways| gateways.mail.map_or(0, |mail| mail.teams.len()))
        };
        // The same inbox, however it is written: a mail for it could only ever be the first team's.
        for same in ["ops@in.example.com", "Ops@In.Example.COM"] {
            let refused = with(same).err().unwrap();
            assert!(
                refused.contains("mail.teams[1].inbox is the inbox of an earlier team"),
                "{refused}"
            );
            assert!(
                !refused.to_lowercase().contains("ops@in.example.com"),
                "{refused}"
            );
        }
        assert_eq!(with("web@in.example.com"), Ok(2));
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_refusal_names_the_member_and_never_repeats_a_value_of_the_file() {
        let dir = scratch("values");
        let token = put(&dir, "token", "xoxb-not-a-real-token\n");
        let signing = put(&dir, "signing", "0123456789abcdef0123456789abcdef\n");
        let join = put(&dir, "join", "brg1.team.secret\n");
        const PASTED: &str = "PASTED-SECRET-MUST-NOT-LEAK";
        for (name, text, says) in [
            // A secret pasted where the path of its file belongs.
            (
                "token-for-path",
                format!(
                    r#"{{"slack": {{"bot_token_file": "xoxb-{PASTED}", "signing_secret_file": "{signing}",
                                    "teams": [{{"join_secret_file": "{join}"}}]}}}}"#
                ),
                "slack.bot_token_file could not be read: entity not found",
            ),
            (
                "signing-for-path",
                format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{PASTED}",
                                    "teams": [{{"join_secret_file": "{join}"}}]}}}}"#
                ),
                "slack.signing_secret_file could not be read: entity not found",
            ),
            (
                "join-for-path",
                format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{signing}",
                                    "teams": [{{"join_secret_file": "{join}"}},
                                              {{"join_secret_file": "brg1.team.{PASTED}"}}]}}}}"#
                ),
                "slack.teams[1].join_secret_file could not be read: entity not found",
            ),
            (
                "key-for-path",
                format!(
                    r#"{{"mail": {{"provider": "resend", "api_key_file": "re_{PASTED}", "webhook_secret_file": "{token}",
                                   "teams": [{{"join_secret_file": "{join}", "inbox": "ops@in.example.com",
                                               "public_address": "ops@example.com", "from": "ops@example.com"}}]}}}}"#
                ),
                "mail.api_key_file could not be read: entity not found",
            ),
            // A join secret written where a team is described, and one given as a member's name: the
            // reader's own words for either would quote it.
            (
                "join-inline",
                format!(
                    r#"{{"slack": {{"bot_token_file": "{token}", "signing_secret_file": "{signing}",
  "teams": ["brg1.team.{PASTED}"]}}}}"#
                ),
                "does not read as a gateway configuration: line 2, column",
            ),
            (
                "secret-as-member",
                format!(r#"{{"slack": {{"xoxb-{PASTED}": true}}}}"#),
                "does not read as a gateway configuration: line 1, column",
            ),
            (
                "not-json",
                format!(r#"{{"slack": {PASTED}"#),
                "line 1, column 11: it is not JSON there",
            ),
            (
                "cut-short",
                format!(r#"{{"slack": {{"bot_token_file": "{PASTED}"#),
                "it ends too soon",
            ),
            // An address that is none, and a provider that is none, are named by their members.
            (
                "public-address",
                format!(
                    r#"{{"mail": {{"provider": "resend", "api_key_file": "{token}", "webhook_secret_file": "{token}",
                                   "teams": [{{"join_secret_file": "{join}", "inbox": "ops@in.example.com",
                                               "public_address": "{PASTED}", "from": "ops@example.com"}}]}}}}"#
                ),
                "mail.teams[0].public_address is not an address",
            ),
            (
                "provider",
                format!(
                    r#"{{"mail": {{"provider": "{PASTED}", "api_key_file": "{token}", "webhook_secret_file": "{token}",
                                   "teams": []}}}}"#
                ),
                "mail.provider is resend or postmark",
            ),
        ] {
            let config = put(&dir, &format!("{name}.json"), &text);
            let refused = read(Path::new(&config)).err().unwrap();
            assert!(refused.contains(says), "{name}: {refused}");
            assert!(!refused.contains(PASTED), "{name}: {refused}");
            assert!(
                !refused.contains("xoxb-") && !refused.contains("brg1."),
                "{name}: {refused}"
            );
        }
        let _ = fs::remove_dir_all(&dir);
    }
}
