//! What the tests of the running server share: a directory of the test's own, the server as a process, and
//! one request as the bytes of HTTP/1.1.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use serde_json::Value;

/// A directory of the test's own, removed when the test ends.
pub struct Scratch(PathBuf);

impl Scratch {
    pub fn new(test: &str) -> Self {
        let dir = std::env::temp_dir().join(format!("brigade-server-{test}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Self(dir)
    }

    pub fn path(&self, name: &str) -> PathBuf {
        self.0.join(name)
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// A UDP port nothing is bound to now, and that no other test of this run was given: the tests run side by
/// side, and the system hands a port out again as soon as it is free.
#[cfg(feature = "cavalry")]
fn free_udp() -> String {
    static GIVEN: std::sync::Mutex<std::collections::BTreeSet<u16>> =
        std::sync::Mutex::new(std::collections::BTreeSet::new());
    loop {
        let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let address = socket.local_addr().unwrap();
        if GIVEN.lock().unwrap().insert(address.port()) {
            return address.to_string();
        }
    }
}

/// The same arguments with the feed on two new UDP ports.
fn on_new_ports(arguments: &[String]) -> Vec<String> {
    #[cfg(feature = "cavalry")]
    {
        let mut moved = arguments.to_vec();
        for index in 1..moved.len() {
            if moved[index - 1] == "--feed-control" || moved[index - 1] == "--feed-self" {
                moved[index] = free_udp();
            }
        }
        moved
    }
    #[cfg(not(feature = "cavalry"))]
    arguments.to_vec()
}

/// The arguments that choose the log a test runs on.
pub fn log_arguments(log: &str, scratch: &Scratch) -> Vec<String> {
    let arguments = vec!["--log".to_owned(), log.to_owned()];
    #[cfg(feature = "cavalry")]
    let arguments = if log == "cavalry" {
        let feed = [
            "--feed-control".to_owned(),
            free_udp(),
            "--feed-self".to_owned(),
            free_udp(),
        ];
        let driver = [
            "--driver-dir".to_owned(),
            scratch.path("driver").display().to_string(),
        ];
        arguments.into_iter().chain(feed).chain(driver).collect()
    } else {
        arguments
    };
    let _ = scratch;
    arguments
}

/// One running server.
pub struct Server {
    /// The server's process.
    pub child: Child,
    /// Where it listens.
    pub url: String,
}

impl Server {
    /// Starts a server on `scratch`'s data directory and waits until it listens.
    ///
    /// A port that was free when the test chose it can be taken by the time the server binds it, by
    /// anything on the machine. Where the feed's ports are is nothing a test is about, so such a start is
    /// made again on new ones.
    pub fn start(scratch: &Scratch, arguments: &[String]) -> Self {
        let mut arguments = arguments.to_vec();
        for _ in 0..3 {
            match Self::try_start(scratch, &arguments) {
                Ok(server) => return server,
                Err(log) if log.contains("Address already in use") => arguments = on_new_ports(&arguments),
                Err(log) => panic!("the server did not start:\n{log}"),
            }
        }
        panic!("the server found its feed's ports taken three times in a row");
    }

    /// Starts a server; when it ends before it listens, the error is what it wrote to stderr.
    pub fn try_start(scratch: &Scratch, arguments: &[String]) -> Result<Self, String> {
        let address = scratch.path("address");
        let _ = std::fs::remove_file(&address);
        let stderr = std::fs::File::create(scratch.path("stderr")).unwrap();
        let mut child = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
            .arg("--data")
            .arg(scratch.path("data"))
            .args(["--listen", "127.0.0.1:0", "--address-file"])
            .arg(&address)
            .args(arguments)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(stderr)
            .spawn()
            .unwrap();
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            if let Ok(url) = std::fs::read_to_string(&address)
                && url.ends_with('\n')
            {
                return Ok(Self {
                    child,
                    url: url.trim().to_owned(),
                });
            }
            if child.try_wait().unwrap().is_some() || Instant::now() > deadline {
                let _ = child.kill();
                let _ = child.wait();
                return Err(std::fs::read_to_string(scratch.path("stderr")).unwrap_or_default());
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    /// Ends the server as a power cut would, as far as a process can: no signal it can handle.
    pub fn kill(mut self) {
        self.child.kill().unwrap();
        self.child.wait().unwrap();
    }

    /// Asks the server to stop and waits until it has.
    pub fn stop(mut self) {
        let status = Command::new("kill")
            .arg("-TERM")
            .arg(self.child.id().to_string())
            .status()
            .unwrap();
        assert!(status.success());
        let ended = self.child.wait().unwrap();
        assert!(ended.success(), "the server ended with {ended}");
    }

    pub fn post(&self, credential: Option<&str>, path: &str, body: &Value) -> (u16, Value) {
        let authority = self.url.strip_prefix("http://").expect("a plain HTTP server");
        let mut stream = TcpStream::connect(authority).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(60))).unwrap();
        stream
            .write_all(&request(authority, credential, path, &body.to_string(), &[]))
            .unwrap();
        let mut response = Vec::new();
        stream.read_to_end(&mut response).unwrap();
        parse(&response)
    }

    /// Asks and expects a result.
    pub fn ok(&self, credential: &str, path: &str, body: &Value) -> Value {
        let (status, envelope) = self.post(Some(credential), path, body);
        assert_eq!(status, 200, "{path}: {envelope}");
        envelope["result"].clone()
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// One `POST` as the bytes of HTTP/1.1.
pub fn request(
    authority: &str,
    credential: Option<&str>,
    path: &str,
    body: &str,
    headers: &[(&str, &str)],
) -> Vec<u8> {
    let mut head = format!(
        "POST {path} HTTP/1.1\r\nHost: {authority}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n",
        body.len()
    );
    if let Some(credential) = credential {
        head.push_str(&format!("Authorization: Bearer {credential}\r\n"));
    }
    for (name, value) in headers {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let mut bytes = head.into_bytes();
    bytes.extend_from_slice(body.as_bytes());
    bytes
}

/// The status and the envelope of a response.
pub fn parse(response: &[u8]) -> (u16, Value) {
    let text = String::from_utf8_lossy(response);
    let (head, body) = text
        .split_once("\r\n\r\n")
        .unwrap_or_else(|| panic!("not a response: {text}"));
    let status = head
        .split_whitespace()
        .nth(1)
        .and_then(|status| status.parse().ok())
        .expect("a status line");
    assert!(
        head.to_ascii_lowercase()
            .contains("content-type: application/json"),
        "{head}"
    );
    (
        status,
        serde_json::from_str(body).unwrap_or_else(|_| panic!("not an envelope: {body}")),
    )
}

pub fn text(value: &Value, member: &str) -> String {
    value[member]
        .as_str()
        .unwrap_or_else(|| panic!("no {member} in {value}"))
        .to_owned()
}
