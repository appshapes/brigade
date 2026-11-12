//! A stand-in for Slack or for a mail provider: a server on loopback that keeps every request it is asked
//! and answers what the test tells it to.
//!
//! The gateways speak plain HTTP to this machine and to nothing else, which is what lets a test put one of
//! these where the configuration's `api` points. The tests beside the code and the tests of the running
//! server share this one file.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{Value, json};

/// One request a stand-in was asked.
#[derive(Clone, Debug)]
pub struct Asked {
    method: String,
    target: String,
    headers: Vec<(String, String)>,
    body: String,
}

impl Asked {
    /// The method and the path, as `POST /api/chat.postMessage`.
    pub fn named(&self) -> String {
        let path = self.target.split('?').next().unwrap_or_default();
        format!("{} {path}", self.method)
    }

    /// The request's target: its path and its query.
    pub fn target(&self) -> &str {
        &self.target
    }

    /// The value of a header, its name in any case.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(header, _)| header.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }

    /// The body, as JSON; `null` when it is not JSON.
    pub fn json(&self) -> Value {
        serde_json::from_str(&self.body).unwrap_or(Value::Null)
    }
}

/// What a stand-in answers.
pub struct Said {
    status: u16,
    headers: Vec<(&'static str, String)>,
    body: String,
}

impl Said {
    /// An answer whose body is JSON.
    pub fn json(status: u16, body: &Value) -> Self {
        Self {
            status,
            headers: Vec::new(),
            body: body.to_string(),
        }
    }

    /// The same answer with one more header.
    pub fn with(mut self, name: &'static str, value: &str) -> Self {
        self.headers.push((name, value.to_owned()));
        self
    }
}

/// A running stand-in.
pub struct StandIn {
    /// Where it listens: `http://127.0.0.1:<port>`.
    pub url: String,
    asked: Arc<Mutex<Vec<Asked>>>,
}

impl StandIn {
    /// Starts a stand-in that answers every request with what `answer` says for it.
    pub fn start(answer: impl Fn(&Asked) -> Said + Send + Sync + 'static) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let asked = Arc::new(Mutex::new(Vec::new()));
        let (kept, answer) = (Arc::clone(&asked), Arc::new(answer));
        // The thread ends with the test's process: a stand-in is asked until then.
        std::thread::spawn(move || {
            for stream in listener.incoming().flatten() {
                let (kept, answer) = (Arc::clone(&kept), Arc::clone(&answer));
                std::thread::spawn(move || {
                    let Some(request) = read(&stream) else { return };
                    // Kept before it is answered: a test may look for a request that is still waiting.
                    kept.lock().unwrap().push(request.clone());
                    write(&stream, &(*answer)(&request));
                });
            }
        });
        Self { url, asked }
    }

    /// Every request asked so far, in the order they came.
    pub fn asked(&self) -> Vec<Asked> {
        self.asked.lock().unwrap().clone()
    }
}

/// What Slack answers when all is well, in a workspace with one person, `U0PERSON01` (`pat`, shown as
/// `Pat`), one channel, `C0GENERAL1` (`general`), and the bot `U0BOT00001`, whose token hears a channel's
/// threads. A post is answered with the channel it named and the timestamp `1700000000.000100`.
pub fn slack(asked: &Asked) -> Said {
    let ok = |body: Value| Said::json(200, &body);
    match asked.named().as_str() {
        "POST /api/auth.test" => ok(json!({
            "ok": true, "user_id": "U0BOT00001", "bot_id": "B0BOT00001", "team": "Example",
            "url": "https://example.slack.com/",
        }))
        .with("X-OAuth-Scopes", "chat:write,channels:history,users:read"),
        "GET /api/users.list" => ok(json!({"ok": true, "members": [
            {"id": "U0BOT00001", "name": "brigade", "is_bot": true},
            {"id": "U0PERSON01", "name": "pat",
             "profile": {"display_name": "Pat", "real_name": "Pat Example"}},
        ]})),
        "GET /api/users.info" | "GET /api/users.lookupByEmail" => ok(json!({"ok": true, "user": {
            "id": "U0PERSON01", "name": "pat", "profile": {"display_name": "Pat", "email": "pat@example.com"},
        }})),
        "GET /api/conversations.list" => ok(json!({"ok": true, "channels": [
            {"id": "C0GENERAL1", "name": "general"},
        ]})),
        "GET /api/conversations.info" => {
            ok(json!({"ok": true, "channel": {"id": "C0GENERAL1", "name": "general"}}))
        }
        "POST /api/conversations.open" => ok(json!({"ok": true, "channel": {"id": "D0PERSON01"}})),
        "POST /api/chat.postMessage" => ok(json!({
            "ok": true, "ts": "1700000000.000100", "channel": asked.json()["channel"],
        })),
        "GET /api/chat.getPermalink" => ok(json!({
            "ok": true, "permalink": "https://example.slack.com/archives/C0GENERAL1/p1700000001000200",
        })),
        "POST /api/chat.postEphemeral" | "POST /api/reactions.add" | "POST /api/conversations.join" => {
            ok(json!({"ok": true}))
        }
        _ => ok(json!({"ok": false, "error": "unknown_method"})),
    }
}

/// How many mails the stand-in providers have been asked to send, for the ids they answer.
static SENT: AtomicU32 = AtomicU32::new(0);

/// What Postmark answers when all is well: every send is taken.
pub fn postmark(asked: &Asked) -> Said {
    match asked.named().as_str() {
        "POST /email" => Said::json(
            200,
            &json!({
                "ErrorCode": 0, "Message": "OK",
                "MessageID": format!("pm-out-{}", SENT.fetch_add(1, Ordering::Relaxed)),
            }),
        ),
        _ => Said::json(404, &json!({"ErrorCode": 701, "Message": "no such endpoint"})),
    }
}

/// What Resend answers when all is well: every send is taken, and it holds no received mail.
pub fn resend(asked: &Asked) -> Said {
    match asked.named().as_str() {
        "POST /emails" => Said::json(
            200,
            &json!({"id": format!("re-out-{}", SENT.fetch_add(1, Ordering::Relaxed))}),
        ),
        _ => Said::json(404, &json!({"name": "not_found", "message": "Email not found"})),
    }
}

/// Reads one request: its head, then as many bytes of body as its `Content-Length` says.
fn read(mut stream: &TcpStream) -> Option<Asked> {
    let mut seen = Vec::new();
    let mut buffer = [0_u8; 4096];
    let head_end = loop {
        if let Some(at) = seen.windows(4).position(|window| window == b"\r\n\r\n") {
            break at;
        }
        match stream.read(&mut buffer) {
            Ok(read) if read > 0 => seen.extend_from_slice(&buffer[..read]),
            _ => return None,
        }
    };
    let head = String::from_utf8_lossy(&seen[..head_end]).into_owned();
    let mut lines = head.split("\r\n");
    let mut first = lines.next()?.split(' ');
    let (method, target) = (first.next()?.to_owned(), first.next()?.to_owned());
    let headers: Vec<(String, String)> = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.to_ascii_lowercase(), value.trim().to_owned()))
        .collect();
    let length = headers
        .iter()
        .find(|(name, _)| name == "content-length")
        .and_then(|(_, value)| value.parse::<usize>().ok())
        .unwrap_or(0);
    let mut body = seen[head_end + 4..].to_vec();
    while body.len() < length {
        match stream.read(&mut buffer) {
            Ok(read) if read > 0 => body.extend_from_slice(&buffer[..read]),
            _ => return None,
        }
    }
    Some(Asked {
        method,
        target,
        headers,
        body: String::from_utf8_lossy(&body).into_owned(),
    })
}

fn write(mut stream: &TcpStream, said: &Said) {
    let mut head = format!(
        "HTTP/1.1 {} Said\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n",
        said.status,
        said.body.len()
    );
    for (name, value) in &said.headers {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(said.body.as_bytes());
    let _ = stream.flush();
}
