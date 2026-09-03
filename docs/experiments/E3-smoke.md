# E3-smoke — the headless harness smoke (`make harness-smoke`)

Date: 2026-09-03 · Ticket 15 · Status: **green, 3/3 runs** · Driver: `scripts/harness-smoke.sh` · Claude Code 2.1.259
Model: `claude-opus-5[1m]` (the session default; no `--model` was needed) · macOS 25.6.0 (darwin/arm64)

**One real `claude -p` session, the real plugin, the real hooks, the real detached watcher and the fs adapter as the
backend, plus a second principal driven from the script's own terminal, completed the whole Brigade round trip three
times out of three with every assertion green and nothing left behind.** The model listed its teammates, sent a
teammate a message, was interrupted mid-turn by that teammate's reply, and answered it with `brigade send …
--reply-to` — using only the bare `brigade` on the Bash tool's PATH and never the native `SendMessage`.

This is the fs variant of E2E-01 (plan row P3-7) and the thing the unit and `internal/harness/e2e` suites cannot
reach: those drive `bin/brigade hook …` themselves, while this run makes **Claude Code** fire the hooks.

## Running it

```sh
make harness-smoke          # builds first, then $(unclaude) scripts/harness-smoke.sh
```

Needs a logged-in Claude Code and `jq`; never runs in CI (it costs model calls). It refuses to start if a `brigade`
is already on `PATH`, because the plugin's `bin/` is appended **last** to the Bash tool's PATH
(`docs/research/plugin-bootstrap-cli.md`, finding 1) and any other `brigade` would silently be the one under test.

Everything lives under one `mktemp -d` root: its own `XDG_CONFIG_HOME` (holding the dev-binary pointer),
`XDG_STATE_HOME` (the fs store, the session maps, the watcher pidfile and log) and `XDG_DATA_HOME`, its own project
directory, and its own Claude config dir for the second principal's hook. The user's real `CLAUDE_CONFIG_DIR` is kept
for the `claude` process alone — the login lives there — and the transcript directory that run creates under its
`projects/` is removed by absolute path at the end. The real `~/.config/brigade/dev-binary`, `~/.local/state/brigade`
and `~/.local/share/brigade` are never read or written. The inherited session environment is stripped **by prefix**
(`CLAUDE*` — no underscore, so `CLAUDECODE` too — plus `AI_AGENT*`, keeping only `CLAUDE_CONFIG_DIR`).

## What the run does

1. **Onboarding, from the terminal** (`CLAUDE_PID` unset, so `team`/`profile` pass through to the adapter with
   inherited stdio):

   ```sh
   brigade profile init --profile alice --adapter '["<repo>/bin/brigade-adapter-fs"]'
   brigade profile init --profile bob   --adapter '["<repo>/bin/brigade-adapter-fs"]'
   echo '{"team_name":"smoke","human_label":"…"}' | brigade team create --profile alice --secret-file <0600 file>
   jq -Rn --arg l "…" '{join_secret: input, human_label: $l}' < <that file> | brigade team join --profile bob
   ```

   The join secret goes from the adapter's 0600 file straight into the join document on stdin; it never reaches a
   shell variable, argv, a log or this report.

2. **bob's session is registered through the REAL hook**, not through the adapter directly — form (a) of the brief,
   so the shipped path is exercised and bob appears in the roster:
   `CLAUDE_PID=<a sleeper the script owns> CLAUDE_PLUGIN_OPTION_PROFILE=bob … brigade hook session-start` with the
   hook stdin document and **no** `CLAUDE_CODE_MESSAGING_SOCKET`. No watcher is spawned for him (asserted).
   His session name comes from `basename(cwd)` of the hook document: `bob-session`.

3. **The headless session**:

   ```sh
   cd <temp project dir> && env <strip> XDG_CONFIG_HOME=… XDG_STATE_HOME=… XDG_DATA_HOME=… \
     claude -p "<prompt>" \
       --plugin-dir <repo>/plugin \
       --settings '{"pluginConfigs":{"brigade@inline":{"options":{"profile":"alice","adapter_command":"[\"<repo>/bin/brigade-adapter-fs\"]"}}}}' \
       --allowedTools "Bash(brigade:*),Bash(sleep:*),Skill" \
       --output-format stream-json --verbose --max-turns 8
   ```

   The prompt asks for four Bash calls, one per turn: `brigade sessions`; `brigade send <bob> --summary "smoke"` with
   a quoted heredoc body; `sleep 20`; then "follow the reply instruction inside the Brigade message that arrived".
   `Bash(sleep:*)` is in the allow-list only for step 3 — it is the tool-call boundary the mid-turn injection needs.

4. **The mid-turn interruption**: the script polls the fs store and, the moment the model's own message lands in
   bob's inbox (i.e. the model is now on `sleep 20`), sends bob's message with
   `CLAUDE_PID=<bob's sleeper> brigade send <alice's id> --summary "from bob" --json`. The adapter's default root
   `${BRIGADE_STATE_DIR}/fs-adapter` is what makes the two principals share a store: `BRIGADE_STATE_DIR` is one of
   the four variables the harness computes for every adapter child, and both principals resolve it from the same
   temporary `XDG_STATE_HOME`. **`BRIGADE_FS_ROOT` is deliberately not used** — the harness builds every child
   environment from scratch, so it would not arrive in the session anyway (`internal/adapters/fs/README.md`).

## The three runs

| Run | Started | Wall | `claude` exit | `num_turns` | `duration_ms` | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 2026-09-03 07:41 EDT | 31 s | 0 | 5 | 28908 | **GREEN** — 19 `ok:`, 0 `FAIL:` |
| 2 | 2026-09-03 07:42 EDT | 31 s | 0 | 5 | 29216 | **GREEN** — 19 `ok:`, 0 `FAIL:` |
| 3 | 2026-09-03 07:42 EDT | 29 s | 0 | 5 | 28926 | **GREEN** — 19 `ok:`, 0 `FAIL:` |

`make harness-smoke` exited 0 on all three. Model on all three: `claude-opus-5[1m]` (the session default; the plan's
fallback to `--model sonnet` was never needed — the model followed the four-step prompt exactly every time).
The wall column is the `claude -p` process alone (start to exit, measured by the script); `make build`, the
onboarding and the assertions are on top of it and are not model-bound.

**Zero flakes across the three runs and the two development runs before them (five nested sessions in total).** All
five produced the same command sequence and the same injection; the two development runs were red only on assertions
that were looking in the wrong place (below), never on the behaviour.

## What each assertion reads

The fifteen checks below print nineteen `ok:` lines: the three onboarding steps of section 1–2 above each
print one, and check 15 prints two (the pidfile and the process).

| # | Assertion | Source and expression |
| --- | --- | --- |
| 1 | `SessionStart` fired, exit 0, context line names team `smoke` | stream: `select(.type=="system" and .subtype=="hook_response" and .hook_event=="SessionStart" and .exit_code==0) \| .stdout`, prefix-matched, and its `(<id>)` compared with the by-pid map |
| 2 | a LIVE detached watcher served the session | `${XDG_STATE_HOME}/brigade/watchers/<CLAUDE_PID>.json` read **while the session ran** |
| 3 | `UserPromptSubmit` ran and exited 0 | the by-pid map's `permission_mode`, read while the session ran (see below) |
| 4 | no hook failed | transcript: no `attachment` of type `hook_non_blocking_error`, none with `hookEvent != null and exitCode != 0` |
| 5 | the model ran `brigade sessions` | stream: an assistant `tool_use` `Bash` whose `.input.command`, taken whole, `startswith("brigade sessions")` (a jq predicate per command, not a grep over the collected command LINES: those include heredoc bodies) |
| 6 | the model ran `brigade send <bob>` | same predicate, `startswith("brigade send <bob's id>")` |
| 7 | that body reached bob's inbox | fs store: `<root>/teams/<team_ref>/inbox/<bob>/*.json`, `.body \| contains($model_body)` |
| 8 | bob's frame was injected mid-turn, attributed to bob | transcript: `attachment.type=="queued_command"`, its `.attachment.prompt` frame line matched on `team="smoke"` and `from-name="bob-session"`, `.attachment.origin` reported |
| 9 | it is the message bob sent | the frame's `message-id` == the `message_id` bob's `send --json` returned |
| 10 | the watcher acknowledged it | fs store: the envelope is under `acked/<alice>/`, named `*.<message id>.json` |
| 11 | the model replied with `--reply-to` | stream: ONE `Bash` command that both `startswith("brigade send <bob's id>")` and `contains("--reply-to <bob's message id>")` |
| 12 | no native `SendMessage` anywhere | stream: zero `tool_use` with `.name=="SendMessage"` |
| 13 | no `brigade` through a path or a shell | the collected commands contain neither `/brigade` nor `sh -c` |
| 14 | the session succeeded | stream: `select(.type=="result") \| .is_error == false` |
| 15 | nothing survives | no pidfile left under `watchers/`; the watcher pid is gone |

## Measured facts, and two corrections to P3-1's note

P3-1 recorded that `UserPromptSubmit` "appears only in the transcript JSONL as
`attachment.type == "hook_non_blocking_error"` (or its success twin)". **That is true only of a hook that FAILS or
prints something.** Measured here on 2.1.259, with all three hooks exiting 0 and only `SessionStart` printing:

1. **A hook that exits 0 and prints nothing is recorded in NO source at all** — not `stream-json`, not the
   transcript, not the process stderr. Across all five nested sessions the transcript's complete set of hook
   attachments was exactly one line:

   ```json
   {"t":"hook_success","e":"SessionStart","x":0,"c":"Connecting to the Brigade team"}
   ```

   and the stream's complete set of hook events was exactly two:

   ```json
   {"subtype":"hook_started","hook_name":"SessionStart:startup","exit_code":null}
   {"subtype":"hook_response","hook_name":"SessionStart:startup","exit_code":0}
   ```

   The hooks did all fire. Proven directly: a development run pointed the dev-binary pointer at a wrapper that
   logged every invocation's argv and stdin before exec'ing the real binary, and it recorded
   `hook session-start`, `hook prompt` and `hook session-end` exactly once each, in that order, plus the three
   Bash-tool `sessions`/`send`/`send --reply-to` calls. So **"assert `UserPromptSubmit` exit 0 in the transcript"
   (the P3-7 row and the brief) is not measurable as written**; the smoke asserts the hook's *effect* instead.

2. **The prompt hook's effect is the by-pid map's `permission_mode`.** The same wrapper log shows Claude Code sends
   `permission_mode` on `UserPromptSubmit` and **not** on `SessionStart`:

   ```text
   === argv: hook session-start
   stdin: {"session_id":"…","transcript_path":"…","cwd":"…/proj","hook_event_name":"SessionStart","source":"startup"}
   === argv: hook prompt
   stdin: {"session_id":"…","transcript_path":"…","cwd":"…/proj","prompt_id":"…","permission_mode":"default","hook_event_name":"UserPromptSubmit","prompt":"…"}
   === argv: hook session-end
   stdin: {"session_id":"…","transcript_path":"…","cwd":"…/proj","prompt_id":"…","hook_event_name":"SessionEnd","reason":"other"}
   ```

   `internal/harness/hook/prompt.go` is therefore the only writer of that member in this run, and the map read
   mid-session carries `"permission_mode":"default"` in all three runs. The read has to happen **while the session
   runs**: the `SessionEnd` hook deletes the by-pid map.

3. **An injected message's transcript shape** (6.11 says it is not a `stream-json` event; it is also not a `user`
   record, which is what the brief's "a user message carrying `<brigade-message`" expected). It is a
   `queue-operation`/`enqueue` **and** an `attachment` of type `queued_command`, and Claude Code then removes it from
   the queue with a named reason:

   ```json
   {"operation":"enqueue","reason":null,"head":"Brigade smoke test. Use ONLY the Bash to"}
   {"operation":"dequeue","reason":null,"head":""}
   {"operation":"enqueue","reason":null,"head":"<cross-session-message from-name=\"bob-se"}
   {"operation":"remove","reason":"absorbed_mid_turn","head":"<cross-session-message from-name=\"bob-se"}
   ```

   `absorbed_mid_turn` is Claude Code's own word for what 6.11 calls mid-turn injection, and it is the cleanest
   single piece of evidence that the interruption landed inside the turn rather than after it.

4. **Variant C works as E0-3 predicted.** The `queued_command` attachment carries an `origin` object that consumes
   the native wrapper into an attribution and leaves the inner frame untouched for the model:

   ```json
   {"kind":"peer","from":"unknown","name":"bob-session","body":"<brigade-message team=\"smoke\" …>"}
   ```

   `origin.name` is the wrapper's `from-name`; `origin.kind` is `peer`; `origin.from` is `unknown` (the native tool
   has no principal for a Brigade sender, which is exactly why `from-name` is documented as cosmetic).

## Verbatim excerpts (run 1)

The `SessionStart` hook response — note that the hook's own stderr diagnostic travels with it, and that the
`statusMessage` replaces the command in the transcript's copy (P3-1 finding 4, reconfirmed):

```json
{"hook_name":"SessionStart:startup","hook_event":"SessionStart","exit_code":0,
 "stdout":"Brigade: this session is \"proj-d6\" (09365acd151367f8cdd1d56e8480423c) in team \"smoke\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`; terminal commands: /Users/rjae/Development/appshapes/brigade/plugin/bin/brigade\n",
 "stderr":"{\"time\":\"2026-09-03T07:41:15.759026-04:00\",\"level\":\"INFO\",\"msg\":\"watcher started\",\"watcher_pid\":87696}\n"}
```

The by-pid map and the watcher pidfile, both read while the session ran (the map carries the resolved
`adapter_command` and the socket path; the pidfile carries only the SHA-256 of the messaging token, never the token):

```json
{"brigade_session_id":"09365acd151367f8cdd1d56e8480423c","team_name":"smoke","profile":"alice",
 "session_name":"proj-d6","inbound":"accept","permission_mode":"default",
 "socket_path":"/tmp/cc-socks/87668.sock",
 "adapter_command":["/Users/rjae/Development/appshapes/brigade/bin/brigade-adapter-fs"]}
{"pid":87696,"brigade_session_id":"09365acd151367f8cdd1d56e8480423c",
 "socket_path":"/tmp/cc-socks/87668.sock","token_sha256":"c852279caf7e…"}
```

Every Bash command the model ran, in order — four calls, all bare `brigade` or `sleep`, the send bodies in quoted
heredocs (the form E0-8 proved `Bash(brigade:*)` admits):

```text
brigade sessions
brigade send 33f09c73e250936e1f36e91501f1bff4 --summary "smoke" <<'EOF'
hello bob, this is the smoke test
EOF
sleep 20
brigade send 33f09c73e250936e1f36e91501f1bff4 --summary "reply" --reply-to 5c175941fc34be389bb686129e43fe88 <<'EOF'
got your mid-turn message, bob - smoke test reply from alice
EOF
```

The injected frame's tag line, exactly as the model saw it:

```text
<brigade-message team="smoke" message-id="5c175941fc34be389bb686129e43fe88" reply-to-session-id="33f09c73e250936e1f36e91501f1bff4" from-principal="b6aaf7cccbf2afcf0455a8fe6077d1b8" from-name="bob-session" from-label="bob@smoke.invalid (unverified)" hops="1" sent-at="2026-09-03T11:41:20Z">
```

The fs store at the end of the run — bob's inbox holds the model's first message and its reply (unacknowledged: bob
has no watcher), and alice's acknowledged directory holds bob's message, moved there by her watcher:

```text
inbox/33f09c73…  1788435680356306000.5fb4dacdf8cfcd1dea4eea47279153c3.json   (hop 0, the model's "smoke")
                 1788435704110725000.583c1fe03a5a47719ee21a8d7008aad2.json   (hop 2, reply_to bob's message)
acked/09365acd…  1788435680697005000.5c175941fc34be389bb686129e43fe88.json   (hop 1, bob's message)
```

The watcher's last two log lines (its own clean exit path after the `SessionEnd` hook SIGTERMed it; `removed:false`
because the hook had already compare-then-deleted the pidfile):

```json
{"time":"2026-09-03T07:41:44.991208-04:00","level":"INFO","msg":"watcher exiting","exit":0,"reason":"signal","queued":0,"pending":0,"senders":1,"deferrals":1,"seen":1,"notices":0}
{"time":"2026-09-03T07:41:44.99124-04:00","level":"INFO","msg":"pidfile released","removed":false}
```

And the session result:

```json
{"subtype":"success","is_error":false,"num_turns":5,"duration_ms":28908}
```

## `hops="1"` on bob's first message is correct, not a defect

Bob sent with no `--reply-to`, yet the frame reads `hops="1"` and the model's own reply came back as hop 2. That is
protocol 4.5.12: when `reply_to` is absent the adapter computes "one more than the `hop_count` of the most recent
message from that recipient within the implicit reply window — an unlabelled answer is still an answer". Bob had
just received the model's hop-0 message, so his answer is hop 1 and the labelled reply to it is hop 2. The
`internal/harness/e2e` fixture sees hop 0 there only because its bob sends *first*.

## Teardown, and what is left behind

Nothing. Each run printed, and each was verified afterwards with `ps` and `ls`:

```text
teardown: bob's sleeper (87658) is gone
teardown: removing the transcript directory /Users/rjae/.claude-ifthen/projects/-private-var-folders-…-brigade-smoke-QMRsyD-proj
teardown: removing the temp root /private/var/folders/…/T/brigade-smoke-QMRsyD
```

After all three runs: no `brigade-smoke-*` temp root, no `*brigade-smoke*` transcript directory under the user's
`CLAUDE_CONFIG_DIR/projects/`, no watcher or sleeper process, and the user's real `~/.config/brigade`,
`~/.local/share/brigade` and `~/.local/state/brigade` untouched (the first two do not exist on this machine; the
third holds only an unrelated August research file, with its August mtime intact).

The watcher pidfile is removed twice over: the `SessionEnd` hook compare-then-deletes it and SIGTERMs the watcher,
and the watcher's own exit path would remove it if the hook had not (`removed:false` above says the hook won the
race). The script's `cleanup` trap SIGTERMs any watcher still named by a pidfile as a backstop; it never had to.

## Gates

```text
$ shellcheck -s sh scripts/harness-smoke.sh        # exit 0
$ sh -n / bash -n / zsh -n / dash -n               # all exit 0
$ git ls-files -s scripts/harness-smoke.sh
100755 …  0  scripts/harness-smoke.sh
$ make plugin-check                                 # exit 0; its shellcheck list now names the script:
ok: shellcheck -s sh clean: plugin/bin/brigade scripts/ci/bootstrap-alpine.sh scripts/ci/checksums-check.sh
    scripts/ci/conformance-setup-supabase.sh scripts/ci/no-secrets.sh scripts/ci/plugin-check.sh
    scripts/ci/release-verify.sh scripts/harness-smoke.sh scripts/release-prep.sh
```

## Honest limits

- **Three runs on one machine, one model, one Claude Code version.** Everything here is darwin/arm64,
  Claude Code 2.1.259, `claude-opus-5[1m]`. The transcript shapes (`queued_command`, `absorbed_mid_turn`,
  `origin.kind`) are Claude Code internals with no stability promise; the script asserts on them because they are
  the only place the injection is visible, and a future release that renames them will fail assertion 8 loudly
  rather than silently.
- **The model's cooperation is part of the subject.** Assertions 5, 6, 11 and 12 are about what the model chose to
  run. They held 5/5 with the default model and a four-step prompt, but they are not a property of the harness.
- **The mid-turn window is timing, not synchronisation.** The script posts bob's message when the model's own send
  reaches the store and relies on `sleep 20` still being in flight. It was, in every run (the enqueue timestamp is
  always inside the sleep). A much slower machine could post before the model reaches step 3, in which case the
  message would be injected at the next boundary instead — still a pass for assertion 8, but not strictly
  "mid-turn". The `remove … absorbed_mid_turn` line is what distinguishes the two, and it was present every time.
- **Not run in CI, by design.** It needs a logged-in Claude Code and spends model calls.
- **`SessionEnd` is not asserted directly** for the same reason as `UserPromptSubmit`: it exits 0 silently and is
  recorded nowhere. Its effects are: assertion 15 (the pidfile is gone and the watcher process with it) and the
  disappearance of the by-pid map.
