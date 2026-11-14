# Run the server

```sh
brigade-server --data /var/lib/brigade-server --listen 0.0.0.0:8743 --tls-self-signed
```

`brigade-server --help` lists every option. A start that cannot go on says why in one line and changes nothing.

## The data directory

`--data` is the one thing the server needs. It creates the directory, mode 0700, and keeps everything in it:

| In it | What |
| --- | --- |
| `feed/` | the ordered log of every change, the record of truth |
| `snapshots/` | the state at a point of the log, so that a start reads little; the newest three are kept |
| `record.key` | the key the log's entries are sealed under; a directory that has a log and has lost this file is refused |
| `tls/` | the certificate the server made for itself and its key, with `--tls-self-signed` |
| `gateway/` | the mail and Slack gateways' own records, with `--gateway-config`: the credential each is a member by, and which thread a carried message belongs to |
| `lock` | one server per directory |

Run the server as a user of its own, and give that user the directory and nothing else.

## How it serves HTTPS

With no option the server serves plain HTTP on `127.0.0.1:8743` and nowhere else. To be reached from other
machines it serves HTTPS, one of three ways:

| Option | Use it when | The team file |
| --- | --- | --- |
| `--tls-self-signed` | you have no certificate and want none to manage | pins the fingerprint the server prints |
| `--tls-cert <file> --tls-key <file>` | you have a certificate for the server's name, from a public authority or your own | needs no fingerprint when members' machines already trust the certificate; pins it otherwise |
| `--tls-in-front` | a proxy or a tunnel in front of the server ends TLS | as the proxy's certificate requires |

- **`--tls-self-signed`.** On its first start the server makes a key and a certificate and keeps them in
  `<data>/tls`; every later start serves the same one. Its start line names the fingerprint, `(certificate
  sha256:…)`, and that is what the Team Admin passes to `team create`. Members' sessions then accept that
  certificate and no other: no authority, name or date is consulted, as with an SSH host key.
- **Your own certificate.** The server reads the two PEM files when it starts; after you renew the certificate,
  restart it. The start line names this certificate's fingerprint too.
- **A proxy in front.** The server then serves plain HTTP, and whatever stands in front can read everything. It
  must pass the `Authorization` and `Brigade-Create-Token` headers, allow a body of 1 MiB, hold a quiet request
  open for more than 30 seconds, and never send a `POST` again by itself.

A start that would put plain HTTP on an address that is not loopback, with none of the three, is refused.

## Who may create a team

Anyone who can reach the server can create a team on it. A team is still closed by its join secret: creating one
gives no way into another.

To require a token, put one line of your choosing in a file only the server's user can read, and start the server
with `--create-token-file <file>`. Creating a team then needs that token, and the Team Admin gives `team create`
the path of a file that holds it ([Hand over the values](hand-over-the-values.md)).

## What one member may do in an hour

From server 0.6.0 one member's installation, which the server knows by its credential, is held to the numbers a
team in a Supabase project meets. They are the server's own and there is no option for them.

| | |
| --- | --- |
| Teams created | 5 in an hour |
| Sessions opened | 120 in an hour, over all its teams; a session that comes back is not counted |
| Joins that failed | after 5 in fifteen minutes its next join is refused, the right secret included, until the oldest is fifteen minutes old; a join that succeeds clears them |

- A member who meets one is told `rate_limited`, which bound it was and how long to wait.
- The teams and the sessions are counted from the server's log, so a restart keeps the count. The failed joins
  are counted in memory, and a restart forgets them.
- The server's own gateways are not held by the failed joins: one that was put out of its team still joins
  again within about a minute of its secret file being replaced ([Open the gateways](gateways.md)).
- **None of this holds a stranger.** A caller with no credential is a new member with each request, so a server
  that strangers can reach can still be filled by them. What holds them is a creation token (above), a server
  that is not on the public internet, or a limit by address in the proxy in front of it, which is the one place
  that sees the address.

## As a service

A unit for systemd, `/etc/systemd/system/brigade-server.service`:

```ini
[Unit]
Description=Brigade server
After=network-online.target
Wants=network-online.target

[Service]
User=brigade
ExecStart=/usr/local/bin/brigade-server --data /var/lib/brigade-server --listen 0.0.0.0:8743 --tls-self-signed
Restart=on-failure
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
```

```sh
sudo useradd --system --home-dir /var/lib/brigade-server --shell /usr/sbin/nologin brigade
sudo install -d -o brigade -g brigade -m 0700 /var/lib/brigade-server
sudo systemctl enable --now brigade-server
journalctl -u brigade-server | grep 'listening on'
```

The server stops on `SIGTERM`: it takes no new connection, answers what it is answering, writes a snapshot and
ends. A start after a crash reads the log from the newest snapshot; nothing it had answered is lost.

## As a container

```sh
docker run -d --name brigade-server --restart unless-stopped --shm-size=256m \
  -p 8743:8743 -v brigade-server-data:/data \
  ghcr.io/appshapes/brigade-server:0.1.0 --listen 0.0.0.0:8743 --tls-self-signed
docker logs brigade-server 2>&1 | grep 'listening on'
```

The image already names `--data /data`; what follows the image's name are the server's other options. Keep the
volume: it is the data directory.

## Memory and ports

- **Shared memory.** The log's driver keeps its pages in `/dev/shm`. The server needs 201 MiB there and checks
  before it starts; without it, it ends with the number it found. A container gets 64 MiB unless you pass
  `--shm-size`. `--driver-dir <dir>` names another place.
- **Ports.** Members reach the TCP port of `--listen`. The log uses UDP ports 8744 and 8745 on loopback; they are
  the server's own and are not to be opened to anyone.
