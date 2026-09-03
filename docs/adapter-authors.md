# Writing a Brigade adapter

> **Status.** The reference adapter (`bin/brigade-adapter-fs`) and the conformance suite
> (`bin/brigade-conformance`) exist and are green, so everything below is written against running code; the plugin
> manifest and hooks that read the `adapter_command` option arrive with Phase 3, so today an adapter is exercised
> through the suite and by hand, not from inside a live Claude Code session.

## Purpose

A Brigade adapter is an executable that speaks the Brigade Adapter Protocol v1 (BAP/1, `docs/protocol-v1.md`) on
its argv, stdin and stdout, and talks to some backend — Supabase, a directory on disk, an object store, your own
service — on the other side. The harness (the Claude Code plugin) never knows which. If your adapter passes
`brigade-conformance`, Brigade can use it: set the plugin option `adapter_command` to its absolute path or to a JSON
array with fixed arguments, and every `brigade` command and hook will spawn it instead of the bundled Supabase
adapter.

This page tells you what to build and how to prove it. The protocol document is the authority on every wire detail;
this page repeats only what you need at hand while building, and cites the section number wherever it paraphrases.
Where this page and `docs/protocol-v1.md` disagree, the protocol document is right and this page is a bug.

**Any language.** Everything below is argv, environment, stdin, stdout, exit status and files on disk. A POSIX shell
script with a shebang, a Python program, a Rust binary and a Go binary are all equally conformant. Go authors may
reuse `internal/protocol` (the wire types and their `Validate()`) and `internal/adapterkit` (bounded stdin reader,
XDG resolution, atomic 0600 writes, flock, the redacting logger) as a convenience; they are not part of the
contract, and no case in the suite checks that you used them.

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

Read that sentence per command, not per binary. `--profile` and `--log-level` are the two flags 4.1 says apply to
**every** command, conventions included, so `team create --profile bob` and `profile status --log-level debug` are
well-formed on any adapter. `--session`, `--include-offline` and `--limit` belong to the core commands the table
names them on, and nowhere else: `describe --include-offline` is an unknown flag *for `describe`* and therefore
`usage` (measured on `bin/brigade-adapter-fs`, exit 2). The extra flags in the `team` rows above are the conventions'
own (4.2), and a `team` or `profile` command is free to define more.

## Exit codes

The exit status is a function of `error.code` and nothing else (protocol 4.6). Your adapter maps every failure to one
of these twelve; nothing else is ever exited from your own code.

| Exit | `error.code` | `retryable` | When |
| --- | --- | --- | --- |
| 0 | — | | success |
| 1 | `internal` | `false` | a bug in the adapter |
| 2 | `usage` | `false` | bad argv: an unknown group, verb or flag, a missing required flag, a flag value out of range; a secret on argv |
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

**Not in that table: a terminal on stdin.** 4.6 records that the reference implementation answers `usage` when stdin
is a terminal on a command that reads a document, so a human who forgot to pipe input is told rather than left
hanging — and calls it *a courtesy of `adapterkit.ReadInput`, not a protocol obligation*. No case exercises it,
several languages have no portable terminal test, and under the harness and the suite stdin is never a terminal.
Omit it without penalty.

## Run the conformance suite

The suite is the definition of "works with Brigade". It runs your adapter as three principals in two teams, using
protocol commands only, and checks every rule of the protocol document that a black-box test can see: 45 cases,
`C-01`..`C-43` plus `C-03b`, `C-19b` and `C-29b` (there is no C-09).

```text
$ bin/brigade-conformance -h
Usage: brigade-conformance [flags] --adapter <cmd> [-- <fixed args>...]

Flags:
  -adapter string
    	executable to test (PATH lookup for a bare name)
  -env value
    	extra environment for every adapter process, K=V (repeatable)
  -json
    	machine-readable report on stdout (human table on stderr)
  -keep-temp
    	keep the run directory for inspection
  -only string
    	run only these case ids (comma-separated)
  -rebind string
    	command that rebinds a profile to a team_ref given on stdin
  -setup string
    	command run once per principal before any protocol command
  -shared-env string
    	export NAME=<run temp dir>/shared to every principal
  -shuffle int
    	run the selected cases in the permutation this seed names (0: id order)
  -skip string
    	skip these case ids (comma-separated)
  -slow
    	include cases tagged slow
  -tags string
    	run only cases carrying one of these tags (core, cap:<capability>, slow)
  -timeout string
    	per-command timeout (default "20s")
  -v	show every command, environment, stdin, stdout and stderr

Exit: 0 all selected cases passed  1 a case failed  2 usage  3 launcher error
```

Single and double dashes are both accepted (`-adapter` and `--adapter` are the same flag). Everything after a bare
`--` is a list of fixed arguments prepended to every invocation, so an adapter that needs a leading configuration
flag is launched as `--adapter /abs/my-adapter -- --root /tmp/store`.

### The environment every adapter process gets

The suite builds each child's environment **from scratch** — it never passes its own. Exactly this, in this order:

| Variable | Value |
| --- | --- |
| `PATH`, `TMPDIR` | copied from the suite's own environment |
| `HOME` | `<run>/principals/<name>/home` |
| `BRIGADE_CONFIG_DIR` | `<run>/principals/<name>/config` |
| `BRIGADE_STATE_DIR` | `<run>/principals/<name>/state` |
| `BRIGADE_PROFILE` | `default` |
| `BRIGADE_LOG_LEVEL` | `debug` |
| the `--env NAME=value` pairs | verbatim, repeatable |
| `--shared-env NAME` | `NAME=<run>/shared` — one directory shared by every principal |

That table is the whole environment. The first six rows are what the suite computes; the last two are the ones
**you** added on the command line, and they — together with the fixed arguments after a bare `--`, which are argv
and not environment — are the only way to tell your adapter anything about its backend. Beyond those eight rows the
suite adds exactly one more variable, the test-only one named in the next sentence, and inherits nothing else from
its own environment. That minimality is itself a test: an adapter that needs a variable it did not ask for on the
command line fails C-01 with a clear message (4.1, Environment). Two cases add that one variable to
`describe`'s environment and nothing ever adds another — C-01 and C-07 run `describe` with `BRIGADE_TEST_OFFLINE=1`,
which an adapter with a network client is expected to turn into a dialer that fails
loudly, so "describe makes no network call" is actually asserted rather than assumed. An adapter that does not know
the variable simply ignores it.

`--shared-env` exists because the suite gives every principal its own config and state directory, and a
filesystem-backed adapter needs one *backend* all three principals can see: `--shared-env BRIGADE_FS_ROOT` is how
the fs adapter's three principals end up in one store. A network adapter usually needs `--env` instead (a URL, a
publishable key) and no shared directory at all.

**The variable's name is yours, and your adapter reads it as an ordinary environment variable.** `--shared-env
MYSTORE_ROOT` exports `MYSTORE_ROOT=<run>/shared` into the environment of every principal the run creates — the
three fixture principals and every throwaway scratch principal alike — and nothing in the suite knows what the name
means. The directory itself is not created for you: the suite only names it, and C-01 fails your adapter if
`describe` is the command that brings it into existence. This is the one place a `BRIGADE_<ADAPTER>_*`-shaped variable is legitimate: it is how a
store-backed adapter is *tested*, even though no live session will ever set it (see *The environment your adapter
runs in*). If you would rather not read the environment at all, the other route is fixed arguments — `--adapter
/abs/my-adapter -- --root /abs/store` — but then you create that directory yourself, it lives outside the run
directory, `--keep-temp` will not capture it, and the end-of-run secret scan will not look inside it.

**What you put in that directory is entirely yours.** The protocol stops at the wire (4.8): it does not say how you
represent a team, a membership, a session or a message, and no case looks inside your store. Invent the layout you
want — the fs adapter's is one worked answer, described under *Worked example* below, and a table in a database is
just as conformant. Two rules do reach in: nothing under it may contain a join secret (C-05 walks the whole run
directory at the end), and whatever names the location — your `--shared-env` variable, your `--env` variable, your
fixed argument — is a path you should insist be **absolute**, refusing a relative one with `config` (exit 11)
exactly as the fs adapter refuses a relative `--root` or `$BRIGADE_FS_ROOT` and as 4.1 requires for
`BRIGADE_CONFIG_DIR` and `BRIGADE_STATE_DIR`. Hooks run with the working directory set to the user's project, so a
relative store root is how a team's messages end up committed to somebody's repository.

**When nothing names a location, fall back to `BRIGADE_STATE_DIR` — never to the working directory.** A human will
run your adapter with no `--root` and no variable set, and the protocol has no opinion (4.8), so pick one of two
answers and write it in your README. Either derive a default from `BRIGADE_STATE_DIR`, which 4.1 already guarantees
you and which is already absolute — the fs adapter uses `${BRIGADE_STATE_DIR}/fs-adapter`, and a `team create` with
`BRIGADE_FS_ROOT` unset and no `--root` succeeds against it, measured this session — or refuse with `config`
(exit 11) and say which variable or argument you wanted. What you must not do is default to the current directory or
to anything relative to it: under a hook the current directory is the user's project.

**Validate the location lazily: `describe` must never look at it.** The absolute-path check, the "does this
directory exist" question and the creation of the store all belong on the first command that actually touches the
backend, because `describe` may not fail on state and may not create anything (C-01, section 5). Measured on the fs
adapter this session: `describe` exits **0** with `BRIGADE_FS_ROOT` unset, with `BRIGADE_FS_ROOT=rel` and with a
relative leading `--root rel`, while `team create` under the same relative values is `config`, exit 11,
`details.reason = "relative_path"` (message `"BRIGADE_FS_ROOT must be an absolute path"` / `"--root must be an
absolute path"`). An adapter that resolves — or worse, creates — its root in a shared start-up path every command
runs through fails C-01 the moment `describe` brings the directory into existence, and turns a misconfigured root
into a `describe` failure on a command whose whole job is to answer from local files.

There is no third route. The suite tells your adapter where to put its store **only** through `--shared-env`, `--env`
or fixed arguments, and the `<run>/principals/<name>/…` layout in the table above is a fact about today's launcher,
not part of the contract: do not derive a store location from it. An adapter that guesses at the layout is testing
the suite, not the protocol.

### Provisioning: `--setup`, and when you need it

The suite's fixture is three principals — **A** and **B** in team **T1**, **C** in team **T2** — with one registered
session each (`fixture-a`, `fixture-b`, `fixture-c`). It is built lazily, by the first selected case that asks for
it, in one of two ways:

- If your `describe` advertises **both** `team.create` and `team.join`, the suite provisions the fixture itself with
  `team create` (A), `team join` (B, with A's secret) and `team create` (C).
- Otherwise you **must** pass `--setup <cmd>`. It runs once per principal, in that principal's environment, argv
  split on whitespace with no shell, and must exit 0 leaving a joined profile behind — A and B in one team, C in
  another. The suite then reads each principal's identity from `describe`, which must answer
  `profile.state = "joined"`. It gets three times the per-command timeout, because a backend bootstrap can be slower
  than one protocol command.

With neither, the fixture cannot be built and the run stops with exit 3 and the message *"the adapter advertises
neither team.create and team.join, and no --setup was given: the fixture cannot be provisioned"*.

**What the suite sends on the self-provisioning route.** Both commands are driven by a **stdin document only**: the
`--name`, `--label` and `--prompt` flags of 4.2 exist for humans and the suite never uses them. Because the suite
never sends both, what happens when a human sends a flag *and* a document is yours to decide — accepting the flags
and ignoring them is conformant, and so is the fs adapter's answer, measured this session: `--name` **wins** over the
document's `team_name`, and `--name` alone makes the document optional (`team create --name flag-only` with no stdin
succeeds). Pick one, document it in your README, and keep the two protocol rules that do bind: no flag ever carries a
secret, a body or a user-authored message (4.1), and `--prompt` without a TTY is `usage` (4.4.10). `team create`, for A
(and again for C with `t2-<run id>` and `carol@example.com`):

```json
{"team_name": "t1-<run id>", "human_label": "alice@example.com"}
```

Its result must carry all four of `team_ref`, `team_name`, `join_secret` and `principal_ref` (4.4.10). The suite
keeps that secret and hands it to B:

```json
{"join_secret": "<A's join_secret>", "human_label": "bob@example.com"}
```

`team join`'s result must carry all four of `team_ref`, `team_name`, `principal_ref` and `rejoined` (4.4.10).

**The join secret's format is the protocol's, not yours.** 4.4.10 fixes it: `brg1.<team_ref>.<secret>` — the fixed,
case-sensitive prefix `brg1.`, then the team's own `team_ref`, then a `.`, then the secret proper. Because 4.8 leaves
the shape of `team_ref` open, the `team_ref` is everything between the prefix and the **last** `.`; neither component
may be empty or contain whitespace, control or format characters. Only the secret half is yours to design (the fs
adapter uses 32 hex characters). Two consequences worth writing down before you start: a secret that does not
**parse** is `invalid_input` (exit 3) with `details.field = "join_secret"` and a message containing no part of the
input, while a secret that parses but is **rejected** — wrong secret, unknown team, banned principal — is
`unauthorized` (exit 5) with byte-identical text for all three (4.5.7, 4.6, C-04); and the launcher's scanner knows
the shape, so anything matching `brg1.<x>.<y>` outside `team create`'s own stdout is a C-05 failure wherever it
appears.

**`rejoined`.** 4.4.10: `rejoined: true` means the same membership was re-activated with the same `principal_ref`,
`false` means a new membership was created. Detect it from your own store, not from the request: the profile already
holds a `principal_ref` (from an earlier `team create` or `team join` — `team leave` unbinds the profile and keeps
the credential, 4.2), and that principal already has a membership record in the team the secret names, active or
revoked. Re-activate that record and answer `true`; otherwise create one and answer `false`. A rejoin whose document
omits `human_label` keeps the label the membership already carries — measured on the fs adapter, `describe` still
reports `alice@example.com` after a `team leave` and a label-less `team join` — which is what keeps C-12 satisfied;
clearing it instead would leave you with session records that have no label to copy. C-08 is the case: leave, join
again with the same secret, and the `principal_ref` must be the one from before. A `team join` on a profile
bound to a **different** team is `conflict` (exit 7, `details.reason = "profile_bound"`), checked locally before any
network call.

**What happens next is the same on both routes.** The suite runs `describe` for each principal and requires
`profile.state = "joined"` with `team_ref`, `team_name` and `principal_ref` — that is where it learns each
principal's identity — then checks that A and B share a `team_ref` and that C's differs, and finally registers one
session per principal (`fixture-a`, `fixture-b`, `fixture-c`) with a `SessionRegistration` document on stdin, whose
shape is in *The commands the fixture needs* below.

**Carry `human_label` through.** The label arrives exactly once, on `team create` / `team join` (or from whatever
`--setup` does), and every session record the suite later lists must carry one (C-12). Store it on the profile when
you bind, and stamp it onto every session you record; nothing later in the protocol supplies it again.

**Never persist the join secret.** The launcher walks the entire run directory at the end of the run — every
principal's config and state directory and the `--shared-env` directory included — and a file containing a secret the
run knows, or anything shaped like `brg1.<x>.<y>`, is a C-05 failure. Keep a digest instead: the fs adapter stores
`join_secret_sha256` in `team.json` and nothing else.

**This matters even for a partial adapter.** Cases C-01, C-06, C-07 and the argv half of C-02 run on throwaway
"scratch" principals and need no fixture; C-02's stdin checks and C-05 use principal A, so selecting them builds the
fixture. Plan for provisioning before you plan for any single case.

`--rebind <cmd>` is the other operator hook. Two cases (C-26 and C-43) need a profile pointed at a team it is not a
member of, to prove there is no team-existence oracle. Without the flag the suite does it itself, by rewriting the
`team_ref` member of `<BRIGADE_CONFIG_DIR>/profiles/default/profile.json` and restoring the original bytes
afterwards — which is why a top-level `team_ref` in that file is worth having even if your adapter keeps its real
binding elsewhere (see *The profile file and where state lives*). If your profile is not a JSON file with that
member, pass `--rebind <cmd>`: it is run in the principal's environment with `{"team_ref": "…"}` on stdin and must
rebind the profile to that reference.

### Concurrency: what the suite actually does

Cases run **one at a time**, and within a case the suite spawns one request/response command and waits for it: for
the four cases of section 11, and in fact for every case that does not start a watch, no two of your processes are
ever alive at once. That is a fact about the suite, not a licence — the watch cases break it deliberately, and there
are ten of them (C-33..C-41, plus C-08, which leaves a watch running on a session while that same principal's
`team leave` revokes its membership from a second process and requires the watch to notice within two seconds).
`message watch` is a long-lived child that keeps reading your store while **other principals' processes** register,
send and acknowledge against it, and C-35 asserts that a message sent by another principal reaches the running watch
within five seconds.
A store-backed adapter therefore needs its writes to be safe against a concurrent reader before it can pass a full
run: write files atomically (a temp file in the same directory, `fsync`, rename) so a reader never sees a
half-written one, and take a lock around any multi-file mutation. The fs adapter's answer — one `flock` sidecar held
for the whole of every command but `describe`, and a watch that takes and releases it once per poll and never across
the sleep — is under *Worked example: the fs adapter* below.

You can defer all of that while you are building `describe` and `session list`. You cannot defer it and claim
conformance.

### Selection, tags and what a SKIP means

Every case carries the tag `core`. A case that needs a capability also carries `cap:<capability>`; a case that has to
wait out a lease carries `slow`. `--tags` is any-of; `--only` and `--skip` take comma-separated case ids,
case-insensitively.

- A case whose `cap:` tag names a capability your `describe` does not advertise is reported **SKIP** with the reason
  *"capability not advertised"*. That is not a pass and not a failure: it is a statement that you told the suite you
  do not implement that, so it did not look.
- A `slow` case without `--slow` is reported **SKIP** too. `--tags slow` selects it and still skips it; only `--slow`
  runs it.
- A case can also skip **at run time**, from inside its own body, when the fixture cannot give it what it needs — and
  two of those carry no `cap:` tag at all. C-28 and C-40 join extra principals into T1 with the fixture's join
  secret (the frozen per-principal send budget is too small for them to run on A), and the fixture only *has* a
  secret when it provisioned itself with `team create`. **So on the `--setup` route both are SKIP — *"needs
  team.join to provision an extra principal"* — whatever your `describe` advertises**, and the run still exits 0.
  Measured this session against the fs adapter, which advertises both team capabilities: `--setup <script> --only
  C-07,C-28,C-40` reports "1 passed, 0 failed, 2 skipped". C-04, C-07 and C-08 have their own fallbacks and keep
  running as long as `team.create` is advertised. Count all of it before you pick a provisioning route: `--setup`
  with neither team capability costs you C-03, C-03b, C-04 and C-08 to the `cap:` tags **and** C-28 and C-40 to
  this rule — six of the 45, on a run that still exits 0.
- A selection matching **no** case is a usage error (exit 2), never a pass. `--tags cap:nosuch` printing
  "0 passed, 0 failed, 0 skipped" with exit 0 would read exactly like a clean run, so the suite refuses before it
  launches anything. An unknown id in `--only`/`--skip` is a usage error for the same reason.

`--shuffle <seed>` runs the same selection in the permutation that seed names instead of in id order. The cases share
a fixture, a store and your rate budgets, so id order is one order out of many, and a case that quietly depends on
running after another one would pass in id order forever. A non-zero seed is printed on the run's header line and
carried in the JSON report, and `results` is in execution order, so a shuffled failure replays with the same seed.

Exit statuses: **0** every selected case passed (skips allowed), **1** at least one case failed, **2** a usage error
(bad flag, unknown case id, empty selection), **3** a launcher error (adapter not found, `describe` not ok or
unparseable or a protocol major other than `1`, `--setup` failed, the fixture could not be built). `-h` prints the
usage above on stdout and exits 0.

### A real run

```console
$ bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter bin/brigade-adapter-fs
brigade-conformance: brigade-adapter-fs 0.0.0-dev (bin/brigade-adapter-fs), protocol 1, 45 cases selected.
C-01   PASS   0.01s  4.2 describe offline
C-02   PASS   0.24s  4.6 usage and invalid_input
C-03   PASS   0.06s  4.2 team create
C-03b  PASS   0.06s  4.4.10 profile_bound
C-04   PASS   0.16s  4.2 team join
C-05   PASS   0.01s  4.5.14 no secret on argv or in output
               note: every spawn's stderr and the run directory are scanned for known secrets by the launcher; a hit anywhere in the run is reported as C-05
C-06   PASS   0.01s  4.6 unbound profile
…
C-14   SKIP   0.00s  4.5.8 lease expiry — slow case; run with --slow
…
C-43   PASS   0.05s  4.2 team members
44 passed, 0 failed, 1 skipped in 20.36s.
```

(The `…` lines are elided here; the run prints one line per case.) The human table always goes to **stderr**, in
every mode. With `--json` the machine-readable report goes to **stdout** and nothing else does, so
`brigade-conformance --json … > report.json` gives you a clean document — with one caveat worth knowing before
you wire this into CI: a launcher error raised *before* the first case (adapter not found, `describe` not ok
or not one valid envelope) exits 3 with **empty stdout** and the reason on stderr, so a consumer must handle
an empty document rather than assume exit 3 still produces one:

```console
$ bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter bin/brigade-adapter-fs --json --only C-01,C-12,C-18 2>/dev/null
{
  "adapter": {
    "name": "brigade-adapter-fs",
    "version": "0.0.0-dev",
    "command": "bin/brigade-adapter-fs"
  },
  "protocol_version": "1",
  "capabilities": [
    "team.create",
    "team.join",
    "team.roster",
    "message.receive",
    "message.watch.stdin_commands",
    "session.description",
    "session.resume",
    "session.workspace_label",
    "session.inbound"
  ],
  "results": [
    {
      "id": "C-01",
      "rule": "4.2 describe offline",
      "status": "pass",
      "duration_ms": 10,
      "reason": ""
    },
    {
      "id": "C-12",
      "rule": "4.4 session list",
      "status": "pass",
      "duration_ms": 274,
      "reason": ""
    },
    {
      "id": "C-18",
      "rule": "4.5.13 describe limits and capabilities",
      "status": "pass",
      "duration_ms": 3,
      "reason": ""
    }
  ],
  "summary": {
    "pass": 3,
    "fail": 0,
    "skip": 0
  },
  "duration_ms": 297
}
```

### Reading a failure

A failure line is `<id>  FAIL  <duration>  <rule> — <reasons joined with "; ">`. Each reason names the principal and
the command that produced it and cites the protocol section. This is a real run of a deliberately broken build of the
fs adapter whose `session list` walks every team instead of the profile's (`$MUT` is an absolute path in a scratch
directory; the version string is whatever `go build` derives from your own checkout, so yours will differ):

```console
$ go build -tags mutant_teamleak -o "$MUT" ./cmd/brigade-adapter-fs
$ bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter "$MUT" --only C-12,C-26
brigade-conformance: brigade-adapter-fs v0.0.0-20260902055207-f96143c6dd66+dirty ($MUT), protocol 1, 2 cases selected.
C-12   FAIL   0.28s  4.4 session list — a session list: session b0651690d32251ffdcc66b9b1ce267c8 belongs to a principal outside team T1 (4.5.6); a session list: C's fixture session (team T2) is listed (4.5.6, C-12); b session list: session b0651690d32251ffdcc66b9b1ce267c8 belongs to a principal outside team T1 (4.5.6); b session list: C's fixture session (team T2) is listed (4.5.6, C-12)
C-26   FAIL   0.02s  4.5.6 team isolation — C session list: T1 session ab5a7bb9dc244797cc31a5a8c8146d37 is listed (4.5.6, C-26); C session list: T1 session c5d5801da37c4c36e4f972d6cd20c7e8 is listed (4.5.6, C-26); C session list: T1 session dbb52eb58a5c07cfae2be52b85b84c80 is listed (4.5.6, C-26)
0 passed, 2 failed, 0 skipped in 0.63s.
```

A case records every violation it finds rather than stopping at the first, so one line can carry several reasons. When
a line is not enough, re-run that one case with `-v`, which prints every spawn — argv, the full environment, stdin,
stdout, stderr, exit status and duration — with any join secret the run knows redacted, and add `--keep-temp` to keep
the run directory (its path is printed on stderr) so you can look at the profile files your adapter wrote.

### Running it from the Makefile

```console
make conformance adapter=/abs/path/to/your-adapter args="--shared-env BRIGADE_FS_ROOT -v --only C-01"
```

`adapter=` is passed to `--adapter` and `args=` is appended verbatim, so fixed arguments go in it too
(`args="-- --root /tmp/store"`). `make test` runs the fs adapter's own full pass as part of the Docker-free gate.

### Timing

The whole fs run measured **20.4 s** wall clock on a developer laptop, and **26.5 s** with `--slow` (45 cases, none
skipped); `--slow` runs the one `slow` case, C-14, and C-19b's lease-expiry arm, both of which sleep out a real
lease. Most of the fs run is the handful of cases that sleep on purpose — redelivery, the rate-limit windows, the
watch's stdin commands, the paged catch-up and the two hop chains, in that order of cost (measured at 5.3 s, 3.3 s,
2.4 s, 2.0 s, 1.4 s and 1.3 s of a 20.5 s run). A network adapter is slower — every command is a round trip — so
budget minutes rather than seconds, and note that the per-command timeout (`--timeout`, 20 s by default) applies to
each spawn, not to the run.

`--slow` matters more than its cost suggests: the slow cases are the ones that prove your `state` is computed from
`lease_until` at read time rather than written once. Run it before you claim conformance.

### What a green run does not prove

The suite is black-box and time-bounded, so four normative statements of the protocol's Appendix B are outside it. A
green run says nothing about them, and you have to test them yourself:

| | Statement | Why the suite cannot see it |
| --- | --- | --- |
| B-3 | Every consumer presents `human_label` as unverified | a consumer-side rule; the adapter never displays anything |
| B-4 | A receiver ignores a `status.state` it does not know | a receiver-side rule; the suite is the receiver only in its own code |
| B-9 | Idempotency keys are retained at least as long as the message | needs retention-scale time; write a persistence test of your own |
| B-10 | An unacknowledged message is retained at least `retention.unacked_message_seconds` | 7 days by default; write a sweep test of your own |

Two more limits are worth knowing: C-28 exercises only the per-*minute* send budgets (the per-hour ones are
normative but would need 200 and 600 sends), and C-29b cannot wait out `implicit_reply_window_seconds` (600 s), so
the "a fresh message after the window has `hop_count` 0" arm is unasserted. Both are reported as notes on the passing
case.

## Start here: `describe` and `session list`

These two commands are the whole protocol in miniature: argv parsing, the environment, local state, the envelope, the
error taxonomy and one non-trivial result shape. Build them first, pass `C-01`, `C-02`, `C-05` and `C-06`, and the
rest is more of the same.

Everything in this section is the contract, and none of it is optional — but the four cases are not reachable with
`describe` and `session list` alone, and this section says so rather than making you find out. Two more things are
needed, and both are here: a `session register` that **succeeds**, because building the fixture registers one session
per principal (section 10), and a `message send` and `message receive` that are **recognised** and refused for the
right reason, because C-06 calls them on an unbound profile (section 11). Provisioning — `--shared-env`, `--env` or
`--setup` — is needed too; it is described under *Run the conformance suite* above and repeated in the command line
of section 11. Nothing else outside these two commands is required **to pass the four** — but "enough to pass the
four" is not "conformant". Your `describe` will advertise two things this build does not yet do, and both are
deliberate and bounded: `message.receive`, which 4.7 requires of every v1 adapter whether or not `message receive`
works yet, and — on the self-provisioning route — `team.create` and `team.join`, which owe `team leave` as well
(4.7, and the table in section 6, where both debts and their limits are written down). Take the `--setup` route and
advertise neither team capability if you would rather owe only the first — at the cost of six cases, listed under
*Selection, tags and what a SKIP means*.

### 1. Argv

Your executable is spawned as an argument array, never through a shell:

```text
<your-adapter> [<fixed args from adapter_command>…] <group> <verb> [flags]
```

`describe` is the one command with no verb. The groups are `describe`, `team`, `session`, `message` and `profile`
(4.1). On a **core** command (`describe`, `session *`, `message *`) the flag set is exactly five flags and no others:

| Flag | Applies to | Default |
| --- | --- | --- |
| `--profile <name>` | every command | `$BRIGADE_PROFILE`, else `default` |
| `--session <id>` | the session and message verbs that act on one session | required where the command table says so |
| `--include-offline` | `session list` | off |
| `--limit <n>` | `message receive` | 50, maximum 200 |
| `--log-level error\|warn\|info\|debug` | every command | `$BRIGADE_LOG_LEVEL` |

"Every command" in the first and last rows means every command of every group, conventions included; the other three
rows apply only to the commands named, and are unknown flags anywhere else.

Three things are `usage` (`error.code = "usage"`, **exit 2**) and C-02 and C-05 check all three:

1. an **unknown flag** on a core command — `describe --bogus`;
2. an **unknown group or verb** — `session frobnicate`;
3. **`--join-secret <value>` anywhere**, on every command of every adapter, whether or not your adapter has any
   notion of a join secret (4.1, 4.5.14). Its value must not appear on stdout or on stderr — not in an error message,
   not in a usage banner, not in a debug log line. C-05 plants `brg1.x.y` on the argv of `session list` and greps both
   streams.

Two more argv answers are `usage` too. 4.1 and 4.6 cover them as "bad argv" without naming them one at a time, and
the reference adapter answers this way (measured on `bin/brigade-adapter-fs`; the Supabase adapter is P2 and is not
written, so nothing on this page is measured on two adapters):

4. a **missing required flag** — `session heartbeat` or `session close` with no `--session` — is `usage`, exit 2;
5. a **flag value that is out of range or unparseable** — `--limit 0`, `--limit 500`, `--limit abc`,
   `--log-level shout` — is `usage`, exit 2. `--limit` is 1..200 (4.1: default 50, maximum 200). An invalid
   `BRIGADE_LOG_LEVEL` in the *environment* is `config` instead, not `usage`; see *Logging and redaction*;
6. a **flag that exists in the protocol but does not apply to the command you were given** — `describe
   --include-offline`, `describe --limit 5`, `session list --limit 5` — is an unknown flag *for that command*,
   `usage`, exit 2. Keep one allow-list per command, not one for the whole binary. (Measured on the fs adapter: both
   `describe --include-offline` and `describe --limit 5` are exit 2 with `code: "usage"`.)

**Spelling, position, repetition and empty values.** 4.1 fixes the grammar (`<group> <verb> [flags]`) and not the
spelling, so this is what the callers actually emit and what the reference adapter actually accepts. All of it was
measured on `bin/brigade-adapter-fs` this session.

- The harness and the suite **only ever** emit the two-dash form with the value as a **separate** argument, **after**
  the group and the verb: `session list --session <id> --include-offline`. Neither ever passes `--profile` at all —
  the profile arrives in `BRIGADE_PROFILE`. That form is the whole of what you are obliged to parse.
- The fs adapter additionally accepts `--flag=value`, the single-dash spelling (`-profile alice`) and a flag
  **before** the group (`--profile bob team join`, which `fs-team.txtar` below uses); Go's `flag` package gives it
  all three at no cost. Accepting more than the required form is harmless. An adapter with a leading fixed argument
  of its own — the fs adapter's `--root` — accepts that one *only* before the group, and treats it after the verb as
  an unknown flag (`usage`).
- A **repeated** flag takes its **last** occurrence: `describe --profile a --profile b` runs against profile `b`.
  Nothing in the suite or the harness repeats a flag, so refusing a repeat with `usage` is equally conformant; taking
  the **first** is the one answer to avoid, because it disagrees with the reference adapter and with the last-wins
  rule the environment already follows (section 2).
- A bare `--` is not part of the grammar and no caller sends one. The fs adapter takes it as an end-of-flags
  marker after the verb (`describe --` succeeds) and as an unknown leading flag before the group (`-- describe` is
  `usage`, exit 2, `"unknown flag before the command group"`). Copy that, or refuse it outright with `usage`; what it
  must never do is change which words are read as the group and the verb.
- An **empty flag value** follows the flag's requiredness, not its name. `session list --session ''` is an *optional*
  flag carrying no id, so it behaves exactly like an id that matches nothing: the team's list, `is_self` false on
  every record, exit 0. `session close --session ''` is a *required* flag with no value, which is a missing required
  flag and therefore `usage`, exit 2 (`"--session <id> is required for this command"`).
- `--profile ''` is neither of those: an **empty `--profile` is a bad profile name, not an absent flag**. The name is
  validated as a path component (section 2, and step 3 of the order below), and the empty string fails that check, so the
  answer is `config`, exit 11 — measured on the fs adapter, `details.reason = "invalid_profile_name"`, and the same
  answer whether or not `BRIGADE_PROFILE` is set. (In the *leading* position the fs adapter never gets that far:
  `brigade-adapter-fs --profile '' describe` is `usage`, exit 2, "a leading flag was given an empty value", because
  its own pre-group parser rejects an empty value before the profile exists. That position is the adapter's own
  extension, not the required form, so either answer is fine there.) Do not let an empty value fall through to
  `$BRIGADE_PROFILE` or to `default`: a flag the user typed is a name the user chose. Note the deliberate asymmetry
  with the environment — an **empty `BRIGADE_PROFILE` counts as unset** and resolves to `default` (section 2,
  measured), because an empty variable is how a shell says "not set" and a typed flag is not.
- An **empty argument vector**, a **positional argument after the verb** (`session list extra`), a **verb after
  `describe`** (`describe now`), an **unknown group** (`frobnicate`) and a **group with no verb** (`session`) are
  each `usage`, exit 2.

**Do these checks in this order.** The protocol does not order them and three cases depend on the outcome, so this
is the order to write:

1. scan the raw argument array for `--join-secret` — in any position, `--join-secret=<value>` included — and refuse
   with `usage` **before** you parse anything, log anything, resolve a profile or open a file. That is the only way
   to be certain the value reaches neither stream, and C-05 greps both;
2. parse the grammar: group, verb, flag names, flag values → `usage` (exit 2);
3. resolve and validate the profile name and the config and state directories → `config` (exit 11);
4. read the profile and the credential → the answer of section 9 (`describe` reports a state here instead of
   failing);
5. read stdin, on a command that takes a document → `invalid_input` (exit 3);
6. resolve and validate where your backend lives → `config` (exit 11). This step is last **and** conditional:
   `describe` never reaches it (section 5, and *What you put in that directory is entirely yours* above), and neither
   does any command that has already failed at step 3 or 4 — measured on the fs adapter, `session list` with no
   profile answers `config`/`profile_missing` whether the store root is absolute, relative or unset.

Step 2 before step 3 is what makes `describe --bogus` a `usage` on a machine that has no profile at all — C-02 runs
exactly that on a scratch principal. Step 4 before step 5 is the fs adapter's order and not a rule: the spec does not
order the profile check against the stdin check, which is precisely why C-02 sends its malformed documents to a
*joined* principal, so that either order passes.

`describe` takes no flags but `--profile` and `--log-level`; `session list` takes `--profile`, `--log-level`,
`--session <id>` and `--include-offline`. `--session` on `session list` is **optional** — it names the caller's own
session so the record can be marked `is_self`.

**An unrecognised `--session` value on `session list`.** The protocol does not say, and no case exercises it. Do
this: treat it as *no session matched* — return the list, `is_self` false on every record, exit 0, no error. That is
the fs adapter's behaviour, measured this session: `session list --session deadbeef` on a joined profile returns the
team's sessions with `is_self` false everywhere and exit 0. A uniform `not_found` (exit 6) is equally conformant if
you prefer it and document it. Do not invent a third answer, and do not make it depend on whether the id looks
well-formed.

### 2. Environment and precedence

You may read `BRIGADE_PROFILE`, `BRIGADE_CONFIG_DIR`, `BRIGADE_STATE_DIR`, `BRIGADE_LOG_LEVEL`, `HOME` and the XDG
variables, and you must not *require* anything else (4.1). Resolution, in order:

```text
profile name    --profile <name>   >  $BRIGADE_PROFILE  >  "default"
config dir      $BRIGADE_CONFIG_DIR  >  $XDG_CONFIG_HOME/brigade  >  $HOME/.config/brigade
state dir       $BRIGADE_STATE_DIR   >  $XDG_STATE_HOME/brigade   >  $HOME/.local/state/brigade
```

- **Absolute paths only.** A relative `BRIGADE_CONFIG_DIR` or `BRIGADE_STATE_DIR` is refused with `config`
  (exit 11) — the harness always computes an absolute value, so a relative one is a misconfigured shell and silently
  falling back to `$HOME` would honour a directory the user did not name. A relative `XDG_CONFIG_HOME` /
  `XDG_STATE_HOME` is **ignored** instead, because the XDG specification says so. Hooks run with the working
  directory set to the user's project, so a relative path is how a team's messages end up inside somebody's
  repository.
- If `HOME` is unset or relative and no `BRIGADE_*`/absolute `XDG_*` variable applies, that is `config` too.
- An empty value counts as unset. Where a variable appears twice in the environment, the **last** occurrence wins
  (this is what `os/exec` does, so an appended override beats an inherited value).
- The profile name becomes a path component, so validate it before you join it onto a directory: 1–64 characters
  from `[A-Za-z0-9._-]`, not starting with a dot. That excludes `/`, `.` and `..`, so a hostile `--profile` cannot
  traverse out of the profiles directory. A bad name is `config`, and the offending value is not echoed.

`describe` must answer even when every one of these is missing or points at an empty directory. There is no
"uninitialised" failure mode for `describe`.

### 3. stdin

Neither of these two commands takes an input document, so **neither may read stdin at all** (4.1; Appendix B-1). The
harness gives a no-input command the null device, but a human running your adapter by hand leaves the terminal or a
pipe attached, and a read would block forever. The suite asserts it directly: it runs the command with a pipe that is
never written to and never closed, and the process must still exit within the timeout.

**C-01 asserts it for `describe`; C-12 asserts it for `session list`** — and C-12 is not one of the four cases of
section 11, so the four-case run proves only the `describe` half. Implement both halves regardless: the rule is the
protocol's, not the suite's, and a full run does check it.

For the record, the rules for commands that *do* read stdin (you need them for C-02 and C-06): exactly one UTF-8 JSON
document terminated by EOF, a trailing newline allowed, at most 1 MiB. Over 1 MiB, empty, or not parseable as JSON is
`invalid_input` (exit 3). C-02 sends all three of those to `session register` on a joined principal.

Every one of those is exit 3 with `code: "invalid_input"`; only `details` tells them apart, and 4.3.1 fixes part of
each row. This is the fs adapter's whole answer, measured on `session register` this session; the last column says
how much of each row is the protocol's and how much is the adapter's own wording:

| stdin | `details` | Frozen? |
| --- | --- | --- |
| empty (0 bytes) | `{"field": "stdin", "reason": "required"}` | `field: "stdin"` is 4.3.1; the token is not |
| whitespace only (`"   \n"`) | `{"reason": "malformed_json"}`, no `field` | yes — it is a parse failure, not an empty stream (4.3.1) |
| not JSON (`{`) | `{"reason": "malformed_json"}`, no `field` | **yes**, and C-02 reads this one |
| two JSON documents | `{"reason": "malformed_json"}`, no `field` | yes — "exactly one document" (4.1) |
| over 1 MiB | `{"field": "stdin", "reason": "too_long", "unit": "bytes", "limit": "1048576"}` | `field: "stdin"` is 4.3.1; the rest is not |
| a JSON value that is not an object (`[]`, `"x"`, `42`) | `{"reason": "malformed_json"}`, no `field` | the code and the exit status are; the token is not |
| `null` | `{"reason": "required", "field": "harness"}` | no — see below |
| a member of the wrong JSON type (`"lease_seconds": "ninety"`) | `{"reason": "malformed_json"}`, no `field` | the code and the exit status are; naming the field instead is equally conformant |

**A top-level value that is not an object.** 4.1 says "one UTF-8 JSON document" and every request shape in 4.4 is an
object, so `[]`, `"x"` and `42` are `invalid_input` (exit 3) exactly like `{` — the fs adapter's decoder refuses them
with `malformed_json`. `null` is the odd one: it parses, and a decoder that maps it onto an empty request (Go's does)
then fails on the first missing required member instead — measured, `{"reason": "required", "field": "harness"}` on
`session register`. Both readings are exit 3 with `code: "invalid_input"`, no case looks, and the choice is yours;
what is not yours is answering anything other than exit 3. A member of the **wrong JSON type** lands in the same
place: a string where `lease_seconds` should be an integer is a decode failure (`malformed_json`), and reporting it
as a typed-member failure with `details.field = "lease_seconds"` is just as conformant. **Unknown members are
accepted and ignored** — that one is frozen (JSON convention 2, C-17), so never reject a document for carrying a
member you do not know; measured, a `session register` document with a `"bogus"` member registers normally.

Note the whitespace row: whitespace with no document in it is **malformed**, not empty. The distinction never
changes the code or the exit status, so either reading passes C-02 — but `malformed_json` is the answer that
matches the frozen rule, because 4.3.1 reserves `field: "stdin"` for stdin *itself* being the problem (empty, or over 1 MiB) and a
stream that has bytes in it has been read successfully and has simply failed to parse.

**You need not drain an oversize stream.** The reference reader consumes at most 1 MiB + 1 bytes and answers from
that, so a writer still pushing a large document may see its pipe close under it (`EPIPE`); that is expected, and
C-02's 1 MiB + 1 case passes with it. Do not buffer the whole stream to be polite about it: the cap exists so a
hostile stdin costs bounded memory.

**Every caller closes the pipe.** The harness and the suite both write the document and then close stdin, so reading
to EOF terminates. A defensive read deadline is allowed — the reference reader has none — but if you add one, keep it
well under the harness's 20 s per-command timeout, and make it fail with `invalid_input` and a `details.reason`
rather than hang or accept a partial document. Do not treat a deadline as "no input given": empty stdin and a
timed-out read are both `invalid_input`, so the distinction never reaches the wire anyway.

### 4. stdout: exactly one envelope

stdout carries **exactly one JSON object followed by exactly one `\n`, and nothing else** — no banner, no progress,
no second document, no trailing blank line, at any log level including `debug` (4.1). The suite applies this check to
every spawn of every case, and reports the violation against whichever case was running:

```json
{"ok": true, "protocol_version": "1", "result": { … }}
```

```json
{"ok": false, "protocol_version": "1", "error": {"code": "…", "message": "…", "retryable": false}}
```

| Member | Type | Presence | Meaning |
| --- | --- | --- | --- |
| `ok` | boolean | required | `true` with `result`, `false` with `error`; never both, never neither |
| `protocol_version` | string | required | your protocol major — `"1"` |
| `result` | object | exactly when `ok` is `true` | the command's result |
| `error` | object | exactly when `ok` is `false` | the error object below |
| `error.code` | string | required | one of the twelve codes of the exit-code table above |
| `error.message` | string | required | short, human-readable, safe to show to a model: no raw server text, no SQL, no tokens, and never the offending input value echoed back |
| `error.retryable` | boolean | required on **every** failing envelope | `false` for every code except `rate_limited` and `unavailable` |
| `error.retry_after_ms` | integer ≥ 0 | present for `rate_limited` | how long to wait |
| `error.details` | object of **strings** | optional | machine-readable detail; `details.field` names the offending wire member on a validation failure (4.3.1) |

**The words are yours; the code, the status and two whole envelopes are not.** Nothing in the protocol fixes
`error.message`, and `details` is a free-form object of strings (4.3.1) — so `"invalid arguments"`,
`details.reason = "unknown_flag"` and every other token on this page is the fs adapter's own naming, worth copying
only because a token costs nothing and a shared vocabulary helps a human reading two adapters' logs. Exactly four
things are fixed and each is checked: the twelve `error.code` values and the exit status each implies (4.6); the
presence of `retryable` and its value; `details.field` naming the offending wire member on a validation failure, with
`details.unit` when a cap was applied (4.3.1, JSON convention 5); and the two errors of section 8, whose *whole
envelope* must be byte-identical across the cases they cover. Everywhere else, invent the wording — but never echo
the offending input value back (`error.message` above), and never let `details` distinguish the cases section 8
requires to be indistinguishable.

`retryable` is a plain boolean and never `null`. It must be *present* — C-06 reads the raw JSON and fails the case
when the member is absent, not merely when it is wrong (Appendix B-2). The process exit status must match the code's
row in the exit-code table on every failing envelope; the suite asserts both halves together.

Two real failing envelopes from the fs adapter, byte for byte as they appear on stdout:

```json
{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"invalid arguments","retryable":false,"details":{"reason":"invalid_arguments"}}}
```

```json
{"ok":false,"protocol_version":"1","error":{"code":"config","message":"profile is not configured","retryable":false,"details":{"reason":"profile_missing"}}}
```

The first is `describe --bogus`, exit 2. The second is `session list` with no profile, exit 11. Note there is no
pretty-printing and no leading whitespace: one line, one newline.

### 5. `profile.state`: four values, computed from local files

`describe` reports which of four states the named profile is in, and it works this out from **local files only**:

| `profile.state` | Condition | The four team members |
| --- | --- | --- |
| `unconfigured` | no profile file for this profile name | absent |
| `unauthenticated` | a profile file exists but there is no usable credential | absent |
| `not_member` | a credential exists but no team is bound | absent |
| `joined` | a credential exists and a team is bound | `team_ref`, `team_name`, `principal_ref` required; `human_label` when the profile has one |

**The shapes of `team_ref` and `principal_ref` are yours**, exactly as `session_id`'s is: non-empty opaque strings
with no structure a consumer may rely on (4.8, JSON convention 8), so a UUID, a database key and the fs adapter's 16
random bytes as lowercase hex are all correct. Two constraints come from where they end up rather than from the
wire. The `team_ref` is carried inside the join secret, between `brg1.` and the **last** `.`, so it may not be empty
and may contain no whitespace, control or format character (4.4.10). And any identifier that becomes a path
component, a filename or a query fragment must be validated when it comes *back* in from a caller — the fs adapter
accepts `[A-Za-z0-9_-]{1,64}` and answers the uniform error of section 8 for anything else, which is both a
traversal defence and the reason a malformed id is never distinguishable from an unknown one.

The four members `profile.team_ref`, `profile.team_name`, `profile.principal_ref` and `profile.human_label` may
appear **only** when `state` is `joined`. Emitting any of the four in another state is invalid, and C-01 checks
exactly that on a fresh principal. Within the `joined` state three of them are required — `team_ref`, `team_name`,
`principal_ref`; the suite reads a principal's identity from exactly those three (4.4.1) — and the fourth,
`human_label`, is optional there, because a profile may have been bound without a label. "Optional" scopes to the
`joined` state only: in `unconfigured`, `unauthenticated` and `not_member` a `human_label` is as forbidden as the
other three. In practice you will have a label: the suite
supplies one at `team create` / `team join` time and C-12 requires one on every session record it lists.

Three absolutes, all asserted:

- **`describe` never touches the network.** C-01 and C-07 run it with `BRIGADE_TEST_OFFLINE=1` for adapters that can
  honour it, and C-01 additionally proves it by construction: the run's very first spawn is a `describe` on a fresh
  principal, and whatever it left behind is reported against C-01 whatever order the cases run in.
- **`describe` never fails because of state.** `unconfigured` is a successful answer, `ok: true`, exit 0. There is no
  state in which `describe` returns an error envelope. A *broken* environment or a *broken file* is a different
  thing and does fail, with `config` (exit 11): measured on the fs adapter, that is a relative `BRIGADE_CONFIG_DIR`
  (`details.reason = "relative_path"`), a `profile.json` that does not parse (`malformed_json`), a `version` the
  build does not know (`unsupported_version`) and a `profile.json` **or** a credential file that is group- or
  world-readable (`insecure_mode`, with the offending path in `details.path`). The 0600-in-0700 rule of *The profile
  file and where state lives* is enforced on read, on both files, and `describe` is not exempt from it. "No profile"
  is a state; "a profile I cannot read" is not.
- **`describe` creates nothing.** Not the config directory, not the state directory, not a profile file, not a backend
  root, not a lock file, not a log file. C-01 lists the principal's config and state directories after the call and
  fails on any entry, and separately fails if the `--shared-env` directory gained an entry. If your adapter
  ordinarily creates its store lazily, that has to happen on the first command that is *not* `describe`.

### 6. The full `DescribeResult`

Every member below is required. This is a real, complete `describe` result from `bin/brigade-adapter-fs` on a fresh
profile, reformatted for reading only (on the wire it is one line inside the envelope's `result`):

```json
{
  "protocol_version": "1",
  "adapter": {"name": "brigade-adapter-fs", "version": "0.0.0-dev"},
  "delivery": {"guarantee": "at_least_once", "ordering": "none", "ack_state": "injected"},
  "capabilities": ["team.create", "team.join", "team.roster", "message.receive",
                   "message.watch.stdin_commands", "session.description", "session.resume",
                   "session.workspace_label", "session.inbound"],
  "limits": {"max_body_bytes": 16384, "max_summary_chars": 200, "max_session_name_codepoints": 64,
             "max_team_name_codepoints": 64, "max_description_chars": 256, "max_human_label_chars": 128,
             "max_workspace_label_chars": 128, "max_idempotency_key_chars": 128,
             "send_rate": {"per_minute": 20, "per_hour": 200},
             "principal_send_rate": {"per_minute": 60, "per_hour": 600},
             "max_unacked_per_recipient": 60, "max_unacked_per_sender_recipient": 15,
             "max_hop_count": 32, "implicit_reply_window_seconds": 600},
  "lease": {"default_seconds": 90, "min_seconds": 1, "max_seconds": 600},
  "retention": {"unacked_message_seconds": 604800, "acked_message_seconds": 86400,
                "closed_session_seconds": 604800},
  "profile": {"name": "default", "state": "unconfigured"}
}
```

| Member | Value |
| --- | --- |
| `protocol_version` | `"1"` for a v1 adapter. The harness compares majors and refuses to operate on a mismatch; the suite stops with a launcher error on anything but `1` |
| `adapter.name`, `adapter.version` | free non-empty strings identifying your build |
| `delivery.guarantee` | `at_least_once` — the only v1 value |
| `delivery.ordering` | a free string; both bundled adapters answer `none` |
| `delivery.ack_state` | `injected` (the terminal adapter state in v1) or `processed` (reserved) |
| `capabilities` | strings from the registry below; **always present**, `[]` when empty; **must include `message.receive`** in v1 (C-18) |
| `limits`, `lease`, `retention` | every member below present and a **positive** number (C-18); additionally `lease.min_seconds ≤ lease.default_seconds ≤ lease.max_seconds` |
| `profile.name` | the profile this command ran against, after the `--profile` / `BRIGADE_PROFILE` / `default` resolution |
| `profile.state` | one of the four values above |

**The v1 constants.** These are the protocol's own values (4.4.1). They are the caps you enforce on input, and the
suite asserts that a value exactly *at* each cap is accepted, so you may not advertise or enforce a tighter one:

| Member | v1 value | Unit | Governs |
| --- | --- | --- | --- |
| `max_body_bytes` | 16384 | bytes | `body` |
| `max_summary_chars` | 200 | code points | `summary` |
| `max_session_name_codepoints` | 64 | code points | `session_name` everywhere, `sender.session_name` included |
| `max_team_name_codepoints` | 64 | code points | `team_name` everywhere |
| `max_description_chars` | 256 | code points | `session_description` |
| `max_human_label_chars` | 128 | code points | `human_label` everywhere |
| `max_workspace_label_chars` | 128 | code points | `workspace_label` |
| `max_idempotency_key_chars` | 128 | code points | `idempotency_key` |
| `send_rate.per_minute` / `.per_hour` | 20 / 200 | messages | one sender session's send budget |
| `principal_send_rate.per_minute` / `.per_hour` | 60 / 600 | messages | one principal's budget across all its sessions |
| `max_unacked_per_recipient` | 60 | messages | unacknowledged messages one recipient session may hold |
| `max_unacked_per_sender_recipient` | 15 | messages | unacknowledged messages from one sender session to one recipient |
| `max_hop_count` | 32 | hops | beyond this a send is `loop_detected` |
| `implicit_reply_window_seconds` | 600 | seconds | the window in which an unlabelled answer counts as a reply |
| `retention.unacked_message_seconds` | 604800 | seconds | 7 days: the floor you must retain an unacknowledged message for |
| `retention.acked_message_seconds` | 86400 | seconds | after this you *may* delete an acknowledged message |
| `retention.closed_session_seconds` | 604800 | seconds | after this you *may* delete a closed or expired session and its messages |

A `*_bytes` cap is measured in bytes of UTF-8; a `*_chars` or `*_codepoints` cap is measured in Unicode code points.
A 16 KiB body is not 16,384 characters, and a validation failure names which was applied in `details.unit`
(`bytes` or `codepoints`).

**`lease` is the one place your numbers may legitimately differ.** 4.4.1 defines `lease` as *the range of
`lease_seconds` an adapter accepts* — it is yours, published in `describe` so that programs read the number instead
of a document. The protocol's example is `{90, 30, 600}`; the fs adapter advertises `{"default_seconds": 90,
"min_seconds": 1, "max_seconds": 600}` so that the lease-expiry cases take seconds rather than half a minute. The
harness and the suite read your range from `describe` and stay inside it. Everything else in `limits` and
`retention` is the protocol's, not yours.

**The capabilities registry (v1).** Unknown strings are ignored, so the registry is additive; omitting one is a
first-class answer, not a failure.

| Capability | Unlocks |
| --- | --- |
| `team.create` | `team create` |
| `team.join` | `team join`, `team leave` |
| `team.roster` | `team members` |
| `team.admin` | `team rotate-secret` / `revoke-member` / `transfer` (Phase 5, creator only) |
| **`message.receive`** | `message receive` — **required in v1 and still advertised** (C-18) |
| `message.watch.push` | events arrive without polling: `ready.mode` is `push` and a message is expected within 5 s. Omit it and you **must** send `ready.mode = "polling"`. 4.4.9 relaxes live delivery to "within two poll intervals" for a polling adapter, but `describe` carries no poll-interval member, so **the suite applies the same 5 s deadline either way** (C-35; C-40's catch-up deadline is 10 s). Poll fast enough to clear both |
| `message.watch.stdin_commands` | NDJSON commands on `message watch` stdin |
| `session.description` | the `session_description` member |
| `session.resume` | the `resume` member of `session register` |
| `session.workspace_label` | the `workspace_label` member |
| `session.inbound` | `inbound` is stored and reported (without it you accept the member and ignore it) |
| `delivery.processed` | reserved; not implemented by any v1 consumer |

`message watch` itself is **not** optional — every adapter implements the NDJSON stream. What is optional is the
claim that events arrive without polling.

**A capability is a promise about every command in its "Unlocks" column, and a finished adapter advertises exactly
what it implements — no more and no less.** A capability you omit is a case the suite reports SKIP (see *Selection,
tags and what a SKIP means*) and a command the harness never issues; omitting one is a first-class answer (4.7).
`team.join` unlocks `team join` **and** `team leave`, so advertising it owes both — `team leave` is idempotent,
unbinds the profile and leaves the credential in place (4.2, 4.4.10), and C-08 exercises it in a full run. What your
adapter should do if a *human* runs a convention command whose capability you do not advertise is not specified and
no case observes it: answer `usage` (exit 2) — for your build that verb does not exist — and never answer success.

**Two advertisements are allowed to run ahead of your code, and only two.** "Exactly what you implement" is the rule
for the adapter you claim conformance for; it cannot be the rule for the adapter you are building this afternoon,
because the first `describe` you write has to answer before any other command exists. So the page is explicit about
the two exceptions, and about the debt each one creates:

| Advertisement | Why it may precede the code | The debt |
| --- | --- | --- |
| `message.receive` | 4.7 makes it **required in v1 and still advertised**, and C-18 fails a `describe` without it. There is no honest way to omit it, so an adapter whose `message receive` still answers `internal`/`not_implemented` advertises it anyway — that is the intended state of a build in progress, not a lie the suite will catch | `message receive` must actually work before you claim conformance |
| `team.create` + `team.join` | advertising both is what lets the suite provision its own fixture instead of making you write a `--setup` script (see *Provisioning*), and the four cases of section 11 need a fixture | `team create`, `team join` **and** `team leave` must all work before you claim conformance |

Nothing else may be advertised before it works: every other capability is optional, and omitting it costs you SKIPs,
not failures. Both debts are on the checklist at the end of this page, and the smaller-debt route is real — take
`--setup` and advertise neither team capability if you would rather owe only the first, at the cost of the six cases
named under *Selection, tags and what a SKIP means*.

**`team leave` on a profile that is not bound.** 4.4.10 says `left` is always `true` and the command is idempotent,
so a second `team leave` is a success, not a `conflict` and not a `not_found`. The fs adapter's answer, measured this
session: with a credential and no binding it still answers `ok` with `left: true`, taking `team_ref` from the team
the credential remembers (`last_team_ref` in `credential.json`); with **no profile at all** it answers the unbound
answer of section 9 instead (`config`, exit 11, `profile_missing`). Either half is a documented choice — the
protocol fixes only that a successful leave says `left: true` — but do not invent a third code for the repeat.

**A core verb you have not written yet.** The protocol has no answer for this, because an adapter that has not
implemented a core verb is not a conformant adapter (4.2) — but you will be in that state for a while, and there is
one answer that is safe and one that is not. Answer `internal` (exit 1) with a `details.reason` of your own naming
(`not_implemented` reads well), which 4.6 defines as the unclassified-failure code, and never `usage`: `usage` says
the verb does not exist, which is a lie that C-06 catches the moment it asks an unbound profile for `session
register`, `message send` or `message receive` (section 11). Never answer success. This applies only to a *bound*
profile; on an unbound one the answer of section 9 comes first, and that ordering is what C-06 requires. The
`profile` group is different: 4.2 makes `profile init` / `status` / `reset` / `revoke-credentials`
adapter-defined in both directions, so an adapter that defines none of them may treat the whole group as an unknown
verb (`usage`), and no case observes it either way.

### 7. `session list`

```text
<your-adapter> session list [--session <id>] [--include-offline]
```

Result:

```json
{
  "team_ref": "…",
  "team_name": "ops",
  "server_time": "2026-08-30T12:00:00Z",
  "sessions": [ … SessionRecord … ],
  "truncated": false
}
```

- `team_ref`, `team_name` — the profile's team. Not the caller's choice: 4.5.6 says every command operates within the
  profile's team, and `session list` returns **only** that team's sessions. Listing a session of another team is what
  C-12 and C-26 exist to catch, and it is the bug the reference adapter keeps a deliberately broken twin for
  (`mutant_teamleak`, a build tag in `internal/adapters/fs`) precisely so that those two cases are proved able to
  see it.
- `server_time` — **your** clock, RFC 3339. The harness never computes session state itself; it compares against this.
- `sessions` — a `SessionRecord` array, **always present**, `[]` when empty. A required array is emitted even when
  empty; only optional members are omitted (JSON convention 3). Never `null`.
- `truncated` — `true` when you cut the list short, `false` otherwise. Required either way.

**`SessionRecord`, member by member.** This is one real record from `bin/brigade-adapter-fs`, listed with
`session list --session <its own id>`. It is the same session the `session register` of section 10 created, from the
same run, so the two blocks agree member for member — `harness` and `harness_version` are the registration's, copied
through unchanged. The three optional, nullable members (`session_description`, `workspace_label` and, in the
register result, `resume`) were not in that registration, so they are **absent** rather than `null`:

```json
{
  "session_id": "b80e422f9f0fe9ab2767a7b5bf8bdd52",
  "session_name": "payments-api",
  "principal_ref": "bbed57255bcc1aaf17b41f8d59c444ef",
  "human_label": "alice@example.com",
  "state": "active",
  "activity": "busy",
  "inbound": "accept",
  "last_seen_at": "2026-09-02T06:42:26.622054Z",
  "lease_until": "2026-09-02T06:43:56.622054Z",
  "harness": "claude-code",
  "harness_version": "2.1.251",
  "created_at": "2026-09-02T06:42:26.622054Z",
  "is_self": true
}
```

| Member | Required | Notes |
| --- | --- | --- |
| `session_id` | yes | adapter-assigned, opaque, non-empty; no structure a consumer may rely on, and no shape the protocol fixes (4.8) — the fs adapter uses 16 random bytes as lowercase hex, a UUID or a database key is equally fine. If yours becomes a path component or a query fragment, validate it on the way back in (the fs adapter accepts `[A-Za-z0-9_-]{1,64}` and answers the uniform `not_found` for anything else) |
| `session_name` | yes | non-empty, ≤ `max_session_name_codepoints`; **unverified** display text |
| `session_description` | optional, nullable | ≤ `max_description_chars`; omit it when there is none |
| `principal_ref` | yes | the owning principal, opaque, non-empty |
| `human_label` | optional in the shape, in practice always present | ≤ `max_human_label_chars`; unverified display text. Optional because a profile may have been bound without a label — but the fixture gives every principal one at `team create` / `team join` time, so C-12 requires one on every session it lists. Copy the label from the profile onto every session record you write and the question never arises |
| `state` | yes | `active`, `idle` or `offline` — see below |
| `activity` | yes | `busy` or `idle`, as last reported by the session |
| `inbound` | yes | `accept`, `hold` or `refuse`, as last reported |
| `last_seen_at`, `lease_until`, `created_at` | yes | RFC 3339 timestamps, all three, all non-zero |
| `harness`, `harness_version` | optional | copied from the registration |
| `workspace_label` | optional, nullable | ≤ `max_workspace_label_chars` |
| `is_self` | yes | see below; `false` on every record unless `--session` named it |

**`state` is computed at read time, never stored** (4.5.8):

```text
offline   when now > lease_until, or the session has been closed
active    when the lease is still valid and activity == "busy"
idle      otherwise (lease valid, activity == "idle")
```

Store `lease_until`, the closed flag and `activity`; derive `state` on every read using your own clock — the same
clock you report as `server_time`. Writing `state` into your store is the mistake the `slow` cases are built to
catch: a session whose lease has quietly expired must list as `offline` without anything having happened to it.

**Offline sessions are omitted without `--include-offline`.** With the flag they are included and carry
`"state": "offline"`. C-12 registers a session, closes it, and checks both halves.

**`is_self`** is `true` on exactly the one record whose `session_id` equals the `--session` value, and `false`
everywhere else — including on every record when `--session` was not given. C-12 fails the case both if the named
record is not marked and if any other record is.

**`truncated`** exists so you may cap a very large list; if you never cap, emit `false`.

**Order is yours.** The protocol guarantees no ordering of `sessions` and no case indexes the array beyond the
single-session case. Pick something deterministic — the fs adapter returns them in store order — so that your own
tests are stable.

**Timestamps are RFC 3339 strings and nothing else is fixed** (JSON convention 7): the precision and the offset are
yours. The fs adapter emits UTC with a `Z` and whatever precision the clock gives
(`"2026-09-02T05:55:17.720198Z"`); a whole-second `"2026-08-30T12:00:00Z"` is equally valid, and the suite parses
both. Emit every timestamp in one result from **one** clock — the same one you report as `server_time` — because
that is what the suite compares `lease_until` against (C-10 allows ±5 s between `lease_until` and
`server_time + lease_seconds`).

### 8. The two errors you must get byte-identical

Two error answers are deliberately uniform, because a distinguishable error is an existence oracle — it tells a
caller whether an id or a team it has no right to know about exists.

**`not_found` (exit 6)** is the single answer for an id that is unknown, that belongs to another team, or that
belongs to this team but is not owned by the calling principal. One fixed message, and no `details` that could
distinguish the three:

```json
{"ok":false,"protocol_version":"1","error":{"code":"not_found","message":"session or message not found","retryable":false}}
```

(That is the fs adapter's own text; the wording is yours, the *uniformity* is not.) It applies to `session heartbeat`,
`session close`, `message watch`, `message receive`, `message ack`, `resume.session_id` and `sender_session_id`.

**`unauthorized` (exit 5)** is for "authenticated, but not an active member of this team" — a revoked membership, a
team the profile is bound to but was never a member of, and a `team_ref` that does not exist anywhere. All three
produce byte-identical JSON, again with no `details`:

```json
{"ok":false,"protocol_version":"1","error":{"code":"unauthorized","message":"not an active member of this team","retryable":false}}
```

Note what this rules out: no "team not found", no "your membership was revoked on 2026-08-30", no `details.reason`
that separates the cases. C-26 and C-43 compare the two answers byte for byte. `unauthorized` is reserved for exactly
three situations — not a member of the team, a refused watch join, and a rejected join secret — and never for an
unknown id, which is always `not_found`.

### 9. What an unbound profile answers

A `session` or `message` command on a profile that is not bound to a team exits **4 `unauthenticated`** (there is no
credential) or **11 `config`** (there is a credential but no team): 4.6 pairs each code with the state that earns it.
C-06 runs on a scratch principal with no profile at all, where both readings are true, so it accepts either — and it
checks that the code and the exit status agree, that `retryable` is present and `false`, that stdout is one valid
envelope, and that stderr carries no stack trace.

**The "credential, no team" state is not a free choice.** `team leave` keeps the credential and unbinds the profile
(4.2), and C-08 — which runs on every adapter that advertises `team.join` and can be given a join secret — asserts
`config` (exit 11) for both `session list` and `message receive` on exactly that profile. Answer `unauthenticated`
there and you pass C-06 and fail C-08.

The fs adapter's choice, measured:

| Local state | `describe.profile.state` | `session list` answers |
| --- | --- | --- |
| no `profile.json` | `unconfigured` | `config`, exit 11, `details.reason = "profile_missing"` |
| profile but no credential file | `unauthenticated` | `unauthenticated`, exit 4, `details.reason = "credential_missing"` |
| credential, no `team_ref` | `not_member` | `config`, exit 11, `details.reason = "no_team_bound"` |
| credential and a team | `joined` | the list |

Only the first two rows are yours: C-06 takes either answer for a profile that does not exist, and no case
exercises a profile with no credential file. The third is pinned to `config` by C-08, as above. Pick one column and
hold to it; a user reading `brigade doctor` output cares which. If you have no reason to differ, copy that column
verbatim — it is the one the reference adapter's own tests and the example scripts below show, and `details.reason`
is free-form (4.3.1), so those three tokens cost you nothing.

### 10. The commands the fixture needs

C-01 and C-06 run on scratch principals, but C-02's stdin checks and C-05 both use fixture principal A — and
building the fixture registers one session per principal. So a "`describe` and `session list` only" adapter
still needs a `session register` that **succeeds**, whichever provisioning route you take. Here is the whole of
it, so you are not blocked on a section you have not reached yet.

(On the self-provisioning route you need `team create` and `team join` as well. Their documents, and everything else
the suite does while building the fixture, are under *Provisioning: `--setup`, and when you need it* above; their
shapes are 4.4.10. On the `--setup` route your script does that work instead and neither command need exist.)

**Input** (`SessionRegistration`, one JSON document on stdin):

```json
{
  "harness": "claude-code", "harness_version": "2.1.251",
  "session_name": "payments-api", "session_description": null,
  "activity": "busy", "inbound": "accept", "lease_seconds": 90, "workspace_label": null,
  "resume": {"session_id": "a session_id previously returned to this principal"}
}
```

| Member | Required | Notes |
| --- | --- | --- |
| `harness`, `harness_version` | yes | identify the registering harness |
| `session_name` | yes | non-empty, ≤ `max_session_name_codepoints` |
| `activity` | yes | `busy` or `idle` |
| `inbound` | yes | `accept`, `hold` or `refuse`; ignore it if you do not advertise `session.inbound`, but still store and echo *something* valid |
| `session_description` | optional, nullable | capability `session.description` |
| `lease_seconds` | optional, nullable | within **your** advertised `lease.min_seconds..lease.max_seconds`, else `invalid_input`; absent means `lease.default_seconds` |
| `workspace_label` | optional, nullable | capability `session.workspace_label` |
| `resume.session_id` | optional | capability `session.resume` |

**A missing required member is `invalid_input`, never a default.** "Required" in that table is the protocol's word
(4.4.2) and it binds: a document without `activity` or without `inbound` is exit 3 with `details.field` naming it,
not a registration with a guessed `idle`/`accept`. Measured on the fs adapter, `{"harness":"h","harness_version":"1",
"session_name":"x"}` answers `{"code":"invalid_input","message":"activity must be one of: busy, idle","retryable":
false,"details":{"reason":"invalid_value","field":"activity","allowed":"busy, idle"}}`, exit 3. Being lenient here
costs you nothing in the four cases — the suite always sends all five required members — and costs you C-16 and C-27
in a full run, which check that the caps and the shape are enforced before anything is persisted (4.5.11).

**`lease_seconds`, in full.** It is optional *and* nullable (JSON convention 4), and both forms mean the same
thing: absent or `null` grants `lease.default_seconds`, measured 90 on the fs adapter. A value outside your own
advertised `lease.min_seconds..lease.max_seconds` is `invalid_input` (exit 3) — measured, `0` answers
`{"reason": "out_of_range", "min": "1", "field": "lease_seconds"}` and `9999` answers the same `reason` with `min`
and `max` — and a value of the wrong JSON type (`"ninety"`) is a decode failure, `invalid_input` again. The range in
the check is the one you advertise in `describe`, not the protocol's example (section 6): the two must agree, because
the harness and the suite read yours and stay inside it.

**`human_label` is not in this document, and you still have to emit it.** The registration carries no label; the
label reached you once, at `team create` / `team join` (or through `--setup`), and nothing in the protocol supplies
it again. Store it on the profile when you bind and stamp it onto every session record you write — the record's
`human_label` is optional in the shape (4.4.3) but C-12 requires one on every session it lists, so treat "copy it
from the profile at register time" as the rule rather than as advice.

**Output**: the `SessionRecord` of the previous section with three more members — `resumed` (boolean),
`lease_seconds` (integer, the lease actually granted) and `server_time` (timestamp). One flat object, not a
nested one. This is a real result from `bin/brigade-adapter-fs` for exactly the document
`{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "payments-api", "activity": "busy",
"inbound": "accept"}` — the input above without its three optional members, which is why they are absent from the
output — and it is the same session the record in section 7 lists:

```json
{
  "session_id": "b80e422f9f0fe9ab2767a7b5bf8bdd52",
  "session_name": "payments-api",
  "principal_ref": "bbed57255bcc1aaf17b41f8d59c444ef",
  "human_label": "alice@example.com",
  "state": "active",
  "activity": "busy",
  "inbound": "accept",
  "last_seen_at": "2026-09-02T06:42:26.622054Z",
  "lease_until": "2026-09-02T06:43:56.622054Z",
  "harness": "claude-code",
  "harness_version": "2.1.251",
  "created_at": "2026-09-02T06:42:26.622054Z",
  "is_self": false,
  "resumed": false,
  "lease_seconds": 90,
  "server_time": "2026-09-02T06:42:26.622054Z"
}
```

Set `lease_until = server_time + lease_seconds`, `is_self` to `false` (it is only ever `true` in a
`session list --session` result), and `resumed` to `false` unless you honoured a `resume`. Two registrations
with the same `session_name` are **two sessions** with different ids, never one.

C-02 then feeds this same command three bad documents on a joined principal — `{`, a valid document one byte
over 1 MiB, and nothing at all — and each must be `invalid_input` (exit 3). The case additionally reads
`details.reason` on the malformed one, so that document's envelope must carry it (4.3.1 fixes `reason =
"malformed_json"` with no `field` there, because no member can be named); on the empty and oversize ones 4.3.1 asks
for `field: "stdin"`, and the case does not check it. The suite runs this half on a **joined** principal because on
an unconfigured one both the unbound-profile rule of section 9 and the stdin rule apply and the spec orders
neither — so either order passes, and you do not have to match the fs adapter's (profile first).

### 11. Prove it: the four cases

C-02's stdin half and C-05 both run as fixture principal **A**, so selecting these four builds the fixture and your
adapter has to be provisionable before any of them can be reported. The provisioning flags are part of the command
line, not decoration on it: take your route from *Provisioning: `--setup`, and when you need it* above and put it in.
Four shapes cover almost every adapter:

```console
# a store-backed adapter that reads its root from an environment variable it names itself
bin/brigade-conformance --shared-env MYSTORE_ROOT --adapter /abs/path/to/your-adapter --only C-01,C-02,C-05,C-06

# the same adapter taking its root as a fixed argument you create and clean up yourself
bin/brigade-conformance --adapter /abs/path/to/your-adapter --only C-01,C-02,C-05,C-06 -- --root /abs/store

# an adapter that does not advertise team.create and team.join: your script provisions each principal
bin/brigade-conformance --setup /abs/provision.sh --adapter /abs/path/to/your-adapter --only C-01,C-02,C-05,C-06

# a network adapter: the endpoint and the publishable key as ordinary environment variables
bin/brigade-conformance --env API_URL=https://… --env API_KEY=… --adapter /abs/path/to/your-adapter --only C-01,C-02,C-05,C-06
```

The reference adapter, run exactly that way this session:

```console
$ bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter bin/brigade-adapter-fs --only C-01,C-02,C-05,C-06
brigade-conformance: brigade-adapter-fs 0.0.0-dev (bin/brigade-adapter-fs), protocol 1, 4 cases selected.
C-01   PASS   0.01s  4.2 describe offline
C-02   PASS   0.23s  4.6 usage and invalid_input
C-05   PASS   0.01s  4.5.14 no secret on argv or in output
               note: every spawn's stderr and the run directory are scanned for known secrets by the launcher; a hit anywhere in the run is reported as C-05
C-06   PASS   0.01s  4.6 unbound profile
4 passed, 0 failed, 0 skipped in 0.28s.
```

What each case checks:

| Case | Checks |
| --- | --- |
| **C-01** | `describe` on a fresh principal: `ok: true`, exit 0, `protocol_version` `"1"`, `profile.state` `unconfigured` with none of the four team members, **nothing created** under the config dir, the state dir or the shared dir, and — a second spawn — that it returns with stdin held open and never closed (B-1). The run's very first `describe`, which happens before any case, is judged by the same rule |
| **C-02** | `session frobnicate` → `usage` exit 2; `describe --bogus` → `usage` exit 2; then, on principal A, `session register` with malformed stdin (`{`) → `invalid_input` exit 3 with a `details.reason`, with 1 MiB + 1 bytes of valid JSON → `invalid_input`, and with empty stdin → `invalid_input`. Every failing envelope must carry `retryable` |
| **C-05** | `session list --join-secret brg1.x.y` → `usage` exit 2 with the value echoed on neither stream; the same for `team join` when you advertise `team.join`; `describe` on a joined principal carries neither the team's real secret nor the literal prefix `brg1.`. On top of that the launcher scans, for the whole run, **every** spawn's stderr and every spawn's stdout except `team create`'s — that one command's stdout is where a join secret legitimately appears (4.4.10) and is where the launcher learns the secret it then hunts for everywhere else — and it walks the entire run directory at the end (profile files, logs, your store, the `--shared-env` directory). Any hit anywhere is reported as a C-05 failure, whichever case was running |
| **C-06** | on an unbound scratch principal, all four of `session list`, `session register`, `message receive --session <random>` and `message send` answer `unauthenticated` (4) or `config` (11), with the exit status matching the code, `retryable` present and `false`, exactly one envelope on stdout, and no `goroutine`/`panic:` text on stderr |

C-06 is why a describe-and-list-only adapter still has to *recognise* `session register`, `message receive` and
`message send`: an unimplemented verb would be `usage` (exit 2), which is not one of the two allowed answers. It does
not have to implement them — it has to reject them for the right reason, in the right order (check the profile before
you look at the request).

### 12. Beyond these two commands

This page stops the guided tour at `describe` and `session list` on purpose: they are the two the acceptance review
covers, and every other wire shape is already written down once, normatively, and generated into a schema. **Do not
reconstruct a shape from the excerpts on this page or from the example scripts below** — read it where it is defined,
in `docs/protocol-v1.md` beside this file and in the schema generated from the same Go types:

| What | Where |
| --- | --- |
| `SessionRegistration`, `SessionRecord` | 4.4.2, 4.4.3 (section 10 above repeats them because the fixture needs them) |
| `HeartbeatRequest`, `HeartbeatResult` | 4.4.4 |
| `MessageEnvelope`, `SendRequest`, `SendResponse`, `message ack` | 4.4.5, 4.4.6, 4.4.7, 4.4.8 |
| `message watch` events (`ready`, `message`, `status`, `acked`, `heartbeat_ok`, `error`) and its stdin commands | 4.4.9 |
| `team create`, `team join`, `team leave`, `team members` | 4.4.10 |
| every member's type, requiredness and cap, machine-readable | `docs/protocol-v1.schema.json` (`make schema` regenerates it from the same Go types the reference adapter uses; JSON convention 6: where the schema and `Validate()` disagree, `Validate()` wins) |

Three decisions in that territory are genuinely yours, and inventing an answer is the right thing to do:

- **The idempotency fingerprint.** 4.5.4 fixes the behaviour — the same `idempotency_key` from the same
  `sender_session_id` returns the same `message_id` with `duplicate: true` (C-21), and the same key with a different
  body or a different recipient is `conflict` (C-22) — but not how you detect "different". Any digest that covers at
  least `recipient_session_id` and `body` satisfies both cases; the fs adapter keeps one fingerprint per key under
  `idem/<sender>/<sha256(key)>.json`.
- **`profile init` / `profile status` / `profile reset` / `profile revoke-credentials`** are adapter-specific in both
  directions (4.2), and no conformance case exercises them. The one rule that still binds: never print a secret.
- **`delivery.ordering`** beyond `none`, which 4.8 leaves unspecified.

One thing in that territory is *not* optional: `message watch` is core (4.2). Every adapter writes the NDJSON stream,
starting with one `ready` event whose `mode` is `polling` unless you advertise `message.watch.push` (4.4.9, 4.7), and
nine of the 45 cases (C-33..C-41) are about it.

## The environment your adapter runs in

Under a **live Claude Code session** the harness builds your process's environment from scratch and passes only
(plan 3.2, protocol 4.1):

`PATH`, `HOME`, `TMPDIR`, the `XDG_*` variables, `LANG`, `LC_*`, `CLAUDE_CONFIG_DIR`, the proxy variables
(`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lowercase forms), `SSL_CERT_FILE` and `SSL_CERT_DIR` when
non-empty — plus the four the harness computes: `BRIGADE_PROFILE`, `BRIGADE_CONFIG_DIR`, `BRIGADE_STATE_DIR` and
`BRIGADE_LOG_LEVEL`.

`GODEBUG`, `GOFLAGS`, `NODE_OPTIONS`, `CLAUDE_CODE_MESSAGING_*` and every other inherited variable are absent by
construction. The conformance suite passes an even smaller set (the table under *Run the conformance suite*).

**A `BRIGADE_<ADAPTER>_*` variable therefore never arrives under a live session.** This is the trap that catches most
first adapters: your DSN, your bucket name, your storage root cannot come from the ambient environment. They come
from one of two places (4.1, decision 10):

1. **your profile file** under `BRIGADE_CONFIG_DIR`, written by your own `profile init` or by `team join`'s optional
   `backend` member; or
2. **fixed arguments in the `adapter_command` JSON array**, which the harness prepends verbatim to every invocation.

The fs adapter is the worked pattern for the second: it accepts a **leading** `--root <dir>` before the group, so
`["brigade-adapter-fs", "--root", "/abs/store"]` followed by `["describe"]` is one well-formed argv. It honours
`$BRIGADE_FS_ROOT` too, but only because a human running it from their own shell is a supported use — under a live
session that variable is simply not there. Note the discipline: `--root` is accepted *only* in the leading position;
after the verb it is an unknown flag and therefore `usage`.

**Timeouts the harness applies.** 20 s to a request/response command in general; 8 s to `session register` at
`SessionStart`; 1 s to `session close` at `SessionEnd`; 3 s to commands issued by the watcher. `message watch` runs
until stdin EOF, a `close` command, SIGTERM or a fatal error — and must exit **0 within 5 s** of any of the first
three. Do not install a handler that swallows SIGTERM, and do not let a drain loop delay the exit past five seconds.

**Exit statuses.** Only 0..12, ever, from your own code. Never 126 or 127 (the shell's "cannot execute" / "not found"
statuses) and never ≥ 128 (killed by a signal): the harness reads those as a spawn failure, not as your answer. The
suite checks the range on every single spawn and attributes a violation to the running case. A crash that would
otherwise die on a signal should be caught and turned into `internal` (exit 1) with a well-formed envelope.

**Never write to stdout except protocol output**, and never spawn a shell. Diagnostics go to stderr.

## The profile file and where state lives

The protocol deliberately does not specify what a profile file looks like (4.8). What follows is the convention the
shared Go helper (`internal/adapterkit`) writes and the fs adapter follows, and following it costs you nothing and
buys you the suite's default `--rebind`.

```text
${BRIGADE_CONFIG_DIR}/profiles/<name>/         directory, mode 0700
${BRIGADE_CONFIG_DIR}/profiles/<name>/profile.json    mode 0600 — configuration and identity, NEVER a secret
${BRIGADE_CONFIG_DIR}/profiles/<name>/<credential>    mode 0600 — the adapter's own credential file
```

`profile.json` (plan 5.2), as the shared Go helper writes it:

| Member | Type | Notes |
| --- | --- | --- |
| `version` | integer | schema version; `1` today. An unknown version is `config`, not a guess |
| `adapter` | string | which adapter owns this profile (`"fs"`, `"supabase"`, yours). Free-form: nothing but your own build reads it, and the suite's default `--rebind` ignores it |
| `url` | string, optional | backend URL |
| `publishable_key` | string, optional | a *publishable* key is configuration, not a secret |
| `team_ref` | string, optional | the team the profile is bound to; empty while unbound |
| `team_name` | string, optional | that team's display name |
| `principal_ref` | string, optional | the principal the credential authenticates |
| `human_label` | string, optional | the operator-chosen label |
| `secret_store` | string, optional | where the credential lives; `"file"` is the only v1 value |
| `created_at` | RFC 3339 | when the profile was first written |

A real one, written by `team create` (note that no secret appears in it):

```json
{"version":1,"adapter":"fs","team_ref":"19612a12428e75ff9b353a1e7ae54b13","team_name":"ops","principal_ref":"8242a65701d6e211ad7ffea4c3e2db90","human_label":"alice@example.com","secret_store":"file","created_at":"2026-09-02T05:55:17.609965Z"}
```

**A profile file you cannot trust is `config` (exit 11), never a guess.** That covers a file that does not parse as
JSON, a `version` your build does not know, and a file whose mode grants group or other access. Measured on the fs
adapter, in that order: `details.reason` `malformed_json`, `unsupported_version`, `insecure_mode` — and `describe`
answers the same way, because a file it cannot read is a broken environment, not a profile state (section 5).

**Why the top-level `team_ref` matters.** C-26 and C-43 must put a profile in front of a team it is not a member of.
Without `--rebind` the suite does that by parsing `<config>/profiles/default/profile.json`, replacing the top-level
`team_ref` member, writing it back, and restoring the original bytes afterwards. If your binding lives somewhere else
— a database row, a keyring, a nested object — those two cases will not work until you supply `--rebind <cmd>`.
Keeping a top-level `team_ref` here is the cheap option.

**What a credential *is* is yours.** 4.8 says the protocol does not specify how an adapter authenticates or stores
credentials, so a credential can be a refresh token, a key handle, a keyring reference or — for a test-only adapter
— a file whose mere presence stands in for one. It has to answer exactly one question from local files alone,
without the network, because `describe` asks it on every call (section 5): *does this profile hold something that
would let it act as its principal?* The fs adapter's answer is `credential.json` beside `profile.json`, mode 0600,
holding no secret at all because it guards nothing:

```json
{"principal_ref":"1b5727948c44397e1af164cacfe2ca1a","created_at":"2026-09-02T06:24:19.047108Z","last_team_ref":"8dc5590e41e9eeced29de807264b1ed5"}
```

Copy the *shape* of that decision, not that file: presence-as-credential is exactly why the fs adapter is insecure
and test-only.

**Credential files.** Mode **0600** in a **0700** directory, written atomically (write a temp file in the same
directory, `fsync`, rename). Refuse to read one that is group- or world-readable and answer `config` (exit 11): a
credential another local account can read is not a credential. Never put a secret in `profile.json`, never in
`describe` output, and never in any file under the user's **project** directory — hooks run with the working
directory set to the project, which is why every path you resolve must be absolute and rooted in
`BRIGADE_CONFIG_DIR` or `BRIGADE_STATE_DIR`. The suite walks the whole run directory at the end of every run looking
for join secrets and for anything shaped like `brg1.<x>.<y>`, and reports a hit as a C-05 failure.

**`BRIGADE_STATE_DIR`** is for everything that is not configuration and not a credential: caches, logs, lock files, a
local store. It is separate from the config directory so that a user can back up one and discard the other, and it
follows the same absolute-only, `XDG_STATE_HOME`-then-`$HOME/.local/state` chain described under *Environment and
precedence*.

## Logging and redaction

**stderr only.** Every diagnostic your adapter writes goes to stderr; stdout is protocol output and nothing else, at
every log level. The harness captures your stderr into its own log at debug level and never shows it to the model.

**NDJSON is recommended** — one JSON object per line — with the members Brigade's own logger uses:

```json
{"time":"2026-09-02T01:56:26.630026-04:00","level":"DEBUG","msg":"command failed","comp":"adapter-fs","group":"session","verb":"close"}
```

`--log-level error|warn|info|debug` selects the level, defaulting to `$BRIGADE_LOG_LEVEL`. Note the asymmetry the fs
adapter chose and that is worth copying: an invalid `--log-level` **flag** value is `usage` (the user typed it), an
invalid `BRIGADE_LOG_LEVEL` **environment** value is `config` (the environment is wrong).

Two corners of that, both measured on the fs adapter this session and neither observed by any case. When **both** are
present and the environment value is invalid, the answer is still `config` (exit 11): the environment is validated on
its own terms, and a valid flag overrides the *level*, not the fact that the environment is broken. An **empty**
`BRIGADE_LOG_LEVEL` counts as unset (section 2) and is not an error.

**Writing nothing at all to stderr is conformant.** The requirements are that no secret appears there and that
nothing but protocol output appears on stdout; a silent adapter satisfies both and gives C-05 nothing to find.
NDJSON diagnostics are a recommendation for your own sake (4.1, stderr), not a case.

**No secret at any level.** C-05 runs your adapter at `BRIGADE_LOG_LEVEL=debug` for the entire suite and greps every
spawn's stderr, so "it only leaks at debug" is not a defence. Brigade's reference redactor masks four formats inside
string values, and they are a sensible minimum for any adapter:

| Pattern | Shape |
| --- | --- |
| JWT | `eyJ<base64url>.<base64url>.<base64url or empty>` |
| Brigade join secret | `brg1.<team_ref>.<secret>` |
| Supabase secret key | `sb_secret_<rest>` |
| Authorization credential | `Bearer <token>`, case-insensitive, at a word boundary |

It also blanks any attribute whose **key** contains `token`, `secret`, `authorization`, `apikey`/`api_key` or
`password` (case-insensitively, `-` folded to `_`), and it registers exact runtime secrets so a refresh token is
masked even inside a URL query or a wrapped error message. One warning from experience: log **scalars**, not
structures. A struct or map printed through a generic "any" logging call bypasses per-value filtering entirely and
prints its fields verbatim; Brigade bans that call outside the logging package and replaces any non-error structured
value with a fixed marker at run time.

The fixed protocol text `expected brg1.<team_ref>.<secret>`, which names the *format*, is not a secret and the
suite's scanner is written not to flag it — but anything else beginning `brg1.` and containing two dot-separated
components is treated as a leak wherever it appears.

## Worked example: the fs adapter

`bin/brigade-adapter-fs` (`internal/adapters/fs`, `cmd/brigade-adapter-fs`) is the reference implementation, and
`internal/adapters/fs/README.md` is its own full documentation. **It is insecure and test-only.** Its backend is one
directory on disk with no access control beyond file modes: any process that can read the root can read every team's
messages, every team's membership and every session's inbox; there is no authentication, the presence of a
`credential.json` file beside the profile *is* the credential, and the join secret's digest sits next to the data it
protects. Nothing ships it.

It exists for four reasons: it will be the second BAP/1 implementation once the bundled Supabase adapter is written
(P2; today it is the only one), so the plugin can be proved to carry no Supabase assumption; it runs the conformance
suite in seconds instead of the tens of seconds a hosted backend costs; it is the harness's test fixture; and it is
the dry run for the object-store adapter — the closest published precedent for a polling adapter over a dumb store.

**The store layout.** Every directory 0700, every file 0600.

```text
<root>/.lock                                              flock sidecar; every command but `describe` holds it
<root>/teams/<team_ref>/team.json                         {team_ref, team_name, join_secret_sha256, created_by, created_at}
<root>/teams/<team_ref>/members/<principal_ref>.json      {principal_ref, human_label, status, joined_at}
<root>/teams/<team_ref>/sessions/<session_id>.json        the session; no `state` — that is computed at read time
<root>/teams/<team_ref>/inbox/<recipient>/<seq>.<id>.json a pending MessageEnvelope
<root>/teams/<team_ref>/acked/<recipient>/<seq>.<id>.json the same file after acknowledgement, plus acked_at
<root>/teams/<team_ref>/idem/<sender>/<sha256(key)>.json  {message_id, fingerprint, recipient_session_id, hop_count, created_at}
```

The mapping from protocol concept to file is one-to-one and deliberately dull: a **team** is a directory, a
**member** is a file in it, a **session** is a file, an **inbox** is a directory of message files named
`<seq>.<id>.json`, an **acknowledgement** is a rename from `inbox/` to `acked/`, and an **idempotency key** is a file
named by its digest. `seq` is the wall clock in nanoseconds, floored at one past the highest number already used for
that recipient and zero-padded to 19 digits, so lexical order equals send order monotonically, across
acknowledgements and deletions alike.

**Root resolution**, in order: `--root <dir>` (leading position only), else `$BRIGADE_FS_ROOT`, else
`${BRIGADE_STATE_DIR}/fs-adapter`. A **relative** root is refused with `config` (exit 11), for the reason given
above: hooks run with the working directory set to the project tree. `team create --secret-file <path>` is held to
the same rule, as `usage`, and the file is written *before* the team is created so that a path the adapter cannot
write leaves nothing behind — the secret is printed once, and a caller who never received it must be free to try
again rather than end up bound to a team nobody can join.

**Identifiers** are 16 random bytes as lowercase hex — that shape is this adapter's own (4.8 leaves it open). Its
join secrets are `brg1.<team_ref>.<32 hex>`, of which only the last component is the adapter's choice: the `brg1.`
prefix and the embedded `team_ref` are fixed by 4.4.10 for every adapter that implements `team create` / `team join`
(see *Provisioning* above). Only the SHA-256 of the whole secret is stored. Every identifier that becomes a path
component is checked against `[A-Za-z0-9_-]{1,64}` first and is otherwise simply *not found*, so a hostile
`--session` or a hand-edited `profile.json` cannot escape the root.

**Leases and states** are computed at read time from `closed_at`, `lease_until` and `activity`, never stored. The
retention sweep runs under the lock at the start of every command except `describe`, and does nothing at all when no
team exists.

**Concurrency** is one writer at a time: every command but `describe` takes the store lock for the whole of its store
access. The **watch** is a 200 ms poll that takes and releases the lock once per poll and never across the sleep; on
every poll it re-applies the membership check under that lock before reading the inbox, so a watch whose principal is
revoked while it runs emits one `unauthorized` error event and exits 5 within a poll interval instead of going on
delivering a former member the team's messages. It does not advertise `message.watch.push`, so its `ready` event says
`"mode": "polling"`.

**The four mutants.** Four deliberately broken builds live in the package behind build tags, so the normal build
contains no mutant code path at all. They are the suite's positive control: each must fail **exactly** its set and no
other case, and the normal build must fail none. The sets are what `internal/conformance/mutants_test.go` asserts.

| Build tag | Real twin | What it breaks | Fails exactly |
| --- | --- | --- | --- |
| `mutant_noack` | `store_ack.go` | the ack routine reports `acked` and moves nothing | C-29b, C-30, C-36, C-41 |
| `mutant_teamleak` | `store_list.go` | `session list` walks every team, not the profile's | C-12, C-26 |
| `mutant_trustsender` | `store_send.go` | the forbidden members of 4.4.6 are accepted and a forged sender is trusted | C-23, C-24 |
| `mutant_caporder` | `store_caps.go` | the two unacknowledged caps are checked in the wrong order — recipient-wide before per-pair | C-28 |

```console
go build -tags mutant_caporder -o /abs/path/mutant ./cmd/brigade-adapter-fs
bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter /abs/path/mutant --only C-28
```

`mutant_noack`'s set is larger than the original plan's table predicted (it named only C-30 and C-36): C-29b's hop
chain cannot avoid acknowledgements and C-41's restart check is a stdin-ack check. `mutant_caporder` exists because
the *order* of the two unacked caps was the decisive untested defect twice over — every other property of the caps
(codes, reasons, `retry_after_ms`, thresholds) survives the swap, and only a case that puts **both** caps at their
limit at once can tell the two orders apart. An expected set is never widened to fit a case that turns out to depend
on a mutation the table did not foresee; that is reported as a defect instead.

**`--shuffle <seed>`** exists for the same reason the mutants do. The cases share a fixture, a store and the
adapter's rate budgets, so id order is one order out of many; two order-dependent cases were found only by shuffling,
with a throwaway tool the binary did not yet have. If you write cases of your own, shuffle them.

**An honest size note.** `internal/adapters/fs` is about **3,200 lines of Go** without tests (3,400 with the mutant
twins; 6,400 with tests). The plan estimated 500. The overrun is not gold-plating: the store lock, the read-time
state computation, the uniform errors, the two unacked caps in the right order, the implicit hop chain and the
polling watch with its per-poll membership re-check are each small, and there are a lot of them. Budget accordingly
for your own adapter, and read the suite's case list before you estimate.

## Example scripts

Three end-to-end scripts live in `cmd/brigade/testdata/script/`. They are `txtar` archives run by
`rogpeppe/go-internal/testscript`: a script section, then file sections introduced by `-- name --`. They drive the
**real binary** as a child process, which is the only place the 4.6 exit statuses and the 4.1 stdout discipline can
actually be observed, and they are the readable end of the fs adapter's documentation.

**The legend.** Five commands are Brigade's own additions to testscript's builtin set (`internal/testutil/tscmd`);
the rest (`exec`, `stdout`, `stderr`, `cp`, `cmp`, `exists`, and `!` for negation) are builtins.

| Command | Meaning |
| --- | --- |
| `status <n> <cmd> [args…]` | run the command and assert its **exact** exit status, leaving stdout and stderr in place for the checks that follow. The builtin `exec` asserts only success and `! exec` only failure, so neither can tell a `usage` (2) from a `not_found` (6) — and the exit code is half of Brigade's error contract |
| `json <stdout\|stderr\|file> <.dotted.path> <expected>` | assert **one field** of a JSON document; negatable with `!`. The builtin `stdout` is a regexp over the whole stream, which passes on a document that merely contains the text somewhere else |
| `jsonenv <stdout\|stderr\|file> <.dotted.path> <VAR>` | export one field into the script environment, so a value printed by one command becomes an argument to the next |
| `expand <in> <out>` | write `<in>` to `<out>` with `$VAR` expanded, so a script can build the next command's stdin document out of values it just learned |
| `sleeper` | start a `sleep 300` child, reap it, and export `$SLEEPER_PID` (unused by these three scripts) |

**The environment a script gets.** Each script runs in its own `$WORK` directory with every directory Brigade or
Claude Code would otherwise take from the developer's account redirected under `$WORK/.brigade/`:
`BRIGADE_CONFIG_DIR`, `BRIGADE_STATE_DIR`, `BRIGADE_FS_ROOT`, `CLAUDE_CONFIG_DIR`, `CLAUDE_PLUGIN_ROOT`,
`XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `XDG_CACHE_HOME`. `HOME` is deliberately left at testscript's own `/no-home`,
so a stray `~` fails loudly instead of resolving somewhere real. `brigade-adapter-fs` is built exactly as `make build`
builds it and put on `PATH`.

### `fs-team.txtar` — the team lifecycle

Create, join, roster, leave and rejoin: the whole of C-08 in one pass, plus the "describe creates nothing" check
(`! exists $WORK/.brigade/fs-root/.lock`), the `profile_bound` conflict when a bound profile tries to create a second
team, and the argv secret refusal. Note how the join secret is captured with `jsonenv` and never appears in the
script text.

```text
# The filesystem adapter's team lifecycle, through the real binary (P1-5).
#
# One team, two profiles in the same $WORK, and the whole of C-08: create,
# join, roster, leave, and the rejoin that keeps the principal. Everything
# here crosses a process boundary, which is the only place the 4.6 exit
# statuses and the stdout discipline of 4.1 can actually be observed.

# --- a scratch profile is unconfigured, and describe creates nothing -----
exec brigade-adapter-fs describe
json stdout .ok true
json stdout .result.protocol_version 1
json stdout .result.profile.state unconfigured
json stdout .result.profile.name default
# The store root is laid out by the test harness, but `describe` neither
# takes the store lock nor creates a team directory: it answers from local
# files only (C-01).
! exists $WORK/.brigade/fs-root/.lock
! exists $WORK/.brigade/fs-root/teams

# The lease range is this adapter's own, not the 4.4.1 example's: min 1 so
# the slow expiry cases take seconds.
json stdout .result.lease.min_seconds 1
json stdout .result.lease.default_seconds 90

# --- team create prints the secret exactly once -------------------------
stdin create.json
exec brigade-adapter-fs team create
json stdout .ok true
json stdout .result.team_name ops
cp stdout created.json
jsonenv created.json .result.join_secret SECRET
jsonenv created.json .result.team_ref TEAMREF
jsonenv created.json .result.principal_ref ALICE
stderr 'printed once'

exec brigade-adapter-fs describe
json stdout .result.profile.state joined
json stdout .result.profile.team_name ops
json stdout .result.profile.team_ref $TEAMREF
json stdout .result.profile.human_label alice@example.com

# A second team on a bound profile is a conflict, and the binding stands.
stdin create.json
status 7 brigade-adapter-fs team create
json stdout .error.code conflict
json stdout .error.details.reason profile_bound
exec brigade-adapter-fs describe
json stdout .result.profile.team_ref $TEAMREF

# --- a second profile joins with the secret it was given ----------------
expand join.in join.json
stdin join.json
exec brigade-adapter-fs --profile bob team join
json stdout .result.team_ref $TEAMREF
json stdout .result.rejoined false
cp stdout joined.json
jsonenv joined.json .result.principal_ref BOB

exec brigade-adapter-fs team members
json stdout .result.team_name ops
json stdout .result.members.0.status active
json stdout .result.members.1.status active

# --- leave is idempotent and unbinds ------------------------------------
exec brigade-adapter-fs --profile bob team leave
json stdout .result.left true
json stdout .result.team_ref $TEAMREF
exec brigade-adapter-fs --profile bob describe
json stdout .result.profile.state not_member

# A session command on an unbound profile is 11 config for this adapter:
# the credential is there, the team binding is not (4.6, C-06).
status 11 brigade-adapter-fs --profile bob session list
json stdout .error.code config
json stdout .error.retryable false

exec brigade-adapter-fs --profile bob team leave
json stdout .result.left true

# --- the rejoin keeps the same principal (C-08) -------------------------
stdin join.json
exec brigade-adapter-fs --profile bob team join
json stdout .result.rejoined true
json stdout .result.principal_ref $BOB

# --- a secret on argv is refused, and never echoed (C-05) ---------------
status 2 brigade-adapter-fs --profile bob team join --join-secret $SECRET
json stdout .error.code usage
! stdout $SECRET
! stderr $SECRET

-- create.json --
{"team_name": "ops", "human_label": "alice@example.com"}
-- join.in --
{"join_secret": "$SECRET", "human_label": "bob@example.com"}
```

```console
go test ./cmd/brigade -run 'TestScript/fs-team'
```

### `fs-session.txtar` — the session lifecycle

`register` → `list` → `heartbeat` → `close` → `resume`, which is C-10, C-11, C-12, C-13, C-15, C-19 and C-19b in one
pass. (The case ids in the script's comments are notes for a reader, not assertions the script makes: the
`--include-offline` check for a **closed** session is C-12; C-14 is its `slow` twin for a session whose lease merely
**expired**.) Every session id is learned from one
command's output and becomes the next command's argument, so nothing is hard-coded — and `session list` is
deliberately never indexed beyond the single-session case, because the protocol guarantees no ordering. This is the
script to read while implementing `session list`.

```text
# The filesystem adapter's session lifecycle, through the real binary (P1-5).
#
# register → list → heartbeat → close → resume, which is C-10, C-11, C-12,
# C-13, C-15, C-19 and C-19b in one pass. Every session id is learned from
# one command's output and becomes the next command's argument, so nothing
# here is hard-coded — and `session list` is deliberately never indexed
# beyond the single-session case: the protocol guarantees no ordering.

stdin create.json
exec brigade-adapter-fs team create
cp stdout created.json
jsonenv created.json .result.team_ref TEAMREF

# --- register mints an opaque id and an active session ------------------
stdin register.json
exec brigade-adapter-fs session register
json stdout .ok true
json stdout .result.state active
json stdout .result.resumed false
json stdout .result.lease_seconds 90
json stdout .result.human_label alice@example.com
json stdout .result.is_self false
cp stdout registered.json
jsonenv registered.json .result.session_id SID

exec brigade-adapter-fs session list --session $SID
json stdout .result.team_ref $TEAMREF
json stdout .result.team_name ops
json stdout .result.truncated false
json stdout .result.sessions.0.session_id $SID
json stdout .result.sessions.0.is_self true

# --- two registrations with one name are two sessions (C-11) ------------
stdin register.json
exec brigade-adapter-fs session register
cp stdout registered2.json
jsonenv registered2.json .result.session_id SID2
! json stdout .result.session_id $SID

exec brigade-adapter-fs session list
stdout $SID
stdout $SID2

# --- heartbeat applies the new metadata and renews the lease ------------
stdin heartbeat.json
exec brigade-adapter-fs session heartbeat --session $SID
json stdout .result.session_id $SID
json stdout .result.state idle
exec brigade-adapter-fs session list --session $SID
stdout '"session_name":"renamed"'
stdout '"inbound":"hold"'

# An id this profile does not own is the uniform not_found (C-13).
stdin heartbeat.json
status 6 brigade-adapter-fs session heartbeat --session ffffffffffffffffffffffffffffffff
json stdout .error.code not_found
json stdout .error.retryable false
! stdout details

# --- close is idempotent, and a closed session refuses a heartbeat ------
exec brigade-adapter-fs session close --session $SID
json stdout .result.state offline
json stdout .result.session_id $SID
exec brigade-adapter-fs session close --session $SID
json stdout .result.state offline
stdin heartbeat.json
status 7 brigade-adapter-fs session heartbeat --session $SID
json stdout .error.details.reason session_closed

# A closed session is out of the list unless --include-offline (C-12; C-14 is the lease-expiry twin).
exec brigade-adapter-fs session list
! stdout $SID
stdout $SID2
exec brigade-adapter-fs session list --include-offline
stdout $SID
stdout '"state":"offline"'

# --- resume re-opens the closed session in place (C-19) -----------------
expand resume.in resume.json
stdin resume.json
exec brigade-adapter-fs session register
json stdout .result.session_id $SID
json stdout .result.resumed true
json stdout .result.state active

# A resume of a session that is open with a valid lease is a conflict, and
# the session is left alone (C-19b).
stdin resume.json
status 7 brigade-adapter-fs session register
json stdout .error.code conflict
json stdout .error.details.reason session_live

-- create.json --
{"team_name": "ops", "human_label": "alice@example.com"}
-- register.json --
{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "payments-api", "activity": "busy", "inbound": "accept"}
-- heartbeat.json --
{"activity": "idle", "session_name": "renamed", "inbound": "hold", "lease_seconds": 120}
-- resume.in --
{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "payments-api", "activity": "busy", "inbound": "accept", "resume": {"session_id": "$SID"}}
```

```console
go test ./cmd/brigade -run 'TestScript/fs-session'
```

### `fs-message.txtar` — the message path

`send` → `receive` → `ack` → idempotency → a forged sender member → the byte-identity of the uniform `not_found`:
C-20, C-21, C-22, C-23, C-25 and C-30. The last block is the one only a script makes obvious — the answer for a
foreign recipient and the answer for a random id are compared with `cmp`, byte for byte, and a *different* failure is
compared too so the comparison can actually fail.

```text
# The filesystem adapter's message path, through the real binary (P1-5).
#
# send → receive → ack → idempotency → a forged sender member → the
# byte-identity of the uniform not_found. That is C-20, C-21, C-22, C-23,
# C-25 and C-30, and the last block is the one that only a script can make
# obvious: the answer for a foreign id and the answer for a random id are
# compared with cmp, byte for byte.

stdin create.json
exec brigade-adapter-fs team create
cp stdout created.json
jsonenv created.json .result.join_secret SECRET

expand join.in join.json
stdin join.json
exec brigade-adapter-fs --profile bob team join
json stdout .result.rejoined false

stdin register-a.json
exec brigade-adapter-fs session register
cp stdout a.json
jsonenv a.json .result.session_id ALICE

stdin register-b.json
exec brigade-adapter-fs --profile bob session register
cp stdout b.json
jsonenv b.json .result.session_id BOB

# --- send stamps the sender from the adapter's own authority (C-20) -----
expand send.in send.json
stdin send.json
exec brigade-adapter-fs message send
json stdout .ok true
json stdout .result.status accepted
json stdout .result.duplicate false
json stdout .result.hop_count 0
cp stdout sent.json
jsonenv sent.json .result.message_id MSG

exec brigade-adapter-fs --profile bob message receive --session $BOB
json stdout .result.messages.0.message_id $MSG
json stdout .result.messages.0.kind text
json stdout .result.messages.0.delivery_state accepted
json stdout .result.messages.0.hop_count 0
json stdout .result.messages.0.sender.session_id $ALICE
json stdout .result.messages.0.sender.session_name payments-api
json stdout .result.messages.0.sender.human_label alice@example.com

# --- the same key twice is one message (C-21), a different body is a
# --- conflict (C-22) -----------------------------------------------------
stdin send.json
exec brigade-adapter-fs message send
json stdout .result.message_id $MSG
json stdout .result.duplicate true

expand send-other.in send-other.json
stdin send-other.json
status 7 brigade-adapter-fs message send
json stdout .error.code conflict
json stdout .error.details.reason idempotency_key_reused

# --- ack is idempotent and scoped to the addressed session (C-30) -------
expand ack.in ack.json
stdin ack.json
exec brigade-adapter-fs message ack --session $ALICE
json stdout .result.unknown.0 $MSG

stdin ack.json
exec brigade-adapter-fs --profile bob message ack --session $BOB
json stdout .result.acked.0 $MSG
stdin ack.json
exec brigade-adapter-fs --profile bob message ack --session $BOB
json stdout .result.acked.0 $MSG

exec brigade-adapter-fs --profile bob message receive --session $BOB
stdout '"messages":\[\]'
! stdout $MSG

# --- a caller-supplied sender member is refused before anything else ----
expand forged.in forged.json
stdin forged.json
status 3 brigade-adapter-fs message send
json stdout .error.code invalid_input
json stdout .error.details.field sender
json stdout .error.details.reason forbidden_member

# --- the uniform not_found is byte-identical (C-25) ---------------------
# A recipient of another team and a recipient that does not exist must be
# indistinguishable, or the error itself is an existence oracle.
stdin create-other.json
exec brigade-adapter-fs --profile carol team create
stdin register-c.json
exec brigade-adapter-fs --profile carol session register
cp stdout c.json
jsonenv c.json .result.session_id CAROL

expand send-foreign.in send-foreign.json
stdin send-foreign.json
status 6 brigade-adapter-fs message send
json stdout .error.code not_found
cp stdout foreign.txt

expand send-random.in send-random.json
stdin send-random.json
status 6 brigade-adapter-fs message send
cmp stdout foreign.txt

# The positive control: a DIFFERENT failure is not the same bytes, so the
# comparison above can actually fail.
stdin forged.json
status 3 brigade-adapter-fs message send
! cmp stdout foreign.txt

-- create.json --
{"team_name": "ops", "human_label": "alice@example.com"}
-- create-other.json --
{"team_name": "other", "human_label": "carol@example.com"}
-- join.in --
{"join_secret": "$SECRET", "human_label": "bob@example.com"}
-- register-a.json --
{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "payments-api", "activity": "busy", "inbound": "accept"}
-- register-b.json --
{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "billing", "activity": "idle", "inbound": "accept"}
-- register-c.json --
{"harness": "claude-code", "harness_version": "2.1.251", "session_name": "outsider", "activity": "busy", "inbound": "accept"}
-- send.in --
{"sender_session_id": "$ALICE", "recipient_session_id": "$BOB", "body": "the tenant_id migration has landed", "summary": "migration completed", "idempotency_key": "k-1"}
-- send-other.in --
{"sender_session_id": "$ALICE", "recipient_session_id": "$BOB", "body": "a different body", "idempotency_key": "k-1"}
-- ack.in --
{"message_ids": ["$MSG"]}
-- forged.in --
{"sender_session_id": "$ALICE", "recipient_session_id": "$BOB", "body": "who sent this?", "sender": {"session_id": "$BOB"}}
-- send-foreign.in --
{"sender_session_id": "$ALICE", "recipient_session_id": "$CAROL", "body": "across the team boundary"}
-- send-random.in --
{"sender_session_id": "$ALICE", "recipient_session_id": "ffffffffffffffffffffffffffffffff", "body": "across the team boundary"}
```

```console
go test ./cmd/brigade -run 'TestScript/fs-message'
```

## Wiring your adapter into the plugin

A profile carries a **default adapter**, and a session may **override** it (plan decision D36, 2026-09-02). One team
per session and one adapter per session stay as they are.

- **The profile's default.** A human creates a profile through the harness with `brigade profile init
  [--profile <p>] --adapter <name-or-command> …`. The harness records the choice beside the profile (a 0600 file in
  the user's own config directory) and then runs *your* `profile init` with the remaining arguments. A third-party
  adapter is registered once by name in `${BRIGADE_CONFIG_DIR}/adapters.json` with the form
  `--adapter <name>=<absolute path or JSON array>` (`--adapter pg=/usr/local/bin/brigade-adapter-pg`, or
  `--adapter 'pg=["/usr/local/bin/brigade-adapter-pg","--root","/srv/brigade"]'`), after which `--adapter <name>`
  alone selects it for any profile; or the command is given directly, without a name, as an absolute path or a JSON
  array. From then on, selecting the profile selects your adapter: no launch option needs to be restated, and
  `brigade profile status [--profile <p>]` names the default in force.
- **The session override.** The plugin option `adapter_command` overrides the profile's default for one session. It
  takes the same two forms — an absolute path such as `/usr/local/bin/brigade-adapter-pg`, or a JSON array whose
  elements are the executable and fixed arguments prepended verbatim to every invocation, such as
  `["/usr/local/bin/brigade-adapter-pg", "--root", "/srv/brigade"]` — or a registered name. An override that cannot
  read the profile fails at session start with `config` and one clear line; the harness never guesses.
- **The fallback.** A profile created by running an adapter directly, outside the harness, has no sidecar; the
  harness then reads the profile file's top-level `adapter` member as a registry name, best effort (the shared Go
  helper writes it; you may or may not), and finally falls back to the bundled Supabase adapter.

The harness spawns the resolved command as an argument array with `exec` and **no shell**: no globbing, no `~`
expansion, no `sh -c`, no quoting rules. A shell script is a fine adapter, but it needs a shebang line and the
executable bit — there is no shell form and no Windows form (darwin and linux only; Windows means WSL 2). The
environment your process receives is the from-scratch allow-list described under *The environment your adapter runs
in*, which is why fixed arguments and the profile file, never `BRIGADE_<ADAPTER>_*`, are where your configuration
comes from. A team lives on exactly one backend: every member's adapter must speak that backend's data model, which
the protocol deliberately leaves to adapters (4.8).

**Honest status.** The plugin manifest and the `plugin/bin` bootstrap landed with P3-1 and P1-8; the harness commands
(`profile init --adapter`, `profile status`, the `team` pass-through, `sessions`, `send`, `whoami`, `team members`), the
three hooks and the detached watcher landed with P3-3, P3-4 and P3-5, so a local build (`make plugin-dev`) drives your
adapter from inside a live Claude Code session today; the packaged release pins are still the pre-release `0.0.0`.
`brigade-conformance` and a shell remain the way to exercise your adapter on its own; the harness adds only its own
timeouts and its environment construction, which the plugin-driven session exercises.

## Before you claim conformance

- [ ] `brigade-conformance --adapter <yours>` exits **0**, and you can name the cause of **every** SKIP: a capability
      you deliberately do not advertise, or — on the `--setup` route — C-28 and C-40, which cannot provision their
      extra principals without a fixture join secret. Exit 0 with an unexplained SKIP is not conformance.
- [ ] The same run **with `--slow`** exits 0. The slow cases are the ones that prove `state` is derived from
      `lease_until` at read time.
- [ ] A run with **`--shuffle <seed>`** exits 0, for at least one non-zero seed.
- [ ] Every failing envelope carries `retryable`, present and matching its code's row (only `rate_limited` and
      `unavailable` are `true`).
- [ ] The exit status matches `error.code` on every failure, and every status is in **0..12** — never 126, 127 or
      ≥ 128.
- [ ] `not_found` is **byte-identical** for an unknown, a foreign and a not-owned id; `unauthorized` is
      byte-identical for a non-member's team and a `team_ref` that does not exist. Neither carries `details` that
      separates the cases.
- [ ] stdout is exactly one envelope and one newline for every command but `message watch`, at **every** log level.
- [ ] No secret on stderr at `debug`, none in `describe` output, none on argv, and none in any file under the run
      directory — the suite checks all four, and the last one by walking the tree at the end of the run. `team create`
      is the one command whose **stdout** may carry the secret (4.4.10); persist a digest of it, never the secret.
- [ ] Your adapter learns where its backend is from its profile file, from fixed `adapter_command` arguments or from
      an environment variable *you* name (which the suite sets with `--shared-env` or `--env`) — never by inferring
      anything from the suite's run-directory layout, which is not part of any contract.
- [ ] Nothing your adapter writes lands under the user's **project** directory; credential files are 0600 in a 0700
      directory and a group- or world-readable one is refused.
- [ ] `describe` creates no file or directory, makes no network call, never resolves or validates your backend
      location, and never returns an error envelope because of profile state.
- [ ] Every capability you advertise is backed by **every** command in its "Unlocks" column (4.7): `team.join` means
      `team leave` as well as `team join`. And no core verb still answers `internal`/`not_implemented` — `message
      receive` included, which you advertised from your first `describe` (the two allowed debts, section 6): that
      answer is for a build in progress, not for one claiming conformance.
- [ ] Your `lease` range is advertised honestly and you enforce it; every other `limits` and `retention` value is the
      protocol's v1 constant.
- [ ] You have your own tests for the four rules the suite cannot see: B-3, B-4, B-9 and B-10.
