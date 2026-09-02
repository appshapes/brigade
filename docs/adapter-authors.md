# Writing a Brigade adapter

> **Status: skeleton.** This page is filled in by plan task P1-7, after the fs adapter (P1-5) and the conformance
> suite (P1-6) exist to be pointed at. The sections that are complete now are the ones that only depend on the
> frozen protocol: the command table, the exit codes and the shape of the conformance command line. Everything
> marked *P1-7* is a heading with intent, not content.

## Purpose

A Brigade adapter is an executable that speaks the Brigade Adapter Protocol v1 (BAP/1, `docs/protocol-v1.md`) on
its argv, stdin and stdout, and talks to some backend — Supabase, a directory on disk, an object store, your own
service — on the other side. The harness (the Claude Code plugin) never knows which. If your adapter passes
`brigade-conformance`, Brigade can use it: set the plugin option `adapter_command` to its absolute path or to a JSON
array with fixed arguments, and every `brigade` command and hook will spawn it instead of the bundled Supabase
adapter.

This page tells you what to build and how to prove it. The protocol document is the authority on every wire detail;
this page repeats only what you need at hand while building.

## The command surface

`<group> <verb> [flags]`, one process per command, no shell (protocol 4.1). The **frozen core** is what the harness
calls; the **conventions** are the words every adapter uses for team and profile management so that humans and
scripts learn them once.

| Command | stdin | stdout `result` | Status |
| --- | --- | --- | --- |
| `describe` | none | `DescribeResult` | core; must work offline, without credentials, without touching the network |
| `session register` | `SessionRegistration` | `SessionRecord` + `resumed`, `lease_seconds`, `server_time` | core |
| `session heartbeat --session <id>` | `HeartbeatRequest` | `{session_id, state, lease_until, server_time}` | core |
| `session list [--session <id>] [--include-offline]` | none | `{team_ref, team_name, server_time, sessions, truncated}` | core |
| `session close --session <id>` | none | `{session_id, state: "offline"}` | core; idempotent |
| `message send` | `SendRequest` | `SendResponse` | core |
| `message receive --session <id> [--limit <n>]` | none | `{messages}` | core; never acknowledges |
| `message watch --session <id>` | NDJSON commands (capability) or nothing | NDJSON events | core |
| `message ack --session <id>` | `{message_ids}` | `{acked, unknown}` | core; idempotent |
| `team create [--name] [--label] [--prompt] [--secret-file]` | `{team_name, human_label?}` | `{team_ref, team_name, join_secret, principal_ref}` | convention, capability `team.create` |
| `team join [--prompt] [--label]` | `{join_secret, human_label?, backend?}` | `{team_ref, team_name, principal_ref, rejoined}` | convention, capability `team.join` |
| `team leave` | none | `{team_ref, principal_ref, left: true}` | convention, capability `team.join`; idempotent |
| `team members` | none | `{team_ref, team_name, server_time, members}` | convention, capability `team.roster` |
| `team rotate-secret` / `team revoke-member` / `team transfer` | adapter-defined | adapter-defined | convention, capability `team.admin` (Phase 5) |
| `profile init` / `profile status` / `profile reset` / `profile revoke-credentials` | adapter-specific | adapter-specific, never a secret | convention, adapter-defined |

Core-command flags are exactly `--profile`, `--session`, `--include-offline`, `--limit` and `--log-level`; an unknown
flag on a core command is `usage`. Conventions may add flags, but no flag ever carries a secret, a message body or a
user-authored message; `--join-secret` is refused everywhere. A command that takes no input must not read stdin.

## Exit codes

The exit status is a function of `error.code` and nothing else (protocol 4.6). Your adapter maps every failure to one
of these twelve; nothing else is ever exited from your own code.

| Exit | `error.code` | `retryable` | When |
| --- | --- | --- | --- |
| 0 | — | | success |
| 1 | `internal` | `false` | a bug in the adapter |
| 2 | `usage` | `false` | bad argv; a secret on argv; stdin is a terminal |
| 3 | `invalid_input` | `false` | stdin missing, oversize or malformed; a member over its cap; a caller-supplied sender member; a malformed join secret |
| 4 | `unauthenticated` | `false` | no usable local credential |
| 5 | `unauthorized` | `false` | not an active member of the team; watch join refused; join secret rejected |
| 6 | `not_found` | `false` | unknown, foreign or not-owned session or message id — one uniform answer |
| 7 | `conflict` | `false` | idempotency key reused with a different payload; session closed; resume of a live session; profile already bound |
| 8 | `rate_limited` | `true` | a send budget or inbox cap tripped; `retry_after_ms` present |
| 9 | `unavailable` | `true` | backend unreachable, 5xx, timeout, paused project |
| 10 | `protocol_mismatch` | `false` | protocol majors differ |
| 11 | `config` | `false` | profile missing, invalid or world-readable; backend not configured; no team bound |
| 12 | `loop_detected` | `false` | `hop_count` cap reached |

`retryable` is sent on every failing envelope; only `rate_limited` and `unavailable` are ever `true`. On a validation
failure put the offending member's wire name in `details.field` (protocol 4.3.1).

## Run the conformance suite

The suite is the definition of "works with Brigade". It runs your adapter as three principals in two teams, using
protocol commands only, and checks every rule of the protocol document that a black-box test can see
(`C-01`..`C-43` plus `C-03b`, `C-19b`, `C-29b`; plan 9.2).

> **Today the binary is a stub.** `cmd/brigade-conformance` builds, accepts the full command line below and reports
> zero cases selected; the suite library (`internal/conformance`) lands with P1-6. Zero cases is reported as zero
> cases, never as a passing run.

```text
brigade-conformance [flags] --adapter <cmd> [-- <fixed args>...]
  --adapter <cmd>          executable to test (PATH lookup for a bare name); everything after -- is prepended to every invocation
  --env K=V                extra environment for every adapter process (repeatable)
  --shared-env NAME        export NAME=<run temp dir>/shared to every principal (the fs adapter's BRIGADE_FS_ROOT)
  --setup <cmd>            run once per principal with BRIGADE_CONFIG_DIR set, before any protocol command
  --rebind <cmd>           command that rebinds a profile to a team_ref given on stdin {"team_ref":…} (C-26, C-43)
  --tags core,cap:team.create,slow   run only cases carrying one of these tags (default: all applicable)
  --only C-20,C-21  --skip C-14      case selection
  --slow                   include slow cases (lease expiry at lease.min_seconds)
  --timeout 20s            per-command timeout (registration and watch cases use their own)
  --keep-temp              keep the run directory for inspection
  --json                   machine-readable report on stdout (human table on stderr)
  -v                       show every command, stdin, stdout, stderr
exit 0: all selected cases passed (skips allowed)   1: at least one failure   2: usage   3: launcher error
```

The environment of every adapter process is built from scratch — `PATH`, `HOME`, `TMPDIR`, `BRIGADE_CONFIG_DIR`,
`BRIGADE_STATE_DIR`, `BRIGADE_PROFILE=default`, `BRIGADE_LOG_LEVEL=debug`, the `--env` pairs and the shared variable —
which is itself a test: an adapter that needs anything else fails `C-01` with a clear message. Cases tagged
`cap:<capability>` run only when your `describe` advertises the capability and are reported as skipped otherwise;
an adapter without `team.create`/`team.join` is provisioned through `--setup`, which must leave three joined profiles
(two in one team, one in another).

## Start here: `describe` and `session list` *(P1-7)*

## The environment your adapter runs in *(P1-7; the harness-side contract is added by P3-6)*

## The profile file and where state lives *(P1-7)*

## Logging and redaction *(P1-7)*

## Worked example: the fs adapter *(P1-7)*

## Example scripts *(P1-7: three txtar scripts from `cmd/brigade/testdata/script/`)*

## Wiring your adapter into the plugin (`adapter_command`) *(P1-7)*
