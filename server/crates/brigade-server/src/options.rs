//! What the operator tells the server on its command line.
//!
//! Nothing secret is ever an argument: the creation token and the TLS key are files, named by path, and so is
//! every secret of the gateways, by the configuration that is itself a file, and the join secret of a team
//! the server takes in. A team's reference and a principal's are no secrets: every member's roster shows
//! them, so the commands that name a team's owner and take a team back take them as arguments. The join
//! secret a team gets when it is taken back goes to a file the operator names, and to nothing else.

use std::net::SocketAddr;
use std::path::PathBuf;

/// The address the server listens on when the operator names none.
pub const DEFAULT_LISTEN: &str = "127.0.0.1:8743";

/// The UDP address of the feed when the operator names none.
pub const DEFAULT_FEED_CONTROL: &str = "127.0.0.1:8744";

/// The UDP address the server reads its own feed on when the operator names none.
pub const DEFAULT_FEED_SELF: &str = "127.0.0.1:8745";

/// How many bytes a recording of the feed holds before it is rolled, when the operator names no number.
pub const DEFAULT_FEED_ROLL_BYTES: u64 = 16 << 20;

/// How many records are appended between two snapshots when the operator names no number.
pub const DEFAULT_SNAPSHOT_EVERY: u64 = 2000;

/// The server's usage, printed for `--help` and after a refused command line.
pub const USAGE: &str = "\
Usage: brigade-server --data <dir> [options]
       brigade-server --data <dir> --import-team <name> --join-secret-file <file> [the log's options]
       brigade-server --data <dir> --set-owner <team_ref> --principal <principal_ref> [the log's options]
       brigade-server --data <dir> --take-back <team_ref> --secret-file <file> [the log's options]

A Brigade backend that keeps a team on the file system: docs/backend-rest-v1.md, version 1.

  --data <dir>                 the directory the server keeps everything in (required)
  --listen <ip:port>           the address to serve on (default 127.0.0.1:8743)
  --tls-cert <file>            the server's certificate chain, PEM; with --tls-key the server serves HTTPS
  --tls-key <file>             the certificate's private key, PEM
  --tls-self-signed            serve HTTPS with a certificate the server makes for itself on its first start
                               and keeps in <dir>/tls; a team file pins the fingerprint the server prints
  --tls-in-front               serve plain HTTP on an address that is not loopback, because TLS ends in
                               front of the server (a proxy or a tunnel)
  --create-token-file <file>   require this file's token, in the Brigade-Create-Token header, to create a team
  --address-file <file>        write the base URL the server listens on to this file once it is ready
  --gateway-config <file>      the Slack and mail gateways, as one JSON file that names each secret by the
                               path of the file that holds it; without it the server has no gateway and
                               answers nothing under /gateway/
  --log cavalry|file           the ordered log: a Cavalry feed (default) or one file (for tests)
  --feed-control <ip:port>     the UDP address of the Cavalry feed (default 127.0.0.1:8744)
  --feed-self <ip:port>        the UDP address the server reads its own feed on (default 127.0.0.1:8745)
  --driver-dir <dir>           Cavalry's driver directory (default: on /dev/shm where there is one)
  --snapshot-every <records>   records between two snapshots (default 2000)
  --feed-roll-bytes <bytes>    bytes a recording of the feed holds before a snapshot rolls it, so that what
                               the snapshots cover can be let go of (default 16777216)
  --import-team <name>         take in a team that lives on another backend, under this name, and end: the
                               team keeps its join secret, so its members join this server with the secret
                               file they have. Nothing is served, so it goes with --data, the log's options
                               (--log to --feed-roll-bytes) and --join-secret-file, and with no other
  --join-secret-file <file>    the file that holds the join secret of the team --import-team takes in
  --set-owner <team_ref>       name the owner of a team, the member that may rotate its join secret, revoke a
                               member and transfer it, and end: for a team that was taken in and that someone
                               else joined first, and for a team whose owner lost its credential. Nothing is
                               served, so it goes with --data, the log's options and --principal, and with no
                               other
  --principal <principal_ref>  the active member of the team that --set-owner names its owner, by the
                               principal_ref `brigade team members` shows
  --take-back <team_ref>       take a team back from whoever holds it, and end: the team gets a new join
                               secret, every membership ends, and the team has no owner until the first
                               member joins it with the new secret. For a team whose join secret leaked and
                               whose owner is not its admin. Nothing is served, so it goes with --data, the
                               log's options and --secret-file, and with no other
  --secret-file <file>         the file --take-back writes the team's new join secret to, readable by its
                               owner alone: an absolute path of a file that is not there, in a directory that
                               is
  --version                    print the version
  --help                       print this
";

/// Which ordered log the server keeps.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LogKind {
    /// One file of framed records. For tests, and all a build without Cavalry has.
    File,
    /// A recorded Cavalry publication.
    Cavalry,
}

/// The server's settings.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Options {
    /// The directory everything is kept in.
    pub data: PathBuf,
    /// The address to serve on.
    pub listen: SocketAddr,
    /// Where the certificate and its key come from, when the server serves HTTPS itself.
    pub tls: Option<Tls>,
    /// Whether TLS ends in front of the server.
    pub tls_in_front: bool,
    /// The file that holds the creation token, when creating a team needs one.
    pub create_token_file: Option<PathBuf>,
    /// Where to write the base URL once the server listens.
    pub address_file: Option<PathBuf>,
    /// The file that configures the Slack and mail gateways, when the server has any.
    pub gateway_config: Option<PathBuf>,
    /// The ordered log.
    pub log: LogKind,
    /// The UDP address of the Cavalry feed.
    pub feed_control: String,
    /// The UDP address the server reads its own feed on.
    pub feed_self: String,
    /// Cavalry's driver directory, when the operator chose one.
    pub driver_dir: Option<PathBuf>,
    /// Records between two snapshots.
    pub snapshot_every: u64,
    /// How many bytes a recording of the feed holds before it is rolled.
    pub feed_roll_bytes: u64,
}

/// Where a server that serves HTTPS itself has its certificate from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Tls {
    /// The operator's own files: the certificate chain and its key.
    Files(PathBuf, PathBuf),
    /// A certificate the server makes for itself and keeps in its data directory.
    SelfSigned,
}

/// What the command that takes a team in is told.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Import {
    /// The name the team gets here.
    pub team_name: String,
    /// The file that holds the team's join secret.
    pub join_secret_file: PathBuf,
    /// The directory and the log, read as a start reads them. The command serves nothing, so nothing else
    /// of these is the operator's: the command line refuses every option that serving takes.
    pub options: Options,
}

/// What the command that names a team's owner is told.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SetOwner {
    /// The team.
    pub team_ref: String,
    /// The member that is to own it.
    pub principal_ref: String,
    /// The directory and the log, read as a start reads them. Like the import, the command serves nothing,
    /// and the command line refuses every option that serving takes.
    pub options: Options,
}

/// What the command that takes a team back is told.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TakeBack {
    /// The team.
    pub team_ref: String,
    /// The file the team's new join secret is written to.
    pub secret_file: PathBuf,
    /// The directory and the log, read as a start reads them. Like the import, the command serves nothing,
    /// and the command line refuses every option that serving takes.
    pub options: Options,
}

/// What a command line asks for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Asked {
    /// To run the server.
    Run(Box<Options>),
    /// To take in a team that exists elsewhere, and end.
    Import(Box<Import>),
    /// To name the owner of a team, and end.
    SetOwner(Box<SetOwner>),
    /// To take a team back from whoever holds it, and end.
    TakeBack(Box<TakeBack>),
    /// To print the usage.
    Help,
    /// To print the version.
    Version,
}

/// Reads a command line, without the program's name.
///
/// # Errors
/// A sentence that says what is wrong with the command line. It never repeats a value.
pub fn parse(arguments: impl IntoIterator<Item = String>) -> Result<Asked, String> {
    let mut arguments = arguments.into_iter();
    let (mut data, mut listen, mut tls_cert, mut tls_key) = (None, None, None, None);
    let (mut create_token_file, mut address_file, mut driver_dir) = (None, None, None);
    let mut gateway_config = None;
    let (mut feed_control, mut feed_self, mut log, mut snapshot_every) = (None, None, None, None);
    let mut feed_roll_bytes = None;
    let (mut import_team, mut join_secret_file) = (None, None);
    let (mut set_owner, mut principal) = (None, None);
    let (mut take_back, mut secret_file) = (None, None);
    let mut tls_in_front = false;
    let mut tls_self_signed = false;
    while let Some(flag) = arguments.next() {
        let mut value = |name: &str| arguments.next().ok_or_else(|| format!("{name} needs a value"));
        match flag.as_str() {
            "--help" | "-h" => return Ok(Asked::Help),
            "--version" => return Ok(Asked::Version),
            "--tls-in-front" => tls_in_front = true,
            "--tls-self-signed" => tls_self_signed = true,
            "--data" => data = Some(PathBuf::from(value("--data")?)),
            "--listen" => listen = Some(value("--listen")?),
            "--tls-cert" => tls_cert = Some(PathBuf::from(value("--tls-cert")?)),
            "--tls-key" => tls_key = Some(PathBuf::from(value("--tls-key")?)),
            "--create-token-file" => create_token_file = Some(PathBuf::from(value("--create-token-file")?)),
            "--address-file" => address_file = Some(PathBuf::from(value("--address-file")?)),
            "--gateway-config" => gateway_config = Some(PathBuf::from(value("--gateway-config")?)),
            "--driver-dir" => driver_dir = Some(PathBuf::from(value("--driver-dir")?)),
            "--feed-control" => feed_control = Some(value("--feed-control")?),
            "--feed-self" => feed_self = Some(value("--feed-self")?),
            "--log" => log = Some(value("--log")?),
            "--snapshot-every" => snapshot_every = Some(value("--snapshot-every")?),
            "--feed-roll-bytes" => feed_roll_bytes = Some(value("--feed-roll-bytes")?),
            "--import-team" => import_team = Some(value("--import-team")?),
            "--join-secret-file" => join_secret_file = Some(PathBuf::from(value("--join-secret-file")?)),
            "--set-owner" => set_owner = Some(value("--set-owner")?),
            "--principal" => principal = Some(value("--principal")?),
            "--take-back" => take_back = Some(value("--take-back")?),
            "--secret-file" => secret_file = Some(PathBuf::from(value("--secret-file")?)),
            _ => return Err("an argument is not one the server knows".to_owned()),
        }
    }
    let data = data.ok_or("--data <dir> is required")?;
    let import = match (import_team, join_secret_file) {
        (Some(team_name), Some(join_secret_file)) => Some((team_name, join_secret_file)),
        (None, None) => None,
        _ => return Err("--import-team and --join-secret-file go together".to_owned()),
    };
    let owner = match (set_owner, principal) {
        (Some(team_ref), Some(principal_ref)) => Some((team_ref, principal_ref)),
        (None, None) => None,
        _ => return Err("--set-owner and --principal go together".to_owned()),
    };
    let taken = match (take_back, secret_file) {
        (Some(team_ref), Some(secret_file)) => Some((team_ref, secret_file)),
        (None, None) => None,
        _ => return Err("--take-back and --secret-file go together".to_owned()),
    };
    // Each of the three commands ends when it is done, and one run of the program is one of them.
    let given: Vec<(&str, &str)> = [
        ("--import-team", "takes a team in", import.is_some()),
        ("--set-owner", "names a team's owner", owner.is_some()),
        ("--take-back", "takes a team back", taken.is_some()),
    ]
    .into_iter()
    .filter(|(_, _, given)| *given)
    .map(|(option, does, _)| (option, does))
    .collect();
    if let [(first, _), (second, _), ..] = given[..] {
        return Err(format!(
            "{first} and {second} are two commands, and each ends when it is done: give one"
        ));
    }
    if let Some((option, does)) = given.first() {
        let command = format!("{option} {does}");
        // A command that ends binds no address and starts no gateway, so an option that serving takes
        // would be read and never used. It is refused by its name, which is the server's own word.
        let serving = [
            ("--listen", listen.is_some()),
            ("--tls-cert", tls_cert.is_some()),
            ("--tls-key", tls_key.is_some()),
            ("--tls-self-signed", tls_self_signed),
            ("--tls-in-front", tls_in_front),
            ("--create-token-file", create_token_file.is_some()),
            ("--address-file", address_file.is_some()),
            ("--gateway-config", gateway_config.is_some()),
        ];
        if let Some((option, _)) = serving.iter().find(|(_, given)| *given) {
            return Err(format!(
                "{command} and ends, and serves nothing: it does not go with {option}"
            ));
        }
    }
    let listen: SocketAddr = listen
        .as_deref()
        .unwrap_or(DEFAULT_LISTEN)
        .parse()
        .map_err(|_| "--listen takes an IP address and a port, as 127.0.0.1:8743")?;
    let tls = match (tls_cert, tls_key, tls_self_signed) {
        (Some(cert), Some(key), false) => Some(Tls::Files(cert, key)),
        (None, None, true) => Some(Tls::SelfSigned),
        (None, None, false) => None,
        (_, _, true) => {
            return Err(
                "--tls-self-signed makes the server's own certificate; it does not go with --tls-cert"
                    .to_owned(),
            );
        }
        _ => return Err("--tls-cert and --tls-key go together".to_owned()),
    };
    if tls.is_some() && tls_in_front {
        return Err(
            "--tls-in-front is for a server that serves plain HTTP; it does not go with a certificate"
                .to_owned(),
        );
    }
    // Plain HTTP never reaches a public interface by accident: off loopback the operator either gives the
    // server a certificate, has it make one, or says that TLS ends in front of it (owner ruling, 2026-11-12).
    if tls.is_none() && !tls_in_front && !listen.ip().is_loopback() {
        return Err(
            "plain HTTP is served on a loopback address only: give --tls-self-signed, or --tls-cert and \
                    --tls-key, or --tls-in-front when TLS ends in front of the server"
                .to_owned(),
        );
    }
    let log = match log.as_deref() {
        None if cfg!(feature = "cavalry") => LogKind::Cavalry,
        None | Some("file") => LogKind::File,
        Some("cavalry") if cfg!(feature = "cavalry") => LogKind::Cavalry,
        Some("cavalry") => return Err("this build has no Cavalry feed; its log is --log file".to_owned()),
        Some(_) => return Err("--log takes cavalry or file".to_owned()),
    };
    let snapshot_every = match snapshot_every {
        None => DEFAULT_SNAPSHOT_EVERY,
        Some(text) => text
            .parse::<u64>()
            .ok()
            .filter(|every| *every > 0)
            .ok_or("--snapshot-every takes a number of records above zero")?,
    };
    let feed_roll_bytes = match feed_roll_bytes {
        None => DEFAULT_FEED_ROLL_BYTES,
        Some(text) => text
            .parse::<u64>()
            .ok()
            .filter(|bytes| *bytes > 0)
            .ok_or("--feed-roll-bytes takes a number of bytes above zero")?,
    };
    let options = Options {
        data,
        listen,
        tls,
        tls_in_front,
        create_token_file,
        address_file,
        gateway_config,
        log,
        feed_control: feed_control.unwrap_or_else(|| DEFAULT_FEED_CONTROL.to_owned()),
        feed_self: feed_self.unwrap_or_else(|| DEFAULT_FEED_SELF.to_owned()),
        driver_dir,
        snapshot_every,
        feed_roll_bytes,
    };
    Ok(match (import, owner, taken) {
        (Some((team_name, join_secret_file)), _, _) => Asked::Import(Box::new(Import {
            team_name,
            join_secret_file,
            options,
        })),
        (None, Some((team_ref, principal_ref)), _) => Asked::SetOwner(Box::new(SetOwner {
            team_ref,
            principal_ref,
            options,
        })),
        (None, None, Some((team_ref, secret_file))) => Asked::TakeBack(Box::new(TakeBack {
            team_ref,
            secret_file,
            options,
        })),
        (None, None, None) => Asked::Run(Box::new(options)),
    })
}

#[cfg(test)]
mod tests {
    use super::{Asked, Import, LogKind, Options, SetOwner, TakeBack, Tls, parse};

    fn run(arguments: &[&str]) -> Result<Options, String> {
        match parse(arguments.iter().map(|argument| (*argument).to_owned()))? {
            Asked::Run(options) => Ok(*options),
            other => Err(format!("{other:?}")),
        }
    }

    fn import(arguments: &[&str]) -> Result<Import, String> {
        match parse(arguments.iter().map(|argument| (*argument).to_owned()))? {
            Asked::Import(import) => Ok(*import),
            other => Err(format!("{other:?}")),
        }
    }

    #[test]
    fn a_directory_is_all_it_needs() {
        let options = run(&["--data", "/srv/brigade"]).unwrap();
        assert_eq!(options.listen.to_string(), "127.0.0.1:8743");
        assert_eq!(options.tls, None);
        assert_eq!(options.snapshot_every, 2000);
        assert_eq!(
            options.log,
            if cfg!(feature = "cavalry") {
                LogKind::Cavalry
            } else {
                LogKind::File
            }
        );
        assert!(run(&[]).unwrap_err().contains("--data"));
    }

    #[test]
    fn plain_http_leaves_loopback_only_when_the_operator_says_tls_is_in_front() {
        assert!(run(&["--data", "d", "--listen", "[::1]:9000"]).is_ok());
        let refused = run(&["--data", "d", "--listen", "0.0.0.0:9000"]).unwrap_err();
        assert!(refused.contains("loopback"), "{refused}");
        assert!(run(&["--data", "d", "--listen", "192.0.2.7:9000"]).is_err());
        let in_front = run(&["--data", "d", "--listen", "0.0.0.0:9000", "--tls-in-front"]).unwrap();
        assert!(in_front.tls_in_front);
        let own = run(&[
            "--data",
            "d",
            "--listen",
            "0.0.0.0:9000",
            "--tls-cert",
            "c.pem",
            "--tls-key",
            "k.pem",
        ]);
        assert!(own.unwrap().tls.is_some());
    }

    #[test]
    fn the_certificate_and_its_key_go_together() {
        assert!(run(&["--data", "d", "--tls-cert", "c.pem"]).is_err());
        assert!(run(&["--data", "d", "--tls-key", "k.pem"]).is_err());
        assert!(
            run(&[
                "--data",
                "d",
                "--tls-cert",
                "c.pem",
                "--tls-key",
                "k.pem",
                "--tls-in-front"
            ])
            .is_err()
        );
    }

    #[test]
    fn the_server_makes_its_own_certificate_when_it_is_asked_to() {
        let own = run(&["--data", "d", "--listen", "0.0.0.0:9000", "--tls-self-signed"]).unwrap();
        assert_eq!(own.tls, Some(Tls::SelfSigned));
        // It is one way to have a certificate, not a second one beside the operator's files or a proxy.
        for others in [
            &["--tls-cert", "c.pem", "--tls-key", "k.pem"][..],
            &["--tls-cert", "c.pem"][..],
            &["--tls-in-front"][..],
        ] {
            let mut arguments = vec!["--data", "d", "--tls-self-signed"];
            arguments.extend_from_slice(others);
            assert!(run(&arguments).is_err(), "{others:?}");
        }
    }

    #[test]
    fn the_gateways_are_off_until_a_file_configures_them() {
        assert_eq!(run(&["--data", "d"]).unwrap().gateway_config, None);
        let configured = run(&["--data", "d", "--gateway-config", "/etc/brigade/gateways.json"]).unwrap();
        assert_eq!(
            configured.gateway_config.as_deref(),
            Some(std::path::Path::new("/etc/brigade/gateways.json"))
        );
        assert!(
            run(&["--data", "d", "--gateway-config"])
                .unwrap_err()
                .contains("--gateway-config needs a value")
        );
        assert!(super::USAGE.contains("--gateway-config <file>"));
    }

    #[test]
    fn a_team_is_taken_in_by_its_name_and_the_file_of_its_join_secret() {
        let taken = import(&[
            "--data",
            "/srv/brigade",
            "--import-team",
            "ops",
            "--join-secret-file",
            "/run/secrets/ops.join-secret",
        ])
        .unwrap();
        assert_eq!(taken.team_name, "ops");
        assert_eq!(
            taken.join_secret_file,
            std::path::Path::new("/run/secrets/ops.join-secret")
        );
        // The directory and the log are read as a start reads them.
        let started = run(&["--data", "/srv/brigade"]).unwrap();
        assert_eq!(taken.options, started);
        // The two go together, whichever is missing, and the directory is as required as it always is.
        for half in [
            &["--import-team", "ops"][..],
            &["--join-secret-file", "ops.join-secret"][..],
        ] {
            let mut arguments = vec!["--data", "d"];
            arguments.extend_from_slice(half);
            let refused = parse(arguments.iter().map(|argument| (*argument).to_owned())).unwrap_err();
            assert_eq!(refused, "--import-team and --join-secret-file go together");
        }
        let no_directory = import(&["--import-team", "ops", "--join-secret-file", "f"]).unwrap_err();
        assert!(no_directory.contains("--data"), "{no_directory}");
        assert!(import(&["--data", "d", "--import-team"]).is_err());
        assert!(super::USAGE.contains("--import-team <name>"));
        assert!(super::USAGE.contains("--join-secret-file <file>"));
    }

    #[test]
    fn taking_a_team_in_serves_nothing_so_it_takes_the_logs_options_and_no_others() {
        let pair = ["--data", "d", "--import-team", "ops", "--join-secret-file", "f"];
        let with = |others: &[&'static str]| {
            let mut arguments = pair.to_vec();
            arguments.extend_from_slice(others);
            import(&arguments)
        };
        let on_a_file = with(&[
            "--log",
            "file",
            "--feed-control",
            "127.0.0.1:9001",
            "--feed-self",
            "127.0.0.1:9002",
            "--driver-dir",
            "/dev/shm/brigade",
            "--snapshot-every",
            "7",
            "--feed-roll-bytes",
            "4096",
        ])
        .unwrap();
        assert_eq!(on_a_file.options.log, LogKind::File);
        assert_eq!(on_a_file.options.feed_control, "127.0.0.1:9001");
        assert_eq!(on_a_file.options.feed_self, "127.0.0.1:9002");
        assert_eq!(
            on_a_file.options.driver_dir.as_deref(),
            Some(std::path::Path::new("/dev/shm/brigade"))
        );
        assert_eq!(
            (
                on_a_file.options.snapshot_every,
                on_a_file.options.feed_roll_bytes
            ),
            (7, 4096)
        );
        // Everything that serving takes is refused by its own name, and its value is not repeated.
        for (others, named) in [
            (&["--listen", "127.0.0.1:9000"][..], "--listen"),
            (&["--tls-cert", "c.pem", "--tls-key", "k.pem"][..], "--tls-cert"),
            (&["--tls-key", "k.pem"][..], "--tls-key"),
            (&["--tls-self-signed"][..], "--tls-self-signed"),
            (&["--tls-in-front"][..], "--tls-in-front"),
            (&["--create-token-file", "token"][..], "--create-token-file"),
            (&["--address-file", "address"][..], "--address-file"),
            (&["--gateway-config", "gateways.json"][..], "--gateway-config"),
        ] {
            let refused = with(others).unwrap_err();
            assert_eq!(
                refused,
                format!(
                    "--import-team takes a team in and ends, and serves nothing: it does not go with {named}"
                )
            );
        }
        // Whichever comes first on the command line.
        let first = import(&[
            "--address-file",
            "address",
            "--join-secret-file",
            "f",
            "--import-team",
            "ops",
            "--data",
            "d",
        ]);
        assert!(first.unwrap_err().contains("--address-file"));
    }

    fn set_owner(arguments: &[&str]) -> Result<SetOwner, String> {
        match parse(arguments.iter().map(|argument| (*argument).to_owned()))? {
            Asked::SetOwner(named) => Ok(*named),
            other => Err(format!("{other:?}")),
        }
    }

    #[test]
    fn a_teams_owner_is_named_by_the_teams_reference_and_the_members_principal() {
        let pair = [
            "--data",
            "/srv/brigade",
            "--set-owner",
            "the-team",
            "--principal",
            "the-member",
        ];
        let named = set_owner(&pair).unwrap();
        assert_eq!(
            (named.team_ref.as_str(), named.principal_ref.as_str()),
            ("the-team", "the-member")
        );
        // The directory and the log are read as a start reads them, and the log's options go with it.
        assert_eq!(named.options, run(&["--data", "/srv/brigade"]).unwrap());
        let mut on_a_file = pair.to_vec();
        on_a_file.extend_from_slice(&["--log", "file", "--snapshot-every", "7"]);
        let named = set_owner(&on_a_file).unwrap();
        assert_eq!(
            (named.options.log, named.options.snapshot_every),
            (LogKind::File, 7)
        );
        // The two go together, whichever is missing, and the directory is as required as it always is.
        for half in [
            &["--set-owner", "the-team"][..],
            &["--principal", "the-member"][..],
        ] {
            let mut arguments = vec!["--data", "d"];
            arguments.extend_from_slice(half);
            assert_eq!(
                set_owner(&arguments).unwrap_err(),
                "--set-owner and --principal go together"
            );
        }
        let no_directory = set_owner(&pair[2..]).unwrap_err();
        assert!(no_directory.contains("--data"), "{no_directory}");
        assert!(set_owner(&["--data", "d", "--set-owner"]).is_err());
        assert!(super::USAGE.contains("--set-owner <team_ref>"));
        assert!(super::USAGE.contains("--principal <principal_ref>"));
    }

    #[test]
    fn naming_an_owner_serves_nothing_and_is_one_command_of_its_own() {
        let pair = [
            "--data",
            "d",
            "--set-owner",
            "the-team",
            "--principal",
            "the-member",
        ];
        let with = |others: &[&'static str]| {
            let mut arguments = pair.to_vec();
            arguments.extend_from_slice(others);
            set_owner(&arguments)
        };
        // Everything that serving takes is refused by its own name, and no value is repeated.
        for (others, named) in [
            (&["--listen", "127.0.0.1:9000"][..], "--listen"),
            (&["--tls-cert", "c.pem", "--tls-key", "k.pem"][..], "--tls-cert"),
            (&["--tls-self-signed"][..], "--tls-self-signed"),
            (&["--tls-in-front"][..], "--tls-in-front"),
            (&["--create-token-file", "token"][..], "--create-token-file"),
            (&["--address-file", "address"][..], "--address-file"),
            (&["--gateway-config", "gateways.json"][..], "--gateway-config"),
        ] {
            assert_eq!(
                with(others).unwrap_err(),
                format!(
                    "--set-owner names a team's owner and ends, and serves nothing: it does not go with {named}"
                )
            );
        }
        // It is not the command that takes a team in, in whichever order the two are given.
        let both = with(&["--import-team", "ops", "--join-secret-file", "f"]).unwrap_err();
        assert!(both.contains("two commands"), "{both}");
        assert!(
            !both.contains("the-team") && !both.contains("the-member"),
            "{both}"
        );
        let both = set_owner(&[
            "--import-team",
            "ops",
            "--principal",
            "the-member",
            "--join-secret-file",
            "f",
            "--set-owner",
            "the-team",
            "--data",
            "d",
        ]);
        assert!(both.unwrap_err().contains("two commands"));
        // Half of the other command beside it is told as that command's half.
        assert_eq!(
            with(&["--import-team", "ops"]).unwrap_err(),
            "--import-team and --join-secret-file go together"
        );
    }

    fn take_back(arguments: &[&str]) -> Result<TakeBack, String> {
        match parse(arguments.iter().map(|argument| (*argument).to_owned()))? {
            Asked::TakeBack(taken) => Ok(*taken),
            other => Err(format!("{other:?}")),
        }
    }

    #[test]
    fn a_team_is_taken_back_by_its_reference_and_the_file_its_new_secret_goes_to() {
        let pair = [
            "--data",
            "/srv/brigade",
            "--take-back",
            "the-team",
            "--secret-file",
            "/run/secrets/ops.join-secret",
        ];
        let taken = take_back(&pair).unwrap();
        assert_eq!(taken.team_ref, "the-team");
        assert_eq!(
            taken.secret_file,
            std::path::Path::new("/run/secrets/ops.join-secret")
        );
        // The directory and the log are read as a start reads them, and the log's options go with it.
        assert_eq!(taken.options, run(&["--data", "/srv/brigade"]).unwrap());
        let mut on_a_file = pair.to_vec();
        on_a_file.extend_from_slice(&["--log", "file", "--snapshot-every", "7"]);
        let taken = take_back(&on_a_file).unwrap();
        assert_eq!(
            (taken.options.log, taken.options.snapshot_every),
            (LogKind::File, 7)
        );
        // The two go together, whichever is missing, and the directory is as required as it always is.
        for half in [&["--take-back", "the-team"][..], &["--secret-file", "/f"][..]] {
            let mut arguments = vec!["--data", "d"];
            arguments.extend_from_slice(half);
            assert_eq!(
                take_back(&arguments).unwrap_err(),
                "--take-back and --secret-file go together"
            );
        }
        let no_directory = take_back(&pair[2..]).unwrap_err();
        assert!(no_directory.contains("--data"), "{no_directory}");
        assert!(take_back(&["--data", "d", "--take-back"]).is_err());
        assert!(super::USAGE.contains("--take-back <team_ref>"));
        assert!(super::USAGE.contains("--secret-file <file>"));
    }

    #[test]
    fn taking_a_team_back_serves_nothing_and_is_one_command_of_its_own() {
        let pair = ["--data", "d", "--take-back", "the-team", "--secret-file", "/f"];
        let with = |others: &[&'static str]| {
            let mut arguments = pair.to_vec();
            arguments.extend_from_slice(others);
            take_back(&arguments)
        };
        // Everything that serving takes is refused by its own name, and no value is repeated.
        for (others, named) in [
            (&["--listen", "127.0.0.1:9000"][..], "--listen"),
            (&["--tls-cert", "c.pem", "--tls-key", "k.pem"][..], "--tls-cert"),
            (&["--tls-self-signed"][..], "--tls-self-signed"),
            (&["--tls-in-front"][..], "--tls-in-front"),
            (&["--create-token-file", "token"][..], "--create-token-file"),
            (&["--address-file", "address"][..], "--address-file"),
            (&["--gateway-config", "gateways.json"][..], "--gateway-config"),
        ] {
            assert_eq!(
                with(others).unwrap_err(),
                format!(
                    "--take-back takes a team back and ends, and serves nothing: it does not go with {named}"
                )
            );
        }
        // It is neither of the other two commands, and the refusal names the two that were given.
        assert_eq!(
            with(&["--import-team", "ops", "--join-secret-file", "f"]).unwrap_err(),
            "--import-team and --take-back are two commands, and each ends when it is done: give one"
        );
        assert_eq!(
            with(&["--set-owner", "the-team", "--principal", "the-member"]).unwrap_err(),
            "--set-owner and --take-back are two commands, and each ends when it is done: give one"
        );
        // The file of the one command is not the file of the other.
        assert_eq!(
            take_back(&[
                "--data",
                "d",
                "--take-back",
                "the-team",
                "--join-secret-file",
                "/f"
            ])
            .unwrap_err(),
            "--import-team and --join-secret-file go together"
        );
    }

    #[test]
    fn a_refusal_never_repeats_what_was_typed() {
        let refused = run(&["--data", "d", "--join-secret", "brg1.team.s3cret"]).unwrap_err();
        assert!(!refused.contains("s3cret"));
        assert!(!refused.contains("join-secret"));
        assert!(run(&["--data"]).is_err());
        assert!(run(&["--data", "d", "--listen", "localhost"]).is_err());
        assert!(run(&["--data", "d", "--snapshot-every", "0"]).is_err());
        assert!(run(&["--data", "d", "--log", "postgres"]).is_err());
    }

    #[test]
    fn the_version_is_the_one_in_the_version_file() {
        // The server has its own version, in server/VERSION; the workspace's Cargo.toml is held to it.
        assert_eq!(include_str!("../../../VERSION").trim(), env!("CARGO_PKG_VERSION"));
    }

    #[test]
    fn help_and_version_are_asked_for_by_name() {
        assert_eq!(parse(["--help".to_owned()]).unwrap(), Asked::Help);
        assert_eq!(
            parse(["--data".to_owned(), "d".to_owned(), "--version".to_owned()]).unwrap(),
            Asked::Version
        );
    }
}
