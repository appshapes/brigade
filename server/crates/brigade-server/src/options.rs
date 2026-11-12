//! What the operator tells the server on its command line.
//!
//! Nothing secret is ever an argument: the creation token and the TLS key are files, named by path, and so is
//! every secret of the gateways, by the configuration that is itself a file.

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

/// What a command line asks for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Asked {
    /// To run the server.
    Run(Box<Options>),
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
            _ => return Err("an argument is not one the server knows".to_owned()),
        }
    }
    let data = data.ok_or("--data <dir> is required")?;
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
    Ok(Asked::Run(Box::new(Options {
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
    })))
}

#[cfg(test)]
mod tests {
    use super::{Asked, LogKind, Options, Tls, parse};

    fn run(arguments: &[&str]) -> Result<Options, String> {
        match parse(arguments.iter().map(|argument| (*argument).to_owned()))? {
            Asked::Run(options) => Ok(*options),
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
