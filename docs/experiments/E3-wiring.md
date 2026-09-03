# E3-wiring — the plugin wiring, measured end to end with `claude -p`

Date: 2026-09-03 · Ticket 15 · Task **P3-6** · Status: **closed; all four acceptance items pass** ·
Claude Code **2.1.259** · model **`claude-opus-5[1m]`** (the session default; no run needed `--model sonnet`)

The P3-6 row asks for four things to be true of `hooks.json` → `plugin/bin/brigade` → the dev pointer →
`brigade hook …` with a real Claude Code process in the middle. The plan's own form for the first of them is an
interactive `make plugin-dev`, which is P3-8's job at a keyboard; everything here was measured **headless**, with
`claude -p --output-format stream-json --verbose`, which is the same chain minus the terminal.

Nothing in this file was typed from memory: every line quoted below was read out of a capture with the `jq`
expression printed beside it.

## How every run was set up

Each run got a throwaway root and never touched the developer's own Brigade state:

```sh
RUN=<a fresh directory>
mkdir -p "$RUN"/{config/brigade,state,data,proj}
export XDG_CONFIG_HOME=$RUN/config XDG_STATE_HOME=$RUN/state XDG_DATA_HOME=$RUN/data
printf '%s\n' "$REPO/bin/brigade" > "$RUN/config/brigade/dev-binary"   # what `make plugin-dev-pointer` writes
chmod 600 "$RUN/config/brigade/dev-binary"
```

and the outer Claude Code session's environment was stripped **by prefix** — every `CLAUDE*` and `AI_AGENT*`
variable, keeping only `CLAUDE_CONFIG_DIR` (the login) — from `/bin/sh`, because zsh does not word-split an
unquoted substitution:

```sh
strip=$(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/-u \1/p' \
                     -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/-u \1/p' | grep -v -- '-u CLAUDE_CONFIG_DIR$')
exec env $strip … /bin/sh "$0"
```

**The strip is not optional even for the terminal half.** The first attempt ran the onboarding with the outer
session's variables intact and `team create` refused, correctly:

```
brigade team failed (usage): run this in your own terminal: the join secret must never pass through the chat
```

`CLAUDE_PID` is what the refusal keys on, so a "terminal" command run from inside a session is not a terminal
command. The Makefile's `$(unclaude)` does the same strip for `make plugin-dev`.

## (a) The fs onboarding, then one session

### The onboarding, settled by running it

```sh
bin/brigade profile init --adapter '["<REPO>/bin/brigade-adapter-fs"]'
bin/brigade team create --name ops --label dev --secret-file "$RUN/ops.secret"
```

Both exit **0**:

```json
{"ok":true,"protocol_version":"1","result":{"name":"default","state":"not_member","principal_ref":"1bcd…31d"}}
{"ok":true,"protocol_version":"1","result":{"team_ref":"1f60…817","team_name":"ops","principal_ref":"1bcd…31d"}}
```

and `profile init --adapter` leaves the D36 sidecar behind, which is why nothing after this needs
`adapter_command` at all:

```
$RUN/config/brigade/profiles/default/adapter = ["<REPO>/bin/brigade-adapter-fs"]
```

`team create` writes the join secret to the 0600 file and says so on **stderr** (`the join secret was written to
the file named by --secret-file (mode 0600)`); it is not on stdout and appears nowhere in this document.

A **second profile** joins that team instead of creating one. This form keeps the secret off argv — it goes from
the file straight into the request document on stdin — and was run:

```sh
bin/brigade profile init --profile third --adapter '["<REPO>/bin/brigade-adapter-fs"]'
{ printf '{"human_label":"third","join_secret":"'; tr -d '\n' < "$RUN/ops.secret"; printf '"}'; } |
  bin/brigade team join --profile third
→ {"ok":true,…,"result":{"team_ref":"1f60…817","team_name":"ops","principal_ref":"f9c0…eda","rejoined":false}}
bin/brigade team members --profile third
→ principal=1bcd…31d  dev (unverified)    joined 2026-09-03  0 sessions, seen 474s ago
  principal=f9c0…eda  third (unverified)  joined 2026-09-03  0 sessions, seen never
```

(A `profile init` on a profile that already exists is `conflict` / `profile_exists` — pass `--force` or pick a new
name. Measured on the way here.)

### The session

```sh
cd "$RUN/proj"
claude --plugin-dir <REPO>/plugin \
  --settings '{"pluginConfigs":{"brigade@inline":{"options":{"adapter_command":"[\"<REPO>/bin/brigade-adapter-fs\"]"}}}}' \
  -p --output-format stream-json --verbose --max-turns 1 'Reply with the single word ok'
```

Exit **0**; `jq -c 'select(.type=="result")|{subtype,is_error,num_turns,duration_ms}'`:

```json
{"subtype":"success","is_error":false,"num_turns":1,"duration_ms":1517}
```

**The `SessionStart` hook.** `jq -r 'select(.subtype=="hook_response")|{hook_name,exit_code,stdout}'`:

```json
{
  "hook_name": "SessionStart:startup",
  "exit_code": 0,
  "stdout": "Brigade: this session is \"proj-be\" (45ea2a112278b47f350ff3ca53902882) in team \"ops\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`; terminal commands: /Users/rjae/…/plugin/bin/brigade\n"
}
```

One line, exit 0, and it names the **fs** team `ops`. The hook's stderr in the same record is the watcher's own
`{"level":"INFO","msg":"watcher started","watcher_pid":76521}` — diagnostics, never stdout.

`jq -c 'select(.subtype=="init")|{plugins,mcp_servers,messaging_socket_path}'` confirms the rest of the wiring:

```json
{"plugins":[{"name":"brigade","source":"brigade@inline","version":"0.0.0"}],
 "mcp_servers":[],"messaging_socket_path":"/tmp/cc-socks/77554.sock"}
```

**The three files, with a live watcher.** The by-pid map and the pidfile were copied out *while the session ran*
(a 20 ms poll), because `SessionEnd` removes both — see the timing below.

```json
// $RUN/state/brigade/sessions/by-pid/77554.json
{"claude_pid":77554,"claude_session_id":"61bf75da-…","brigade_session_id":"45ea2a11…","team_ref":"1f60…817",
 "team_name":"ops","session_name":"proj-be","non_interactive":true,"inbound":"accept",
 "socket_path":"/tmp/cc-socks/77554.sock","profile":"default",
 "config_dir":"…/run-a/config/brigade","adapter_command":["<REPO>/bin/brigade-adapter-fs"],
 "plugin_bin":"<REPO>/plugin/bin/brigade","harness_version":"2.1.259","registered_at":"2026-09-03T07:29:18…"}

// $RUN/state/brigade/watchers/77554.json     (pid 77587 was alive: `kill -0` succeeded at capture time)
{"pid":77587,"start_token":"1788434959.000412","brigade_session_id":"45ea2a11…",
 "socket_path":"/tmp/cc-socks/77554.sock","token_sha256":"8caf…d60d"}

// $RUN/state/brigade/sessions/by-native/61bf75da-….json   (kept after the session ends)
{"brigade_session_id":"45ea2a11…","team_ref":"1f60…817","session_name":"proj-be","updated_at":"…"}
```

No messaging token appears in any of them; the pidfile carries only its SHA-256.

**Did `SessionEnd` stop the watcher, or did we have to?** It did, and with room to spare. Epoch seconds from the
poll loop:

| event | t |
| --- | --- |
| `claude` started | 1788434958.789 |
| watcher pidfile present, pid alive | 1788434959.060 (+0.271 s) |
| watcher pidfile **gone** | 1788434960.828 |
| `claude` process exited | 1788434961.189 |

The pidfile disappeared **0.361 s before** `claude` exited — E0-5's "`SessionEnd` fires ~0.5 s before exit" seen
from the other side. No SIGTERM from the harness of this experiment was needed, and none was sent; `ps` after
every run showed no `brigade watch` and no `brigade-adapter-fs` of ours.

**The `UserPromptSubmit` hook.** This one has a measurement trap in it. With the map written, the watcher alive,
no notice pending and `poll_on_prompt` off, the prompt hook **prints nothing and exits 0 — and then appears in no
capture at all**: not in `stream-json` (P3-1 already measured that only `SessionStart` produces hook events
there), not on the process stderr (0 bytes), and **not in the transcript JSONL** either. `--debug hooks` added
nothing: stderr stayed empty under `-p`. Claude Code appears to record a hook attachment only when the hook
produced output.

So the run plants one line in the file the prompt hook is specified to print once — `${stateDir}/state/<CLAUDE_PID>.notice`,
0600, written immediately after the `claude` process was backgrounded (its pid *is* `CLAUDE_PID`, confirmed by the
watcher log name `watcher-77554.log` and the socket path) — and the hook then shows up, having also proved the
notice path works:

```sh
jq -c '[.. | objects | select(has("hookEvent"))] | .[] | {type,hookEvent,command,exitCode,content}' \
  "$CLAUDE_CONFIG_DIR"/projects/<mangled cwd>/*.jsonl
```

```json
{"type":"hook_success","hookEvent":"SessionStart","command":"Connecting to the Brigade team","exitCode":0,
 "content":"Brigade: this session is \"proj-be\" (45ea…) in team \"ops\"; …"}
{"type":"hook_success","hookEvent":"UserPromptSubmit","command":"${CLAUDE_PLUGIN_ROOT}/bin/brigade hook prompt",
 "exitCode":0,"content":"Brigade: P3-6 acceptance notice (planted so the prompt hook has something to print)"}
```

Two facts for P3-7 and P3-8 in that pair: the `SessionStart` record's `command` is the **`statusMessage`**, not the
command line (P3-1's finding, reproduced), and the `UserPromptSubmit` record's `command` keeps
`${CLAUDE_PLUGIN_ROOT}` **unsubstituted**, so a smoke check must not match hook records on a resolved path.

**The cold-cache path is not exercised, and that is by design.** The bootstrap execs the dev pointer before it
looks at the cache or the network (`plugin/bin/brigade`, step 4 before step 5), so no download branch runs with a
pointer present. Evidence rather than reading: after the whole run, `find "$RUN/data" -type f` is **empty** — the
cache directory `${XDG_DATA_HOME}/brigade/bin` was never even created. In the pre-release `0.0.0` state there is
nothing to download anyway, which is why the pointer is the only way a developer's session can run at all.

## (b) `make plugin-check plugin-validate`

```
$ make plugin-check                                    # exit 0
ok: plugin/ contains only allow-listed paths
ok: plugin/bin/brigade is 100755 in git and nothing else under plugin/ is executable
ok: plugin/bin/VERSION (0.0.0) matches plugin/.claude-plugin/plugin.json
ok: no mcpServers, no channels and no --channels anywhere under plugin/ (.mcp.json: check 1)
ok: plugin/hooks/hooks.json is exec form and every hook command exists and is executable
ok: every hooks.json "args" pair names a brigade hook subcommand that exists (prompt session-end session-start)
ok: plugin/README.md has a Status section and no 'not implemented' disclaimer
ok: plugin/bin/brigade parses under: sh bash zsh dash
ok: shellcheck -s sh clean: plugin/bin/brigade scripts/ci/bootstrap-alpine.sh scripts/ci/checksums-check.sh
    scripts/ci/conformance-setup-supabase.sh scripts/ci/no-secrets.sh scripts/ci/plugin-check.sh
    scripts/ci/release-verify.sh scripts/harness-smoke.sh scripts/release-prep.sh
plugin-check: all checks passed
no-secrets: scanned 479 tracked/plugin/built file(s) … clean

$ make plugin-validate                                 # exit 0
claude plugin validate .            → ✔ Validation passed
claude plugin validate ./plugin --strict → ✔ Validation passed
```

Checks 6 and 7 are this task's additions. Both are textual, because `plugin-check.sh` must run on a host with no
Go toolchain and no `claude`, and both were proven able to fail by mutating the script and watching
`go test ./scripts/ci/` go red, then restoring the file byte-for-byte (`cmp` clean):

| mutation | test that went red |
| --- | --- |
| check 6's `[ "$found" = yes ]` forced true | `a hooks.json naming a subcommand the binary does not implement fails` |
| check 6's `args_seen` guard deleted | `a hooks.json with no args array at all fails` |
| check 7's disclaimer pattern made unmatchable | `a plugin README that still says the wiring is not implemented fails`, `…is not runnable fails` |
| `hook_subcommands` widened by one word | `TestHookSubcommandListMatchesTheGoConstants` |

That last test is the join between the shell list and the Go code: it parses `hook_subcommands="…"` out of the
script and asserts it equals `{hook.SubSessionStart, hook.SubPrompt, hook.SubSessionEnd}`, so renaming a
subcommand in Go fails the build rather than silently leaving the shell check asserting a fiction.

`make lint` is unaffected (0 issues, mutant build tags included) and `go test ./scripts/ci/` is green.

## (c) Two profiles, mixed adapters, and no `adapter_command` anywhere

The row asks for two profiles on one machine bound to **different** adapters, selected by the `profile` option
alone. Brigade has exactly two adapter implementations and the second needs a Supabase project, so "a different
adapter" is realised here in the only form available without a second backend: the same executable **registered
under a name with a fixed `--root`**, which makes it a different *store* — an independent backend with its own
team, principals and sessions, reached through a different resolution path (name → `adapters.json` → argv).

```sh
# fsa: a bare JSON array; the fs adapter's DEFAULT root ${BRIGADE_STATE_DIR}/fs-adapter
bin/brigade profile init --profile fsa --adapter '["<REPO>/bin/brigade-adapter-fs"]'
bin/brigade team create --profile fsa --name ops  --label alpha --secret-file "$RUN/ops.secret"

# fsb: a REGISTERED NAME whose command carries a fixed --root, i.e. a different store
bin/brigade profile init --profile fsb --adapter 'fsdev=["<REPO>/bin/brigade-adapter-fs","--root","'"$RUN"'/store2"]'
bin/brigade team create --profile fsb --name beta --label bravo --secret-file "$RUN/beta.secret"
```

All four exit 0, and the registration lands where D36 says:

```json
// $RUN/config/brigade/adapters.json
{"fsdev":["<REPO>/bin/brigade-adapter-fs","--root","<RUN>/store2"]}
```
```
fsa sidecar: ["<REPO>/bin/brigade-adapter-fs"]
fsb sidecar: fsdev
```

**In a terminal**, `profile status` names the default adapter and where the choice came from, then passes through
to the adapter's own answer:

```
$ bin/brigade profile status --profile fsa
profile fsa: default adapter ["<REPO>/bin/brigade-adapter-fs"] (from sidecar)
{"ok":true,…,"result":{"name":"fsa","state":"joined","team_ref":"a272…972","team_name":"ops","human_label":"alpha"}}

$ bin/brigade profile status --profile fsb
profile fsb: default adapter fsdev (from sidecar)
{"ok":true,…,"result":{"name":"fsb","state":"joined","team_ref":"d8c9…d48","team_name":"beta","human_label":"bravo"}}
```

One marker session was registered in each store beforehand (`alpha-marker`, `beta-marker`) so that a roster
leaking across stores would be visible rather than merely absent.

**The two sessions.** Each `--settings` carries the `profile` option and **nothing else** — no `adapter_command`:

```sh
claude --plugin-dir <REPO>/plugin \
  --settings '{"pluginConfigs":{"brigade@inline":{"options":{"profile":"fsa"}}}}' \
  --allowedTools "Bash(brigade:*),Skill" \
  -p --output-format stream-json --verbose --max-turns 4 \
  'Run `brigade sessions` and paste its output verbatim.'
```

Both exit 0 (`{"subtype":"success","is_error":false,"num_turns":3,"duration_ms":9897}` and `…,8825`). Each start
line names **its own** team:

```
fsa: Brigade: this session is "proj-a-2c" (af27255f3132379874a47d239d5aedaf) in team "ops";  inbound: accept; …
fsb: Brigade: this session is "proj-b-f8" (6b7dbe3dc6a72b22aed0cdc0a9b79798) in team "beta"; inbound: accept; …
```

and each model's verbatim `brigade sessions` lists only its own team's sessions:

```
fsa  af27255f3132379874a47d239d5aedaf  proj-a-2c     alpha (unverified)  idle  inbound=accept  … (this session)
     f741edaa78909ce9648e22f277545eb1  alpha-marker  alpha (unverified)  idle  inbound=accept  … seen 10s ago

fsb  6b7dbe3dc6a72b22aed0cdc0a9b79798  proj-b-f8     bravo (unverified)  idle  inbound=accept  … (this session)
     f11a6d7e113b5793831ae78507cd27d1  beta-marker   bravo (unverified)  idle  inbound=accept  … seen 23s ago
```

The independent check that the fixed `--root` was really honoured is on disk: the fsb session's record was written
into the **second** store, and neither store holds the other's sessions.

```
$RUN/state/brigade/fs-adapter/teams/*/sessions/  → proj-a-2c, alpha-marker      (team "ops")
$RUN/store2/teams/*/sessions/                    → proj-b-f8, beta-marker       (team "beta")
```

**A finding worth carrying into P3-8.** In both runs the model's *first* attempt was
`/Users/…/plugin/bin/brigade sessions` — the absolute path the start line prints as `terminal commands:` — and the
`Bash(brigade:*)` grant does not match it, so it was denied; the model then ran the bare `brigade sessions` and
succeeded. Nothing is broken (the deny is exactly the plan's rule against full-path invocations), but the start
line's own text is what tempts the model into the denied form, at the cost of one wasted turn per session.

## (d) An `adapter_command` override that cannot read the profile

Two shapes, one run each, `--max-turns 1`.

**d1 — the override cannot be resolved at all** (`adapter_command` is a relative path, which D36 refuses without
ever spawning anything):

```sh
--settings '{"pluginConfigs":{"brigade@inline":{"options":{"adapter_command":"bin/brigade-adapter-fs"}}}}'
```

```
claude exit 0
SessionStart hook_response: exit_code 0
  Brigade: not connected (config): the adapter for profile "default" could not be resolved from option; run `brigade profile status` in a terminal
$RUN/state/brigade/watchers        → empty (no watcher)
$RUN/state/brigade/sessions/by-pid → empty (no map)
```

That is the documented D36 line, verbatim, and the option's **value is not echoed** — only the word `option`.

**d2 — the override resolves, runs, and cannot read the profile** (profile `ghost`, which does not exist; the
adapter's `describe` still answers, because `describe` never fails on profile state, and `session register` is
where it breaks):

```sh
--settings '{"pluginConfigs":{"brigade@inline":{"options":{"profile":"ghost","adapter_command":"[\"<REPO>/bin/brigade-adapter-fs\"]"}}}}'
```

```
claude exit 0
SessionStart hook_response: exit_code 0
  Brigade: not connected (config: profile_missing); run `brigade team join` in a terminal
$RUN/state/brigade/watchers        → empty (no watcher)
$RUN/state/brigade/sessions/by-pid → empty (no map)
```

`profile_missing` is the fs adapter's own fixed `details.reason`, carried into the line by the P3-3/P3-4
integration fix; the generic `not connected (config)` of an unreadable stdin is now distinguishable from an
adapter that answered `config`.

## Also measured

### The shadowing warning, through the real hook

A decoy `brigade` (a shell script printing a marker) was put **first** on the nested session's `PATH`. The
`SessionStart` stdout then carries two lines — the start line, and the warning as its own line after it, exactly
as P3-4 decided:

```
Brigade: this session is "proj-85" (cafd90754b8202ecce67e0c9a957a3de) in team "ops"; inbound: accept; …
Brigade: another `brigade` at <RUN>/decoy/brigade shadows the plugin's; remove it or the wrong version runs
```

And the Bash tool really does run the decoy. Told plainly to use the bare name, the model's tool result was:

```
Bash :: brigade sessions
→ DECOY-BRIGADE-MARKER-8Q2 was run instead of the plugin's brigade: sessions
```

In the first, undirected run the model **read the warning and refused to run the bare command**, offering to use
the plugin path or to have the decoy removed — the warning works on its intended reader, which is worth knowing
before P3-8 tries to reproduce a decoy interactively.

**Which symlinks warn.** The execution log's hand-off note says a `brigade` symlinked into `~/.local/bin` — the
setup skill's own suggestion — warns every session. Measured directly against the real hook (no model involved),
that is true only of the symlink that does *not* point at the plugin's bootstrap:

| `PATH` head | resolves to | start-up output |
| --- | --- | --- |
| `…/binA/brigade` | `<REPO>/plugin/bin/brigade` (what the setup skill suggests) | start line only, **no warning** |
| `…/binB/brigade` | `<REPO>/bin/brigade` (a developer's own build) | start line **+ the shadow warning** |

So the skill's sentence needs no change, and a developer who symlinks their own build should expect the warning
every session. Both READMEs this task touches now say so.

### Where a silent hook is invisible

Collected in one place, because a smoke test that asserts the wrong source will pass for the wrong reason:

| Hook | Where its exit status can be read |
| --- | --- |
| `SessionStart` | `stream-json` `system/hook_started` + `system/hook_response`, **and** the transcript JSONL |
| `UserPromptSubmit` | the transcript JSONL **only, and only when the hook printed something** |
| `SessionEnd` | the process's stderr, **only when it failed** — a clean run's stderr was 0 bytes |

`--debug hooks` added nothing under `-p`.

## Contradictions with the brief and the plan

1. **The brief's suggested (d) shape does not produce `config`.** It proposes the fs binary with `--root` pointing
   at an EMPTY directory for a bound profile. Measured directly (no session needed): `describe` succeeds (it reads
   local files only, not the store), and `session register` answers **`unauthorized` / "not an active member of
   this team"**, exit 5 — because the profile's `team_ref` names a team that store has never heard of. The hook
   would print `Brigade: not connected (unauthorized); …`, not a `config` line. The brief's own escape hatch ("or
   any mismatch that makes `describe`/`register` fail with `config`") is what d2 uses.
2. **The brief's (a) assertion "the transcript JSONL shows the `UserPromptSubmit` hook with exit 0" is not true of
   a hook that prints nothing** — the record does not exist. See above; the run makes the hook print, and says so.
3. **The execution log's shadowing note is imprecise** — see the table above: only a symlink that does not resolve
   to `plugin/bin/brigade` warns.

## Limits — what this does not prove

- Nothing here is interactive. `/clear`, `/compact`, `/rename`, the Manual-mode skill dialog and a real
  `make plugin-dev` at a keyboard are P3-8's, and the `-p` runs are all `permissionMode: "default"`,
  `non_interactive: true`.
- No message was sent or injected in any run; message flow end to end is P3-7's `scripts/harness-smoke.sh`
  (`docs/experiments/E3-smoke.md`).
- The "different adapter" of item (c) is one binary in two stores, not two implementations. A genuinely mixed pair
  (fs + Supabase on one machine) waits for a Supabase project and is Phase 4's shape.
- The download half of the bootstrap is *not* exercised — deliberately, and that is item (a)'s point — so nothing
  here says anything about first-use timing. That is E0-8 (a) and the D1 release rehearsal.
- Every measurement is macOS 26 / arm64, Claude Code 2.1.259, one machine.

## Teardown

Each run's temporary root (`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_DATA_HOME`, the project directories and both
fs stores) lives under a scratch directory outside the repository and outside the developer's home; the developer's
own `${XDG_CONFIG_HOME:-~/.config}/brigade/dev-binary`, `~/.local/state/brigade` and `~/.local/share/brigade` were
never written (the first two do not exist on this machine, and `make plugin-dev` was only ever dry-run with
`make -n`). Each nested session also writes a transcript directory under `$CLAUDE_CONFIG_DIR/projects/` keyed by
its temporary cwd; those were removed by absolute path after the measurements were read out of them. `ps` after
the last run showed no `brigade watch`, no `brigade hook` and no `brigade-adapter-fs` process, and every run's
`watchers/` directory was empty.
