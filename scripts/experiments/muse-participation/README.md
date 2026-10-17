# Muse participation experiment (card 88)

The live test behind `docs/experiments/muse-participation.md`: can Meta's Muse Code (`muse`) give Brigade what
Claude Code gives it, above all a way for an outside process to wake an idle session? Plan:
`.context/plans/muse-team-participation.md` (sections 4 and 5). This never runs in CI and spends a real Muse account.

## What is here

| File | Role |
| --- | --- |
| `run-muse.sh` | Runs the pinned `muse` binary outside the Claude session with an allow-listed environment (`env -i`; no `CLAUDE*`, `AI_AGENT*`, `BRIGADE*`, `META_API_KEY`), the operator's real sign-in (real `XDG_CONFIG_HOME`), isolated `XDG_DATA_HOME`/`XDG_STATE_HOME`/`XDG_CACHE_HOME` under the test root, `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on`. `--cwd DIR` and repeatable `--env K=V` precede the `muse` arguments |
| `arm2-monitor-v2.sh` | Arm 2, route B: a TUI session in tmux is asked to arm a persistent `monitor` with `wake_delay_ms 0` on `fixture/watch.sh`; the driver appends a nonce line after idle holds of 5 s, 120 s and an optional long hold, scores the wake from the pane, and runs a null control of the longest hold. `INBOX_OVERRIDE` puts the trigger file outside the workspace; `HOLDS` and `CONTROL` override the hold list and the control length; `LAUNCHER` replaces the launch command (a script taking the workspace directory, e.g. to add per-run `-c hooks=…`) |
| `arm2-monitor-v3.sh` | Arm 2 follow-ups: lines of 64 B, 2 KiB, 8 KiB and 16 KiB (does each arrive whole?), then whether the monitor still wakes the session after `/compact`, after `/clear`, and after `/exit` + `muse resume <uuid>` |
| `fixture/watch.sh` | The monitor's command: records environment names and sandbox facts of the monitor's shell once (never values), then `tail -n 0 -F` of the trigger file |
| `fixture/hook-probe.sh` | A settings-level hook for the three Brigade events: records stdin field names, environment names and `MUSE_SESSION_ID == session_id`, and from SessionStart leaves a child running in Brigade's spawn shape (new session, stdio to a log) to measure survival |
| `fixture/end-budget.sh` | A SessionEnd hook ticking every 0.1 s: the last tick is the shutdown budget's lower bound |
| `score.py` | Offline scorer: reads a session's `session.jsonl` and the driver log and prints, per append, the queued/run-start/drained/assistant-text offsets, the monitor call's arguments, keystrokes in the window, the tool calls of the session and whether any names an evidence path, and the runs inside the null control |

## Running it

Prerequisites: `muse` installed and signed in through the browser on the operator's account (never `META_API_KEY`,
never a copied `auth.json`), the pinned binary's sha256 equal to the channel manifest's, `tmux`, `python3`, `bc`.

```sh
export MUSE_TEST_ROOT=/path/outside/the/repository/muse-test      # keep it for every run
export MUSE_BIN=~/.local/bin/muse-bin-<version>                     # the pinned binary, not the launcher
mkdir -p "$MUSE_TEST_ROOT"/{data,state,cache/tmp,work,logs,evidence,fixture}
cp scripts/experiments/muse-participation/fixture/* "$MUSE_TEST_ROOT/fixture/"
cd scripts/experiments/muse-participation
./arm2-monitor-v2.sh yolo arm2y 0 --yolo                              # customer configuration
INBOX_OVERRIDE="$MUSE_TEST_ROOT/state/inbox/inbox.ndjson" ./arm2-monitor-v2.sh default arm2d 0 --trust-workspace
HOLDS=2100 CONTROL=0 ./arm2-monitor-v2.sh long arm2l 0 --yolo         # the ≥ 35-minute hold, then one append
HOLDS= CONTROL=2100 ./arm2-monitor-v2.sh null arm2n 0 --yolo           # its null control: armed, nothing appended
./arm2-monitor-v3.sh yolo arm2v3 --yolo
python3 -I score.py "$MUSE_TEST_ROOT/evidence/arm2v2-yolo"
```

Settings-level hooks need no edit of the operator's `settings.json`: pass them per run, e.g.
`./run-muse.sh exec --provider echo --trust-workspace -c 'hooks={"SessionStart":[{"hooks":[{"type":"command","command":"<root>/fixture/hook-probe.sh <store-dir>","timeout":30}]}]}' ping`.

Edit no driver while an instance of it runs (bash reads scripts incrementally). Monitor sessions may run concurrently (each has its own trigger file and log); sends never may. Raw `session.jsonl` copies, pane captures and the sign-in stay outside the repository; the
report carries hashes, booleans, event names and relative timestamps.
