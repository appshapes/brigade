# `brigade adapter syncthing` — the team-folder adapter

The third BAP/1 adapter (card 141), bundled in the one binary: `brigade adapter syncthing <group> <verb>`. Its
backend is one directory per team that Syncthing keeps in step between the members' machines, through the engine
of [`internal/syncthing`](../../syncthing/doc.go), which the sync adapter of
[`internal/syncadapters/syncthing`](../../syncadapters/syncthing/doc.go) drives for a project's folders. There is
no server, no project and no account. The wire contract is [`docs/protocol-v1.md`](../../../docs/protocol-v1.md)
(BAP/1, frozen); what the protocol leaves to the adapter (4.8) is described here and nowhere else. What it changes
in the security picture is [`docs/security.md`, section 18](../../../docs/security.md#18-the-syncthing-adapter);
the administrator's page is
[Create a team over Syncthing](../../../docs/team-admin/create/create-over-syncthing.md).

The store is the test fs adapter's layout ([`../fs/README.md`](../fs/README.md)), which was written as the dry
run for this, with the changes a replicated directory needs. The test adapter stays as it is.

## Root resolution and local mode

1. `--root <dir>` (a leading flag, before the group);
2. else `$BRIGADE_SYNCTHING_STORE`;
3. else `${BRIGADE_STATE_DIR}/syncthing/teams`.

A relative value is refused with `config` (exit 11). Rules 1 and 2 are **local mode**: the root is a directory the
caller shares by other means, and the adapter neither starts, configures nor asks the engine anything. That is how
the conformance suite runs it in `make test` (`--shared-env BRIGADE_SYNCTHING_STORE`: three principals, one
store, no daemon) and how a test drives it. No live session ever sets the variable: the harness builds a child's
environment from scratch (4.1), so a shipped installation is always rule 3, with the engine.

## What it stores

The profile lives where every adapter's does, `${BRIGADE_CONFIG_DIR}/profiles/<name>/`, mode 0700:

| File | Contents |
| --- | --- |
| `profile.json` | `adapterkit.Profile` with `adapter: "syncthing"`, the team binding, `principal_ref`, `human_label`, and `devices`: the seeds of the team file. Never a secret. |
| `credential.json` | `{principal_ref, created_at, last_team_ref, join_digest}`, mode 0600. Its presence is the credential. `join_digest` is the SHA-256 of the secret the last join presented, kept until the team file arrives (below); never the secret. |

The root holds one directory per team, the Syncthing folder, and the lock beside them — outside every folder,
because Syncthing replaces a synced file by rename, which would break a flock's identity, and the lock is this
machine's alone:

```text
<root>/.lock                                        flock sidecar; every command but `describe` holds it
<root>/<team_ref>/                                  the team folder: Syncthing id brigade-<team_ref[:8]>-team-messages
<root>/<team_ref>/.stignore                         the atomic writer's temporary files, .DS_Store
<root>/<team_ref>/team.json                         {team_ref, team_name, join_secret_sha256, secret_version, secret_rotated_at, created_by, created_at}
<root>/<team_ref>/members/<principal_ref>.json      {principal_ref, human_label, status: active|left, joined_at, device_id, secret_version}
<root>/<team_ref>/revoked/<principal_ref>.json      {principal_ref, device_id, revoked_at, ban, by}
<root>/<team_ref>/sessions/<session_id>.json        the session; `state` is computed at read time
<root>/<team_ref>/inbox/<recipient>/<seq>.<id>.json a pending protocol.MessageEnvelope
<root>/<team_ref>/acked/<recipient>/<seq>.<id>.json the same file after acknowledgement, plus acked_at
<root>/<team_ref>/idem/<sender>/<sha256(key)>.json  {message_id, fingerprint, recipient_session_id, hop_count, created_at}
```

The folder id has four dash-separated parts on purpose: the sync adapter's `teamPrefix` reads only three-part ids
as a checkout's folders, so it never pauses this one.

**One writer per path.** A message file is written once, by its sender, and moved to `acked/` by its recipient; a
session file by its owner; `members/<me>.json` by me (`team join`, `team leave`); `revoked/<p>.json` by the
creator, deleted by a rejoin; `team.json` by the creator (`team create`, `team rotate-secret`, `team transfer`).
The retention sweep runs on every machine, and two deletions of one file never conflict. A conflict copy Syncthing
might still write (`<name>.sync-conflict-…`) is never read: its stem fails the identifier check.

Identifiers are opaque: 16 random bytes as lowercase hex. A join secret is `brg1.<team_ref>.<32 hex>` and only
its SHA-256 is stored. `seq` is the wall clock in nanoseconds, floored at one past the highest already used for
that recipient on this machine; two senders on two machines may pick one number, and the random id after it
keeps their files apart. Session state is computed from `closed_at`, `lease_until` and `activity` on the reading
machine's clock.

## Membership

A principal is an active member when its own file says `active` and the creator holds no marker for it under
`revoked/`. Every read filters on that: `session list`, a message's recipient, the watch's every poll. The
refusal is one fixed `unauthorized` with no details, byte-identical for a missing team, a missing membership and
a revoked one, and `team join` answers one fixed `unauthorized` for a wrong secret, an unknown team and a banned
principal.

`team leave` writes `status: left` into the member's own file and closes its own sessions. `team revoke-member`
(creator only) writes the marker, never the member's file; `--ban` sets `ban: true`; the marker rewritten
without `--ban` un-bans; `--max-version <n>` writes a marker for every active member whose `secret_version` is at
most n, never the creator. `team rotate-secret` writes the new digest and bumps `secret_version`; `team transfer`
rewrites `created_by`.

## Joining is device acceptance

`team join` parses the secret, checks the binding locally, then:

- **the team file is here** (the folder arrived earlier, or the store is shared): the digest is checked, a
  non-ban marker is deleted (the rejoin), the member file is written with the team's `secret_version`;
- **it is not, and the engine runs**: the join is pending. The request's `backend` member carries the team file's
  `devices` (the seeds) and `team_name`. The engine is attached, this machine's device is named
  `brigade1-<unix seconds>-<hex HMAC-SHA256(key = SHA-256(secret), "<device id>\n<seconds>")>`
  (`proof.go`), the seeds are added as devices and the team folder is posted shared with them, the member file
  is written with `secret_version: 0`, and the profile is bound with the backend's team name while the credential
  keeps the digest. The join answers at once. In local mode, or with no seed, it is refused (`unauthorized` and
  `config` respectively).

`completeJoin` runs under the lock on every later command: when the team file has arrived and the credential
holds a digest, the digest is compared with the team's. A match records the secret version and the team's name
and clears the digest; a mismatch — a rotation before the folder arrived — marks the membership `left`, and the
person joins again with the current secret.

## The engine round

`message watch` attaches the engine at start (starting it when it is stopped), holds the reference while it
runs, and detaches at exit, so Syncthing runs while a session lives and stops with the last one. `team create`
and `team join` attach too, to learn the device id, and leave their reference to the next prune. Every other
command touches only the local replica. A machine where the engine cannot start still watches its replica.

Every `engineTick` (5 s) the watch does one `reconcile` for the team:

1. the replica is read under the lock: the team file, the active members, the markers;
2. every **pending device** whose announced name is a proof keyed by the team's current digest is accepted — a
   `POST /rest/config/devices` — unless a ban marker names its device; a revoked (not banned) device whose proof
   is newer than its marker is re-accepted and the marker deleted, which is its rejoin;
3. the **team folder** is posted when its device list differs from the union of the devices it has, the seeds,
   the active members' devices and this round's acceptances — never this machine's own, never a device whose
   marker is past the grace — with `fsWatcherDelayS: 1`, no versioning, `rescanIntervalS: 60`;
4. a device whose marker is `revokeGrace` (15 s) old is removed from the engine, and with it from every folder.

The grace is what lets the marker reach the revoked machine before nothing syncs with it: its own watch ends
with `unauthorized` when the marker lands. Measured on one machine with two engines
(`BRIGADE_TEST_SYNCTHING=1 go test ./internal/adapters/syncthing -run TestRealTwoEngines`): the folder reached the
joiner 7.6 s after the join, its session was on the creator's roster at 10.2 s, a message crossed in 2.0 s, the
revocation ended the revoked watch in 7.5 s and the device was dropped at 18.5 s.

## What it advertises

`describe` answers `protocol_version "1"`, `delivery {at_least_once, none, injected}`, the protocol's `limits`
and `retention`, `lease {default_seconds: 90, min_seconds: 1, max_seconds: 600}` (the test adapter's range, so
the suite's lease cases take seconds; a harness never asks for less than its heartbeat interval), and the
capabilities of the fs adapter plus `team.admin`. It does not advertise `message.watch.push`: the watch polls
its replica twice a second, and the `ready` event says `"mode": "polling"`.

`profile status` is this adapter's own: `{name, state, team_ref, team_name, principal_ref, human_label,
device_id, engine: running|stopped|local, team_folder: present|pending|none, seeds}`. The harness reads
`device_id` after `team create` to write the seed into the project's team file.

## Errors

The 4.6 taxonomy, as the fs adapter's, plus:

| Situation | Code | Exit |
| --- | --- | --- |
| an admin verb by a member who is not the creator | `unauthorized` | 5 |
| a join before the folder arrived, with no seed in the team file | `config` (`no_seed_device`) | 11 |
| `team create --team-ref` naming a team this machine already holds | `conflict` (`team_exists`) | 7 |
| `syncthing` not on PATH when a join or create needs the engine | `unavailable` | 9 |
