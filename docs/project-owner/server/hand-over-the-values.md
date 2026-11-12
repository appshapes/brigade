# Hand over the values

| Value | Looks like | Where to get it | Who may have it |
| --- | --- | --- | --- |
| Server URL | `https://<host>:8743` | the name or address members reach the server by, and the port of `--listen` | everyone on the team; committed in `.brigade.json` |
| Certificate fingerprint | `sha256:` and 64 hex digits | the server's start line, `(certificate sha256:…)` | everyone on the team; committed in `.brigade.json` |
| Creation token | one line of your choosing | the file you gave `--create-token-file` | the Team Admins you let create teams, over a password manager |
| Data directory | | the server's machine | nobody; it holds every message |

- **The URL** is the one members' machines can reach: a name they resolve, or an address. A member's session
  refuses a plain `http://` URL unless the host is the member's own machine.
- **The fingerprint** goes with a certificate members' machines do not already trust: the one the server made
  with `--tls-self-signed`, or one from an authority of your own. With a certificate from a public authority,
  hand over the URL alone.
- **To read the fingerprint again**: `journalctl -u brigade-server | grep 'listening on'`, or
  `openssl x509 -noout -fingerprint -sha256 -in <data>/tls/certificate.pem`. `team create` takes either spelling.
- **The creation token** exists only if you set one. Without it anyone who can reach the server can create a
  team, and there is nothing to hand over.

The Team Admin's side is [Create the team on a server](../../team-admin/create/create-on-a-server.md).
