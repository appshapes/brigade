# Codex CLI's terminal title: where ` | brigade` after a session's name comes from (card 190)

Date: 2026-11-09 (evening, EST)
Card: AppShapes Trello 190, "Investigate the repository or folder suffix on renamed Codex sessions"
Precedent: `docs/experiments/codex-queue-participation.md` (card 107), whose driver shape this run reuses;
`docs/research/host-session-facts.md` (card 117), which measured the file Brigade reads a Codex name from.
Client: OpenAI Codex CLI **0.166.0** (`/opt/homebrew/bin/codex`, Homebrew cask; 0.167.0 was on offer and was not
installed), run directly
Host: macOS 26.6.1 (Darwin 25.6.0, arm64), `tmux` 3.7c, `python3` 3.14.6, `sqlite3` 3.51.0, `git` 2.55.0
Account: the operator's ChatGPT sign-in in the real `~/.codex` (never a copied `auth.json`; see §8 for what the
runs left there)
Model: `gpt-6.1-sol` at `xhigh`, for the three one-prompt turns of §6; everything else ran with no model
Driver: `scripts/experiments/codex-title/` (README there)
Result: **the ` | brigade` is Codex's own terminal title, not part of the session's name, and Brigade neither adds
it nor reads it. Codex 0.166.0 builds the terminal tab or window title from an ordered list of items joined by
` | `: by default an activity marker, the thread's name (left out while the thread has none) and the project name.
The project name is the folder name of the git repository's root (a linked worktree's own root), never the
remote's name and never the working directory's when that is inside a repository; outside a repository it is
the working directory's folder name. Codex stores the name alone (`thread_name` in `session_index.jsonl`,
`threads.name` in its state database); that index is the one place Brigade reads it, so the roster already shows
the intended name. What the title shows is the person's own setting (`/title`, or `tui.terminal_title` in
Codex's configuration); a Brigade plugin, hook or command has no way to set it short of writing Codex
configuration that is the person's or their project's, which Brigade should not. No code change; the expected
difference is documented.**

Pane captures, raw terminal output and the doctor reports stayed outside the repository. The titles below were
read two ways from the same run: as tmux's `#{pane_title}` (tmux keeps the title a program sets with OSC 0 or
OSC 2) and as the OSC sequences in the pane's raw output (`tmux pipe-pane`).

The runs: a first pass by hand, which found the reproduction (a thread named `probe-190`); the scripted run
whose rows are below (names ending `4fa7`, with a model turn; its repository-configuration row was run by hand
with the driver's functions); and two more end-to-end runs from empty test roots (names ending `b1b0`, and
`4088` with a model turn), the last after the agent review's fixes. Both reproduced every row of §3 to §5. The
model-turn step then ran once more alone, with the longer wait §6 explains.

## 1. The observation

Filed on 2026-11-02 from a Codex session in a checkout of this repository: after it was renamed to
`reviewer-rjae`, Codex displayed `reviewer-rjae | brigade` while the Brigade roster displayed `reviewer-rjae`
(REPO `brigade`). The card's questions: is the suffix Codex's or Brigade's; is it the repository's name, the
folder's, or another label; is it part of the stored name; can Brigade affect it; is the roster right.

`brigade` names three different things in that checkout, which is why the card asks for a case where they differ:
the folder (`…/appshapes/brigade`), the `origin` remote (`appshapes/brigade.git`) and Brigade's own workspace
label (the roster's REPO column: by default the `origin` remote's name, else the directory's; `workspace_label`
replaces it and `share_workspace_label: false` withholds it).

## 2. Five folders whose names all differ

`titles.sh` builds them under a test root that is in no repository:

| Label | Working directory | What it is |
| --- | --- | --- |
| `repo` | `plain-repo` | a repository with no remote |
| `clone` | `folder-name` | a clone whose `origin` is `…/remotes/remote-name.git` |
| `subdir` | `folder-name/sub-dir/deep-dir` | two levels down in that clone |
| `worktree` | `worktree-name` | a linked worktree of that clone (`git worktree add`) |
| `norepo` | `plain-dir/inner-dir` | a folder in no repository |

## 3. What Codex says with no TUI: `codex doctor --json`

Its `terminal.title` check names the project value and where it came from. No model, no thread:

| Label | `terminal title project source` | `terminal title project value` |
| --- | --- | --- |
| `repo` | `git repo root` | `plain-repo` |
| `clone` | `git repo root` | `folder-name` |
| `subdir` | `git repo root` | `folder-name` |
| `worktree` | `git repo root` | `worktree-name` |
| `norepo` | `cwd` | `inner-dir` |
| this repository's checkout | `git repo root` | `brigade` |

`remote-name` appears nowhere. Doctor's list of the default items is wrong in 0.166.0, though: see §5.

## 4. The title before and after `/rename`, and what Codex stored

In each folder: start the TUI, read the title, type `/rename probe-<label>-4fa7`, read the title again, then read
Codex's title index and state database. No prompt was submitted, so no model ran; `/rename` works on a thread
that has had no turn.

| Label | Title before | Title after `/rename` | `thread_name` in the index | `threads.name` |
| --- | --- | --- | --- | --- |
| `repo` | `plain-repo` | `probe-repo-4fa7 \| plain-repo` | `probe-repo-4fa7` | `probe-repo-4fa7` |
| `clone` | `folder-name` | `probe-clone-4fa7 \| folder-name` | `probe-clone-4fa7` | `probe-clone-4fa7` |
| `subdir` | `folder-name` | `probe-subdir-4fa7 \| folder-name` | `probe-subdir-4fa7` | `probe-subdir-4fa7` |
| `worktree` | `worktree-name` | `probe-worktree-4fa7 \| worktree-name` | `probe-worktree-4fa7` | `probe-worktree-4fa7` |
| `norepo` | `inner-dir` | `probe-norepo-4fa7 \| inner-dir` | `probe-norepo-4fa7` | `probe-norepo-4fa7` |

The raw output of the `clone` run carries these titles, in order, all as OSC 0:
`folder-name`, `<spinner> folder-name` (while the TUI starts), `folder-name`, `probe-clone-4fa7 | folder-name`,
and an empty title at exit (Codex clears what it set).

So the suffix is the **folder name of the repository's root**: not the remote's name (`clone`), not the working
directory's (`subdir`), and for a linked worktree the worktree's own folder, not the main checkout's
(`worktree`). With no repository it is the working directory's folder (`norepo`). And no store holds it: the
index and the state database carry the name alone. The title's own bottom-of-screen twin, the status line, shows
the same name with no suffix (`GPT-6.1-Sol xhigh · <working directory> · probe-190`).

**Brigade's side.** The roster name of a Codex session comes from one place, the last entry of
`$CODEX_HOME/session_index.jsonl` whose `id` is the session's thread id (`refreshIndex` in
`internal/harness/watch/hostfacts.go`, card 117); the watcher never sees a terminal title. `reader-probe.sh`
ran that shipped reader on the real index for five of these runs' threads and got `probe-190`,
`probe-clone-4fa7`, `probe-turn-4fa7`, `probe-repo-b1b0` and `probe-repo-4088`: each the name alone, and for the
third the name typed with `/rename`, not the one Codex had given the thread first (§6). The roster is showing the
intended name.

## 5. The setting, and what moves the title

`/title` in the TUI opens "Configure Terminal Title: select which items to display in the terminal title". With
nothing configured, three items are ticked, in this order:

| Item | The picker's description |
| --- | --- |
| `activity` | Spinner while working, action-required message while blocked |
| `thread-name` | Current thread name (omitted when unnamed) |
| `project-name` | Project name (falls back to current directory name) |

Unticked and available: `app-name`, `current-dir`, `run-state`, `thread-title` ("Current thread title, or thread
identifier when unnamed"), `git-branch`, context, limit, token and cost counters, `codex-version`, `thread-id`,
`fast-mode`, `daybreak`, `model`, `model-with-reasoning`, `reasoning`, `task-progress`. The picker's preview line
read `[ ! ] Action Required | probe-190 | folder-name`: the items, joined by ` | `. Saving the picker writes
`tui.terminal_title` to the person's `~/.codex/config.toml`; the driver cancelled it with Esc.

The same setting as a launch override, in the `subdir` folder (`-c 'tui.terminal_title=[…]'`), and from a
repository's own `.codex/config.toml`:

| Setting | Title before | Title after `/rename` |
| --- | --- | --- |
| `["thread-name"]` | none set | `probe-only-thread-4fa7` |
| `["project-name"]` | `folder-name` | `folder-name` |
| `["thread-name","current-dir","git-branch"]` | `<the working directory, abbreviated> \| main` | `probe-thread-cwd-4fa7 \| <the working directory, abbreviated> \| main` |
| `["thread-title","project-name"]` | `<the thread id> \| folder-name` | `probe-thread-title-4fa7 \| folder-name` |
| `[]` | none set | none set |
| `terminal_title = ["thread-name"]` under `[tui]` in the repository's `.codex/config.toml`, the folder trusted | none set | `probe-project-config-4fa7` |

So a person who wants the bare name in the tab sets `["thread-name"]`, with `/title`, in `~/.codex/config.toml`
or per launch; and a repository can carry the setting for everyone who trusts it.

What Codex's own pages say: the configuration reference lists `tui.terminal_title` as `array<string> | null`,
"Ordered list of terminal window/tab title item identifiers. Defaults to `["spinner", "project"]`; `null`
disables title updates.", and the developer-settings page lists `/title` among the TUI's pickers for keys under
`[tui]`. Two things in 0.166.0 disagree with what it does:

- The reference's default (`spinner`, `project`) is older than this version's item names and has no thread name
  in it; the TUI's default has one.
- `codex doctor` does not know the item `thread-name`. With nothing configured it reports the default items as
  `activity, project-name`; with the repository's `terminal_title = ["thread-name"]` in force (the title showed
  the name alone) it reports "terminal title configured with invalid items", `invalid items = "thread-name"`,
  `items = none`. Its project source and value agreed with the TUI in every folder.

## 6. A model turn: the title while Codex works, and a name nobody typed

In `plain-repo`, one prompt ("Reply with exactly the word READY and nothing else. Do not run any command."),
then `/rename`. The titles in the raw output, in order: `plain-repo`, `<spinner> plain-repo`, `plain-repo`,
`Reply READY | plain-repo`, `probe-turn-4fa7 | plain-repo`, empty at exit.

Codex can name a thread itself after its first turn: about three seconds after the prompt the index had
`"thread_name":"Reply READY"` for the thread, and the title had the suffix without anyone renaming anything. The
`/rename` then appended a second entry for the same id; the last one is the name (and is what Brigade's reader
returned). So a Codex session whose thread has a name, typed or generated, is titled `<name> | <folder>` by
default, and the roster row of the same session reads `<name>`.

The generated name is not immediate and was not seen every time. Of three turn runs it arrived about three and
four seconds after the prompt in two; in the other the driver renamed the thread about seven seconds after the
prompt, no generated name had been written by then, and none came afterwards (the index holds the typed name
only). The driver now waits up to 30 s for a name to show in the title before it renames.

## 7. Can Brigade change it, and should it

- **Brigade does not add the suffix.** Nothing in the Codex package writes a terminal title, and the name Brigade
  publishes is read from the index, where the suffix never is.
- **Brigade cannot set it from where it runs.** The title is composed by the TUI from the person's `[tui]`
  settings. A Codex plugin has no option mechanism and its hooks have no field that names a session or sets a
  title (card 174: `brigade rename` records nothing on Codex and says so). The only route would be to edit the
  person's `~/.codex/config.toml`, or to ship a `.codex/config.toml` in their project.
- **Brigade should not.** Which items a person's terminal tab shows is their display preference, the project
  name in a tab is useful to someone with several repositories open, and Brigade writes no harness's
  configuration.
- **The two displays can differ further than the card saw.** The roster's REPO column is by default the `origin`
  remote's name (else the directory's; `workspace_label` replaces it); Codex's suffix is the folder's. In a
  second checkout named `brigade-work`, Codex shows `<name> | brigade-work` and the roster shows `<name>` with
  REPO `brigade`.

**Recommended next step:** none in code. The expected difference is now on the page a member reads
([Know what teammates see about your session](../team-member/use/your-session-on-the-roster.md#names-models-and-context-by-host)),
with the setting that changes the title.

## 8. What the runs did to the operator's machine

- **Codex offered an update and the first draft of the driver accepted it.** From its second launch on, Codex
  0.166.0 opened with a modal "Update available · 0.166.0 → 0.167.0" whose default answer runs
  `brew upgrade --cask codex`. A hand-run loop typed `/quit` and Enter into it without reading the screen, four
  times; each upgrade was killed about three seconds later when the loop closed its tmux session (the last had
  downloaded and verified the package and installed nothing). Codex stayed at 0.166.0; the 0.167.0 download is
  in Homebrew's cache. The driver now answers that prompt with Esc, types a line only when the composer is on
  screen (`composer` in `lib.sh`), reads the visible screen and never the scrollback, and sends its one other
  Enter, to the folder-trust prompt, only after two looks at the screen show that prompt and no update prompt.
- **Codex moved the Brigade plugin to 0.47.2 and asks for its hooks to be reviewed again.** The plugin cache
  directory `plugins/cache/brigade/brigade/0.47.2` was created during the first launch, and every later launch
  opened with "Hooks need review: 3 hooks are new or changed"; `/hooks` lists one hook to review under each of
  SessionStart, SessionEnd and UserPromptSubmit, and the SessionStart entry reads "Modified since last trusted -
  review required", its command carrying the versioned cache path; the turns of §6 printed "Hook failed:
  plugin contents changed; hook approval required". The driver skipped the review with Esc every
  time, trusted nothing and never used `--dangerously-bypass-hook-trust`, so the operator's next Codex session
  asks, and Brigade's hooks do not run there until the answer is "Trust all and continue". Whether every plugin
  update needs this is not settled here; the package page says the trust is given once.
- **Codex's managed app-server daemon package updated itself** from 0.163.0 to 0.167.0 while the runs' daemon was
  up (`~/.codex/packages/app-server-daemon/current`); the CLI is still 0.166.0.
- **Cleaned up:** the 38 threads the runs named are archived; the `[projects."…"]` trust tables the runs added
  are removed and `~/.codex/config.toml` is byte-identical to its state before the first launch (same SHA-256);
  the daemon the runs started and its updater loop are stopped (`daemon stop` leaves the loop; `cleanup.sh` ends
  it); no `terminal_title` line was ever written. **Left:** the archived threads' lines in `session_index.jsonl`
  and their rows in the state database.

## 9. Not proven

- A live roster row beside a live title in one session of this run: registration needs Brigade's hooks, which
  were waiting for the operator's review (§8). The card's own observation of 2026-11-02 is that pair
  (`reviewer-rjae | brigade` in Codex, `reviewer-rjae` on the roster), the index still holds
  `"thread_name":"reviewer-rjae"` for that thread, and §4's reader probe is the code between the two.
- The setting saved in the person's own `~/.codex/config.toml`, by `/title` or by hand: the driver never writes
  that file. It was measured as a launch override and from a repository's `.codex/config.toml`, the same key.
- Terminals other than tmux. The title is a plain OSC 0 sequence; how a terminal shows it (tab, window, both) is
  the terminal's.
- The Codex desktop app and IDE extension, Windows and Linux, and any Codex version but 0.166.0. The item names
  have changed before (the reference still says `spinner` and `project`).
