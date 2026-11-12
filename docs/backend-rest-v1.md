# Brigade backend REST contract, version 1 (draft)

A server that implements this contract is a Brigade backend. Brigade's bundled `rest` adapter
(`brigade adapter rest`) speaks it, so an organisation that wants its own backend writes a server and no adapter.

**Status: draft.** Version 1 is frozen with the first release of a server that implements it (work item 89).
Until then a change to this page lands in one commit with the adapter, the stand-in server and the conformance
run.

The contract is [BAP/1](protocol-v1.md) carried over HTTP. Section numbers below, such as 4.5.5, are that
document's. A command the adapter cannot answer from its own files becomes one request, the request body is the
command's input, and the response body is the command's result envelope. Every rule of BAP/1's section 4.5 that
names "the adapter" as an authority is the **server's** to keep: the server stamps identity, ids and time, checks
ownership and membership, enforces the limits and the quotas, and keeps the leases and the retention. The adapter
validates what it sends as a courtesy; a server never relies on it.

## 1. Transport

- **Base URL.** `https://<host>[:<port>][/<prefix>]`. `http://` is accepted by the adapter for a loopback host
  only (`127.0.0.1`, `::1`, `localhost`). Every endpoint is under `<base>/v1/`.
- **Requests.** `POST`, `Content-Type: application/json`, one JSON object of at most 1 MiB. A command with no
  input sends `{}`.
- **Credential.** `Authorization: Bearer <credential>` on every request once the adapter holds one. A credential
  is an opaque string of 16 to 512 characters from `A-Z a-z 0-9 . _ ~ + / = -`, issued by the server (§3) and
  never shown again. A server stores a credential's hash, never the credential.
- **Responses.** `Content-Type: application/json`, and the body is always one BAP/1 result envelope (4.3):
  `{"ok": true, "protocol_version": "1", "result": {…}}` or `{"ok": false, "protocol_version": "1", "error":
  {…}}`. The error object is BAP/1's, with `retryable` always present and `retry_after_ms` on `rate_limited`.
  A message, a detail or a header never carries a secret, raw input or a credential.
- **Status.** The HTTP status follows the envelope:

  | Envelope | Status |
  | --- | --- |
  | `ok: true` | 200 |
  | `invalid_input` | 400 |
  | `unauthenticated` | 401 |
  | `unauthorized` | 403 |
  | `not_found` | 404 |
  | `conflict` | 409 |
  | `loop_detected` | 422 |
  | `rate_limited` | 429 |
  | `internal` | 500 |
  | `unavailable` | 503 |

  A server never answers `usage`, `config` or `protocol_mismatch`: those are the adapter's own.
- **What the adapter does with a response.** A body that is a valid envelope is the answer, whatever the status.
  Anything else is `unavailable` when the status is 5xx or the request failed in transport (refused, timed out,
  TLS), and `internal` otherwise.
- **Unknown members** of a request are ignored, with BAP/1's one exception: the forbidden members of a send
  (4.4.6) are refused. An unknown path is `not_found` with status 404.

## 2. Endpoints

`<session>` below is the value BAP/1 passes as `--session`, and `<team>` is the `team_ref` the adapter's profile is
bound to (4.5.6: every command operates within the profile's team). Results are the shapes of BAP/1's section 4.4
unless this table adds a member.

| Command (4.2) | Path | Request body | Result |
| --- | --- | --- | --- |
| `team create` | `/v1/team/create` | `{"team_name", "human_label"?}` | 4.4.10, plus `credential` (§3) |
| `team join` | `/v1/team/join` | `{"join_secret", "human_label"?}` | 4.4.10, plus `credential` (§3) |
| `team leave` | `/v1/team/leave` | `{"team_ref": <team>}` | 4.4.10 |
| `team members` | `/v1/team/members` | `{"team_ref": <team>}` | 4.4.10 |
| `session register` | `/v1/session/register` | `SessionRegistration` (4.4.2), plus `team_ref: <team>` | 4.4.2's result |
| `session heartbeat` | `/v1/session/heartbeat` | `{"session_id": <session>, "heartbeat": HeartbeatRequest}` (4.4.4) | 4.4.4's result |
| `session list` | `/v1/session/list` | `{"team_ref": <team>, "session_id"?: <session>, "include_offline"?: boolean}` | 4.4.3's list result |
| `session close` | `/v1/session/close` | `{"session_id": <session>}` | `{"session_id", "state": "offline"}` |
| `message send` | `/v1/message/send` | `SendRequest` (4.4.6) | `SendResponse` (4.4.7) |
| `message receive` | `/v1/message/receive` | `{"session_id": <session>, "limit"?, "after"?, "wait_seconds"?}` | `{"messages": MessageEnvelope[], "cursor"}` (§4) |
| `message ack` | `/v1/message/ack` | `{"session_id": <session>, "message_ids": […]}` | `{"acked", "unknown"}` (4.4.8) |
| `profile revoke-credentials` | `/v1/credential/revoke` | `{}` | `{"revoked": true}` |
| (none) | `/v1/info` | `{}` | `{"contract_version": "1", "server": {"name", "version"}}` |

`describe`, `profile init`, `profile status` and `profile reset` are answered by the adapter from its own files;
`profile reset` first asks `/v1/credential/revoke`, best effort. `message watch` is the adapter's loop over
`/v1/message/receive`, `/v1/message/ack`, `/v1/session/heartbeat` and `/v1/session/close` (§4).

`/v1/info` needs no credential and says nothing about any team.

## 3. Principals and credentials

A principal is the server's record of one member's installation. A credential names exactly one principal.

- **`team create` and `team join` with no credential, or with one the server does not know**, create a principal.
  The result carries `credential`, a new credential for it. The adapter writes it to its credential file (mode
  0600) and never prints it.
- **`team create` and `team join` with a known credential** act for that principal. When a join's principal was
  a member of that team before and left, the same membership is re-activated: the same `principal_ref`, and
  `rejoined: true` (4.2). The result carries no `credential`, or a new one that replaces the old.
- **A rejected join** is `unauthorized` with text that is byte-identical for a wrong secret, an unknown team and
  a banned principal (4.5.7), and creates nothing.
- **`/v1/credential/revoke`** makes the presented credential unknown to the server. The principal's membership
  is not changed by it.
- **Every other endpoint** needs a known credential, else `unauthenticated`. A known credential whose principal
  is not an active member of the team a request names (`team_ref`, §2), or of the team of the session it names,
  is `unauthorized` on every endpoint that touches a team, its sessions or their messages, with the
  byte-identical text of 4.5.7 for a team that exists and one that does not. `/v1/team/leave` is the one
  exception: leaving a team the principal is not an active member of answers `left: true` and changes nothing
  (4.4.10: idempotent).
- **Who may create a team.** A server may require a creation token on `/v1/team/create`, sent as the header
  `Brigade-Create-Token`; a missing or wrong token is `unauthorized`. The token is the operator's, is read by the
  adapter from a file and never from argv, and is not part of version 1's required surface.

The join secret has BAP/1's format, `brg1.<team_ref>.<secret>` (4.4.10), and the server keeps what it needs to
check one, never the secret itself where a hash will do.

## 4. Receiving and watching

`/v1/message/receive` is BAP/1's `message receive` with two additions that make a watch cheap.

- **`limit`**: 1 to 200, default 50, oldest first.
- **`cursor`** in every result: an opaque string of at most 256 characters that stands for "everything accepted
  for this session up to now".
- **`after`**: a cursor from an earlier result for the same session. The result then holds only messages
  accepted for the session after that cursor was issued. Without `after` the result holds every accepted,
  unacknowledged message, as `message receive` does. A cursor the server does not recognise is treated as
  absent.
- **`wait_seconds`**: 0 to 30, default 0. When the result would be empty, the server holds the request until a
  message is accepted for the session or the time has passed, and then answers. A message accepted while a
  request waits is in that request's answer within one second. A request that waits is also answered within one
  second of the moment its caller would be refused: `unauthorized` when the principal's membership ends,
  `unauthenticated` when its credential is revoked (4.5.7: a running watch of a member who left ends with
  `unauthorized`, and the conformance suite gives it five seconds).

A watch is therefore: one receive without `after` (the catch-up), then receives with `after` and
`wait_seconds`, and a receive without `after` at each periodic drain so that an unacknowledged message is
emitted again (4.4.9). Because a waiting receive answers within a second of an acceptance, the adapter advertises
`message.watch.push`.

Receiving never acknowledges. Ownership is checked on every receive: a session that is unknown, foreign or not
the principal's is `not_found` (4.5.7).

## 5. What a version 1 server implements

- Every endpoint of §2 with the semantics of BAP/1's section 4.5, including durable acceptance before the answer
  to a send (4.5.1), idempotency keys (4.5.4), server-stamped identity (4.5.5), team isolation (4.5.6),
  ownership (4.5.7), leases with the server's own clock (4.5.8), retention (4.5.9), and the rate limits, quotas
  and hop counting of 4.5.12.
- The limits, the lease range and the retention floors of BAP/1's 4.4.1 exactly: the adapter advertises those
  constants in `describe` without asking the server, and the conformance suite checks a value at each cap.
  A member BAP/1 gives no cap (`harness`, `harness_version`) may have one of the server's own, of 256 code
  points or more, refused as `invalid_input` like any other: a server need not store a megabyte it was sent.
- The session members behind these capabilities, stored and reported: `session.description`, `session.resume`,
  `session.workspace_label`, `session.inbound`, `session.model`, `session.context_used_tokens`,
  `session.human_label`, `session.brigade_version` and `session.sync_peer`; and `team.create`, `team.join` and
  `team.roster`.

The admin verbs (`team.admin`) are not in version 1: BAP/1 does not yet give them shapes.

## 6. Proving a server

Run Brigade's conformance suite through the bundled adapter against the server:

```
bin/brigade-conformance --env BRIGADE_REST_URL=<base> --adapter bin/brigade -- adapter rest
```

A server is correct when the suite is green, `--slow` included. The suite makes its own teams, so the server
must let the run create them.
