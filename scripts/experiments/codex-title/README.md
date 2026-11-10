# Codex terminal title experiment (card 190)

The live test behind `docs/experiments/codex-terminal-title.md`: a Codex session renamed to `reviewer-rjae` showed
`reviewer-rjae | brigade` while the Brigade roster showed `reviewer-rjae`. Where does ` | brigade` come from, is
it part of the stored name, and can Brigade affect it? This never runs in CI and uses a real Codex account (one
short model turn, and only with `TURN=1`; it then waits up to 30 s for Codex to name the thread itself).

## What is here

| File | Role |
| --- | --- |
| `run-codex.sh` | Runs `codex` outside the calling session with an allow-listed environment (`env -i`; no `CLAUDE*`, `AI_AGENT`, `BRIGADE_*`, `MUSE_*`, `CODEX_*`) and the test root's `TMPDIR`. With no `CODEX_HOME` passed on, Codex uses the operator's real `~/.codex` (never a copied `auth.json`), which is the directory every other file here reads: a `CODEX_HOME` in the caller's environment is ignored by all of them. `--cwd DIR` precedes the `codex` arguments |
| `lib.sh` | Shared driver functions: launch a TUI in tmux with its raw output piped to a file, answer the three prompts a launch can open with (below), read the terminal title (tmux keeps an OSC 0/2 title as the pane's title), type only into the composer, read Codex's title index and state database, list the OSC titles in the raw output |
| `titles.sh` | The whole run. Builds five folders whose names all differ (a repository; a clone whose folder is `folder-name` and whose `origin` is `remote-name.git`; a subdirectory two levels down; a linked worktree; a folder in no repository), asks `codex doctor --json` what project name it would show in each, then in each starts a TUI, reads the title, types `/rename probe-<label>-<nonce>`, reads the title again and reads back what Codex stored. Then the same in the subdirectory with `-c 'tui.terminal_title=[…]'` set five ways. `TURN=1` adds one model turn |
| `cleanup.sh` | After the run: closes the tmux server; archives the run's threads (the ids `titles.sh` recorded, and every unarchived thread Codex's state database places in the test root or under it); removes the trust tables the run added to the operator's `config.toml` (the test root's and those under it, written whole and renamed into place); stops the app-server daemon and its updater loop if the run started them (`evidence/before.txt`, written by the first run in a test root and kept, says whether a daemon was running) |
| `reader-probe.sh`, `reader-probe.go.txt` | Brigade's side: runs the shipped host-facts reader (`internal/harness/watch`, the code the watcher runs before every heartbeat) on the real Codex home for one thread id and prints the name the roster would carry. The test is laid over the package with `go test -overlay`, so the checkout is never written; it exits 1 when the probe did not run |

## Running it

Prerequisites: `codex` installed and signed in on the operator's account (the real `~/.codex`; nothing is copied),
`tmux`, `python3`, `sqlite3`, `git`, and for the reader probe `go` and a checkout of this repository.

```sh
export CODEX_TEST_ROOT=/path/outside/any/repository/codex-title     # letters, digits and / . _ - only
mkdir -p "$CODEX_TEST_ROOT"
cp scripts/experiments/codex-title/*.sh scripts/experiments/codex-title/reader-probe.go.txt "$CODEX_TEST_ROOT/"
cd "$CODEX_TEST_ROOT"
TURN=1 ./titles.sh                      # evidence/doctor.tsv, evidence/titles.tsv, one folder per run
./reader-probe.sh /path/to/brigade "$(head -1 evidence/thread-ids.txt)"
./cleanup.sh
```

## What a launch can open with, and what the driver answers

- **"Update available"** (a modal, from the second launch after Codex learns of a newer version): its default
  answer, Enter, runs `brew upgrade --cask codex`. The driver answers **Esc** (skip, this launch only). The first
  draft of this driver typed `/quit` and Enter without looking at the screen and started that upgrade four times;
  each was killed seconds later and nothing was installed. Since then every line the driver types goes through
  `composer`, which refuses unless the composer is on the visible screen and no modal is; the scrollback is never
  read.
- **"Trust this folder?"**: Enter, the one key sent outside `composer`, and only when a second look at the
  screen, just before it, still shows that prompt and no update prompt. Codex saves the answer to the operator's
  `~/.codex/config.toml` as a `[projects."<folder>"]` table; `cleanup.sh` removes the test root's.
- **"Hooks need review"**: **Esc**. No hook is trusted and none runs; the title needs none. The driver never uses
  `--dangerously-bypass-hook-trust`.

The run never saves the `/title` picker and never writes `tui.terminal_title` to the operator's configuration:
the setting is passed as a launch override (`-c`), and `titles.sh` prints the count of `terminal_title` lines in
`config.toml` before and after. What it leaves behind: the archived threads' lines in Codex's
`session_index.jsonl` (an append-only file the driver does not edit) and their rows in the state database.
