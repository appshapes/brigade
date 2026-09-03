# `brigade-adapter-fs` — the filesystem adapter

**This adapter is insecure and test-only. Never point it at anything you care about.** Its backend is one directory on
disk with no access control beyond file modes: any process that can read the root can read every team's messages, every
team's membership and every session's inbox. There is no authentication — the presence of a `credential.json` file
beside the profile *is* the credential — and the join secret's digest is stored next to the data it protects. It is a
development binary: `make build` produces `bin/brigade-adapter-fs`, and nothing ships it.

It exists for four reasons. It is the second BAP/1 implementation, so the plugin can be proved to carry no Supabase
assumption (D3). It runs the conformance suite of plan 9.2 in seconds instead of the tens of seconds a hosted backend
costs. It is the harness's test fixture. And it is the dry run for the object-store adapter of plan section 12.

The wire contract is [`docs/protocol-v1.md`](../../../docs/protocol-v1.md) (BAP/1, frozen). Section numbers below are
its. Everything the protocol deliberately leaves to an adapter (4.8) — the store layout, the credential file, the shape
of an identifier — is described here and nowhere else.

## Running it by hand

```sh
make build
export BRIGADE_CONFIG_DIR=$(mktemp -d) BRIGADE_STATE_DIR=$(mktemp -d) BRIGADE_FS_ROOT=$(mktemp -d)/store

bin/brigade-adapter-fs describe
echo '{"team_name":"ops","human_label":"you@example.com"}' | bin/brigade-adapter-fs team create
echo '{"harness":"shell","harness_version":"1","session_name":"a","activity":"busy","inbound":"accept"}' \
  | bin/brigade-adapter-fs session register
echo '{"harness":"shell","harness_version":"1","session_name":"b","activity":"idle","inbound":"accept"}' \
  | bin/brigade-adapter-fs session register
echo '{"sender_session_id":"<A>","recipient_session_id":"<B>","body":"hello"}' \
  | bin/brigade-adapter-fs message send
bin/brigade-adapter-fs message receive --session <B>
bin/brigade-adapter-fs message watch --session <B>          # NDJSON until stdin EOF, a close command or SIGTERM
```

`team create` prints the join secret once. A second profile joins with it:

```sh
echo '{"join_secret":"brg1.…","human_label":"them@example.com"}' \
  | bin/brigade-adapter-fs --profile them team join
```

Three worked examples live in `cmd/brigade/testdata/script/`: `fs-team.txtar`, `fs-session.txtar` and
`fs-message.txtar`. They drive the real binary as a child process and are the readable end of this document.

## Argv

```text
brigade-adapter-fs [--root <dir>] [--profile <name>] [--log-level <l>] <group> <verb> [flags]
```

The leading flags exist because the harness prepends `adapter_command`'s fixed arguments (4.1, decision 10):
`["brigade-adapter-fs", "--root", "/x"]` followed by `["describe"]`. `--root` is accepted **only** in that leading
position; after the verb it is an unknown flag and therefore `usage` (C-02). `--profile` and `--log-level` are accepted
in both places, and the later occurrence wins.

`--join-secret` is refused with `usage` on every command and its value is never echoed (4.5.14, C-05). An invalid
`--log-level` **flag** value is `usage`; an invalid `BRIGADE_LOG_LEVEL` **environment** value is `config`.

`-h`, `--help` write a short usage text to **stderr** and exit 2 with the envelope on stdout: stdout is protocol output
and never free text (4.1). This is a test adapter; nobody reads its help.

## Root resolution

1. `--root <dir>`;
2. else `$BRIGADE_FS_ROOT`;
3. else `${BRIGADE_STATE_DIR}/fs-adapter`, with `BRIGADE_STATE_DIR` resolved by `adapterkit.StateDir` (so
   `XDG_STATE_HOME` and `HOME` apply, and a relative `XDG_STATE_HOME` is ignored).

A relative `--root` or `BRIGADE_FS_ROOT` is refused with `config` (exit 11), exactly as `adapterkit` refuses a relative
`BRIGADE_STATE_DIR`: hooks run with the working directory set to the project tree, so a relative root would put a
team's messages inside somebody's repository.

Under a live Claude Code session `BRIGADE_FS_ROOT` never arrives — the harness builds the child's environment from
scratch (4.1) — so the root is either rule 3's default (which needs no plumbing at all, because `BRIGADE_STATE_DIR`
IS one of the four variables the harness computes) or a `--root` given as a fixed argument in the `adapter_command`
array. See *Under the plugin* below.

`team create --secret-file <path>` is held to the same rule for the same reason: a relative path is `usage` (exit 2),
because the join secret would otherwise be written into whatever directory the process happens to be run from. The
file is written **before** the team is created and the profile is bound, so a path the adapter cannot write is
`config` (exit 11) and leaves nothing behind — the secret is printed only once, and a caller who never received it
must be free to try again rather than end up bound to a team nobody can join.

## Under the plugin

The fs adapter is the backend `make plugin-dev adapter=fs` uses, and this is the whole onboarding — run **once, in
your own terminal**, before the first session. Not from inside a Claude Code session: `team create` and `team join`
refuse there with `usage` ("run this in your own terminal: the join secret must never pass through the chat"). The
sequence below is the one measured in `docs/experiments/E3-wiring.md`.

```sh
make build

# 1. Bind a profile to this adapter. D36 writes the sidecar
#    ${BRIGADE_CONFIG_DIR}/profiles/default/adapter, so every later session resolves the fs adapter by itself
#    and needs no `adapter_command` option at all.
bin/brigade profile init --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'

# 2. Create the team. The secret goes to a 0600 file, never to your scrollback.
bin/brigade team create --name ops --label dev --secret-file ~/brigade-ops.secret

# 3. Start a session. `adapter=fs` passes the same command as the per-session override; after step 1 it is
#    redundant, and `make plugin-dev` alone works just as well.
make plugin-dev adapter=fs
```

The store lands at `${XDG_STATE_HOME:-~/.local/state}/brigade/fs-adapter` — rule 3's default — and the session's
adapter children find it there without `--root` and without `BRIGADE_FS_ROOT`, because `BRIGADE_STATE_DIR` is one of
the four variables the harness computes for every child.

A **second profile on the same machine** (`make plugin-dev profile=bob`) joins that team rather than creating one.
The secret travels from the file into the request document on stdin and never onto argv:

```sh
bin/brigade profile init --profile bob --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
{ printf '{"human_label":"bob","join_secret":"'; tr -d '\n' < ~/brigade-ops.secret; printf '"}'; } |
  bin/brigade team join --profile bob
```

(`bin/brigade team join --profile bob --prompt` is the interactive form: the secret is read from the TTY without
echo, then an optional label.) `bin/brigade profile status --profile bob` prints which adapter that profile
resolves to and where the choice came from.

A second profile can also be pointed at a **different store** by registering a name whose command carries a fixed
`--root`, which is how two sessions on one machine end up with genuinely independent backends:

```sh
bin/brigade profile init --profile beta \
  --adapter 'fsdev=["'"$PWD"'/bin/brigade-adapter-fs","--root","/tmp/brigade-beta-store"]'
bin/brigade team create --profile beta --name beta --label dev --secret-file ~/brigade-beta.secret
make plugin-dev profile=beta
```

Verified consequences of that split: `bin/brigade profile status --profile beta` reports `default adapter fsdev
(from sidecar)`, and a session on `beta` sees only `beta`'s sessions in `brigade sessions` — the two stores share
nothing.

Two things about the fs adapter that matter only under the plugin:

- **Its watch is polling**, not push: `message watch` re-takes the store lock every 200 ms. Delivery latency is
  therefore up to a poll interval, and an unacknowledged message is re-emitted only when the watch child restarts —
  not on every drain the way a server-backed adapter would.
- **Nothing here is a security boundary.** Any process that can read the root can read every team's messages. Point
  it at a throwaway directory, never at anything you care about.
- **Expect the shadowing warning if you symlink your own build.** `ln -s <repo>/bin/brigade ~/.local/bin/brigade`
  puts a `brigade` on the hook's `PATH` that does not resolve to `plugin/bin/brigade`, so every session start
  carries a second line saying so. A symlink to `plugin/bin/brigade` itself — what the setup skill suggests — is
  silent. Both measured in `docs/experiments/E3-wiring.md`.

## What it stores

The profile lives where every adapter's does, `${BRIGADE_CONFIG_DIR}/profiles/<name>/`, mode 0700:

| File | Contents |
| --- | --- |
| `profile.json` | `adapterkit.Profile` with `adapter: "fs"`, the team binding (`team_ref`, `team_name`), `principal_ref`, `human_label`. Never a secret. |
| `credential.json` | `{principal_ref, created_at, last_team_ref}`, mode 0600, read through the strict reader — a group- or world-readable copy is refused with `config`. Its presence is the credential; `last_team_ref` is what lets a repeated `team leave` still answer a non-empty `team_ref`. |

The store lives under `<root>`, every directory 0700 and every file 0600:

```text
<root>/.lock                                              flock sidecar; every command but `describe` holds it
<root>/teams/<team_ref>/team.json                         {team_ref, team_name, join_secret_sha256, created_by, created_at}
<root>/teams/<team_ref>/members/<principal_ref>.json      {principal_ref, human_label, status, joined_at}
<root>/teams/<team_ref>/sessions/<session_id>.json        the session; no `state` — that is computed at read time
<root>/teams/<team_ref>/inbox/<recipient>/<seq>.<id>.json a pending protocol.MessageEnvelope
<root>/teams/<team_ref>/acked/<recipient>/<seq>.<id>.json the same file after acknowledgement, plus acked_at
<root>/teams/<team_ref>/idem/<sender>/<sha256(key)>.json  {message_id, fingerprint, recipient_session_id, hop_count, created_at}
```

`describe` creates none of it. Every other command takes the store lock for the whole of its store access — the suite
and the harness run adapter processes concurrently, and one writer at a time is the entire concurrency design. The
watch loop takes and releases the lock once per 200 ms poll, never across the sleep. On every one of those polls it re-applies the 4.5.7 membership check under that lock before it reads the inbox, so a watch whose principal is revoked while it runs (a `team leave`, an administrator, a deleted team) emits one `unauthorized` error event and exits 5 within a poll interval instead of going on delivering the team's messages to a former member.

Identifiers are opaque (4.8): 16 random bytes as lowercase hex. A join secret is `brg1.<team_ref>.<32 hex>` and only
its sha256 is stored. `seq` is the wall clock in nanoseconds, floored at one past the highest number already used for
that recipient, zero-padded to 19 digits — so lexical order equals send order, monotonically, across acknowledgements
and deletions. Every identifier that becomes a path component is checked first: anything outside `[A-Za-z0-9_-]{1,64}`
is simply not found, so a rebound `profile.json` or a hostile `--session` cannot escape the root.

Session state is computed at read time from `closed_at`, `lease_until` and `activity` (4.5.8), never stored. The
retention sweep of 4.5.9 runs under the lock at the start of every command except `describe`, and does nothing at all
when no team exists.

## What it advertises

`describe` (4.4.1) answers `protocol_version "1"`, `delivery {at_least_once, none, injected}`, the protocol's own
`limits` and `retention`, and these capabilities (4.7):

`team.create`, `team.join`, `team.roster`, `message.receive`, `message.watch.stdin_commands`, `session.description`,
`session.resume`, `session.workspace_label`, `session.inbound`.

It does **not** advertise `message.watch.push`: it polls, so the watch's `ready` event says `"mode": "polling"` and a
consumer expects two poll intervals of latency rather than five seconds (C-33, C-35).

**The one deviation from the 4.4.1 example** is `lease`: `{default_seconds: 90, min_seconds: 1, max_seconds: 600}`.
The example's `min_seconds` is 30; plan P1-5 requires 1 here so the slow lease-expiry cases (C-14, C-19b) take seconds
instead of half a minute. This is not a protocol deviation — 4.4.1 says `lease` *is* "the range of `lease_seconds` an
adapter accepts", the harness and the suite read it from `describe`, and `protocol.Lease.CheckSeconds` applies the
receiver's own bounds. The request shapes' `Validate` requires only a positive integer.

## Errors

The 4.6 taxonomy, with this adapter's fixed messages:

| Situation | Code | Exit |
| --- | --- | --- |
| no `profile.json` | `config` | 11 |
| no `credential.json` | `unauthenticated` | 4 |
| credential present, no team bound | `config` | 11 |
| not an active member of the team, or the team does not exist | `unauthorized` | 5 |
| unknown, foreign or not-owned session or message id | `not_found` | 6 |
| a rejected join secret | `unauthorized` | 5 |

The last three are **one fixed message with no `details`**, so a missing team, a revoked membership and a team that
never existed are byte-identical, and so are an unknown id, a foreign id and one the caller does not own (4.5.6,
4.5.7). That is not politeness: a distinguishable error is an existence oracle. A revoked member's sessions are outside the team by the same rule that hides them from `session list` (C-08), so they cannot be messaged either: `message send` answers that same uniform `not_found` for a recipient whose owner is no longer an active member, byte-identical to an id that never existed.

## The mutants

Four deliberately broken builds live in this package behind build tags, so the normal build contains no mutant code
path at all — nothing for a stray variable or a repository `env` block to switch on. `mutants_test.go` here proves each
mutation is live and that the normal build is not mutated; P1-6's conformance suite asserts that each fails **exactly**
its cases and no other.

| Tag | Real twin | What it breaks | Expected failures |
| --- | --- | --- | --- |
| `mutant_noack` | `store_ack.go` | the ack routine reports `acked` and moves nothing | C-29b, C-30, C-36, C-41 |
| `mutant_teamleak` | `store_list.go` | `session list` walks every team, not the profile's | C-12, C-26 |
| `mutant_trustsender` | `store_send.go` | the forbidden members of 4.4.6 are accepted and a forged `sender.session_id` is trusted; the sender-ownership check is skipped | C-23, C-24 |
| `mutant_caporder` | `store_caps.go` | the two unacknowledged caps of 4.5.12 are checked in the wrong order: the recipient-wide `max_unacked_per_recipient` before the per-pair `max_unacked_per_sender_recipient` | C-28 |

The expected failures are the sets P1-6's `internal/conformance/mutants_test.go` asserts EXACTLY. `mutant_noack`'s set is
larger than the plan's table said (C-30, C-36): C-29b's hop chain cannot avoid acks and C-41's restart check is a
stdin-ack check. `mutant_caporder` exists because the ORDER of the two caps was the decisive untested defect in both
P1-5 and P1-6 — every other property of the caps (codes, reasons, `retry_after_ms`, thresholds) survives the swap, and
only a case that puts BOTH caps at their limit at once can tell the two orders apart.

```sh
go build -tags mutant_caporder -o /tmp/mutant ./cmd/brigade-adapter-fs
```

`make lint` runs golangci-lint once per tag over this package, on top of the untagged run, so BOTH twins of every pair
are linted. The tag list lives in the Makefile's `mutant_tags` and nowhere else: `.golangci.yml` deliberately sets no
`run.build-tags`, because setting all the tags at once excludes every `//go:build !mutant_*` twin — the real `ack`,
`list`, `send` and cap implementations — from the build the linter sees.
