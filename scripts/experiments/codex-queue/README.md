# Codex queue experiment (card 107)

The live test behind `docs/experiments/codex-queue-participation.md`: can OpenAI's Codex CLI (`codex`) give Brigade
what Claude Code gives it, above all a way for an outside process to wake an idle session? The September gate
(`docs/experiments/codex-participation.md`, Codex 0.153.4) closed on exactly that; this run re-asks it on 0.163.0,
whose `codex queue --thread <uuid> --message <text>` command wraps the app-server's `thread/queue/add`. This never
runs in CI and spends a real Codex account.

## What is here

| File | Role |
| --- | --- |
| `run-codex.sh` | Runs `codex` outside the Claude session with an allow-listed environment (`env -i`; no `CLAUDE*`, `AI_AGENT*`, `BRIGADE*`), the operator's real `CODEX_HOME` (never a copied `auth.json`) and the test root's `TMPDIR`. `--cwd DIR` and repeatable `--env K=V` precede the `codex` arguments |
| `lib.sh` | Shared driver functions: launch a TUI in tmux (answering Codex's "Trust this folder?" prompt, which can appear after the composer in daemon mode), the arming prompt, thread-id discovery from Codex's own state database, the queue-table poller, `send` (one `codex queue` plus the wait for the model's `RECEIVED <nonce>`), evidence copy |
| `arm1-queue.sh` | Arm 1, route A: a TUI session is armed with one prompt; the driver then holds (5 s, 120 s by default), sends one `codex queue` message after each hold, and runs a null control of the same length with nothing sent. `NOPROMPT=1` skips the arming prompt, `KEEP=1` leaves the TUI running, `EXISTING=1`/`WORKDIR` attach to a running TUI, extra arguments are `codex` flags (`--dangerously-bypass-approvals-and-sandbox` is the customer configuration, `--no-daemon` the embedded mode) |
| `arm1-hold.sh` | The same with `HOLDS` allowed to be empty: `HOLDS=2100 CONTROL=0` is the ≥ 35-minute hold, `HOLDS= CONTROL=2100` its null control |
| `arm1-fresh2.sh` | The pre-prompt test: the fresh TUI's thread id is read from Codex's log database (no model turn), a message is queued before anyone has typed, then the session is armed and queued again |
| `arm1-followups.sh` | On a running armed session: lines of 64 B, 2 KiB, 8 KiB and 16 KiB (does each arrive whole?), then `/compact`, `/new` and `/quit` + `codex resume <id>`. Its pane-text waits let the 16 KiB echo's hung turn swallow the later steps; kept as the record |
| `arm1-followups2.sh` | The same, completion-gated: every step waits for the thread's turn to complete in the rollout; sizes 2 and 8 KiB, `/compact`, `/quit` + resume, the 16 KiB echo last and alone |
| `arm1-size16.sh` | 16 KiB and 32 KiB lines with a short-reply arming text (the model names the END marker instead of echoing the padding) |
| `arm-new.sh` | `/new` on an idle armed session with the hook probe installed: which hooks fire, the new thread id, `codex queue` to the old id and to the new id before and after the new chat's first prompt |
| `cleanup.sh` | After the run: archives the test threads, removes the trust tables the runs added to the operator's `config.toml`, stops the daemon, kills the tmux server and the pollers |
| `arm1-busy.sh` | A message queued while the model is mid-turn (a 45 s `sleep` under the bypass flag): when is it delivered? |
| `arm3-hooks.sh` | Project-level `.codex/hooks.json` (SessionStart, UserPromptSubmit, Stop, SessionEnd) under `--dangerously-bypass-hook-trust`: which events fire and when, their stdin fields and environment names, whether UserPromptSubmit fires for a queued message, whether a detached SessionStart child survives, the SessionEnd budget; then the model runs `fixture/shell-facts.sh` once |
| `fixture/hook-probe.sh` | The hook: records stdin field names, environment names and `CODEX_THREAD_ID == session_id` (never values), and from SessionStart leaves a child running in Brigade's spawn shape |
| `fixture/end-budget.sh` | A SessionEnd hook ticking every 0.1 s: the last tick is the shutdown budget's lower bound |
| `fixture/shell-facts.sh` | Run by the model from its shell: environment names and reach facts of the model's shell (never values) |
| `daemon-probe.sh` | An attempt to speak JSON-RPC to the app-server daemon through `codex app-server proxy`; the control socket expects a WebSocket handshake, so it is refused (kept as the record) |
| `score.py` | Offline scorer: reads a session's rollout JSONL (Codex's own log), the driver log and the queue-table poll log and prints, per send, the queue row's appearance and disappearance, the turn start, the user item, the model's own text containing the nonce, keystrokes in the window, and the turns started inside the null control |

## Running it

Prerequisites: `codex` 0.163.0 installed and signed in on the operator's account (the real `~/.codex`; nothing is
copied), `tmux`, `python3`, `sqlite3`, `bc`.

```sh
export CODEX_TEST_ROOT=/path/outside/the/repository/codex-test      # keep it for every run
mkdir -p "$CODEX_TEST_ROOT"/{evidence,fixture,logs,state,tmp}
cp scripts/experiments/codex-queue/*.sh scripts/experiments/codex-queue/score.py "$CODEX_TEST_ROOT/"
cp scripts/experiments/codex-queue/fixture/* "$CODEX_TEST_ROOT/fixture/"
cd "$CODEX_TEST_ROOT"
HOLDS="5 120" CONTROL=120 ./arm1-queue.sh yolo arm1y --dangerously-bypass-approvals-and-sandbox   # customer configuration
HOLDS="5 120" CONTROL=60 ./arm1-queue.sh default arm1d                                            # Codex's default profile
HOLDS="5 60" CONTROL=0 ./arm1-queue.sh embedded arm1e --dangerously-bypass-approvals-and-sandbox --no-daemon
./arm1-fresh2.sh fresh2 arm1f2
./arm1-busy.sh busy arm1b
./arm3-hooks.sh hooks arm3h
./arm5-identity.sh yolo arm5y --dangerously-bypass-approvals-and-sandbox
./arm1-size16.sh size16 arm1s --dangerously-bypass-approvals-and-sandbox
./arm-new.sh new armnew
HOLDS=2100 CONTROL=0 nohup ./arm1-hold.sh long arm1l --dangerously-bypass-approvals-and-sandbox > logs/long.out 2>&1 &
HOLDS= CONTROL=2100 nohup ./arm1-hold.sh null arm1n --dangerously-bypass-approvals-and-sandbox > logs/null.out 2>&1 &
KEEP=1 HOLDS=5 CONTROL=0 ./arm1-queue.sh follow arm1w --dangerously-bypass-approvals-and-sandbox
./arm1-followups2.sh follow arm1w "$(cat evidence/arm1-follow/session-uuid.txt)" --dangerously-bypass-approvals-and-sandbox
python3 -I score.py evidence/arm1-yolo
./cleanup.sh
```

Each TUI answers the "Trust this folder?" prompt with Enter, which Codex saves to the operator's `~/.codex/config.toml`
as a `[projects."<workspace>"]` table; remove those tables after the run (the report names them). Launching the TUI
also writes `[tui] screen_reader_detection_done` and the model-availability counters there. Edit no driver while an
instance of it runs (bash reads scripts incrementally). Sessions may run concurrently (each has its own thread id and
rollout). Raw rollout copies, pane captures and the sign-in stay outside the repository; the report carries hashes,
booleans, event names and relative timestamps.
