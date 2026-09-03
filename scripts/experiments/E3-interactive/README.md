# E3-interactive harness — plan row P3-8, checks 1 and 2

Results and findings: `docs/experiments/E3-interactive.md`. Run on Claude Code 2.1.259.

The checklist calls checks 1 and 2 keyboard-dependent. They are not: what they need is an
interactive **pty** in **Manual** permission mode, and `expect` supplies one. This driver is the
2.1.259 descendant of `scripts/experiments/E0-8/run_b.py`, pointed at the **shipped** plugin
instead of a probe plugin, and it reuses that experiment's `common.py` wholesale (the expect
prelude, the by-prefix environment strip, and the config guard).

```sh
python3 scripts/experiments/E3-interactive/run_manual.py --arms baseline --runs 1 --tag smoke
python3 scripts/experiments/E3-interactive/run_manual.py            # the full matrix
```

Needs a logged-in Claude Code, `expect`, and a Brigade profile already joined to a team
(`bin/brigade profile status` must print a joined profile). It costs model calls, so it never
runs in CI. Artefacts land under `.ignored/e3-interactive/<tag>/` — one directory per run,
carrying `verdict.json`, `marks.ndjson`, `session.log`, `drive.exp` and the detector's
`state/` — plus a `summary.json` for the matrix.

## The detector is mechanical, not prose

A `PreToolUse` hook fires once the model has produced tool parameters and **before** the
permission decision; `PostToolUse` fires only if the call actually ran. So

```
attempt recorded  +  no exec recorded  +  the session stops making progress
    = the permission system intervened, and with allow/deny/ask all empty that is a PROMPT
```

Both hooks are supplied through `--settings`. **The shipped `plugin/` tree is never touched** —
`make plugin-check` asserts its exact file list, so a detector hook could not live there.

`bin/posttool` writes brigade-Bash executions to their own `exec.ndjson` and Skill invocations
to `skill-exec.ndjson`, so the expect script can wait on a line count without parsing anything.

## The permission mode is recorded three independent ways

The first attempt at P3-8 was voided because both sessions had run in `auto`, where Claude Code
approves tool calls itself and neither a dialog nor a prompt can appear. Every run here records:

1. the **hook payload's** `permission_mode`, on every single tool call;
2. the **transcript's** `permission-mode` records;
3. the **Brigade by-pid map's** `permission_mode`, written by the plugin's own
   `UserPromptSubmit` hook — the only writer of that field.

`verdict.json` carries all three and a `mode_is_manual` boolean. A run whose mode is `auto`
measures nothing and must be discarded.

## Arms

| Arm | What it establishes |
| --- | --- |
| `baseline` | **The null control.** No skill; `brigade sessions` asked for directly. It MUST stall. A baseline run that does not stall means the detector cannot go red, and every other arm's result that day is void. |
| `skill` | Check 1 then check 2 in one session, from a fresh temporary project directory (no stored dismissal can explain a missing dialog). |
| `skill-repo` | The same, with the repository as the session's working directory — the checklist's primary location. |
| `dismiss` + `dismiss2` | Two sessions in ONE project directory: the first answers the Skill dialog with option 2 ("don't ask again"), the second must then see no dialog. This re-measures E0-8's per-project scoping claim. |

## What the headless half already settled (2026-09-03, 2.1.259)

Quoted here because it bounds what the pty run still has to prove. With the same two detector
hooks, `allow`/`deny`/`ask` empty and `--permission-prompts none`:

- only `Skill` pre-approved → the skill was invoked and **both** `brigade sessions` and
  `brigade sessions --all` executed. The `allowed-tools` grant works on 2.1.259.
- a resumed second turn, nothing pre-approved, no skill → `brigade sessions` **denied**.
- nothing pre-approved → the **Skill tool itself was denied**, and the model then tried the
  binary by its **full path** (which the skill forbids) and then the native `ListAgents`.

So `-p` settles the grant and its turn scope. It cannot settle the dialogs, because it has no
approval surface: that is what the pty is for.
