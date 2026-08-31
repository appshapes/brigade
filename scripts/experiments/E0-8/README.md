# E0-8 harness

Results and findings: `docs/experiments/E0-8.md`. Run on Claude Code 2.1.252.

Two rigs:

- **`.` (root)** — sub-checks (b) skill grant, (f) `/clear` + `whoami`, (h) the 10,000-character heredoc
  threshold. `run_b.py`, `run_f.py`, `run_h.py`, `analyze.py`, `common.py`, and `plugin/`.
- **`e2/`** — sub-checks (a) download timing, (c) sandbox, (d) context line, (e) `PATH` shadowing, (g) validate.
  `run_a.py`, `run_c.py`, `run_d.py`, `run_e.py`, `nest.py`, `analyze.py`, and `plugin_a|c|d|e/`.

## Build first — binaries are deliberately not committed

Two Go programs are built rather than stored (they are 8–9 MB each):

```sh
cd e2/server && go build -o throttle .     # rate-paced HTTP server for (a)
cd ../cbin   && go build -o brigade .      # HTTP client used as the fake `brigade` for (c)
cp e2/cbin/brigade e2/plugin_c/bin/brigade # (c)'s plugin expects it here
```

`(a)` also needs a ~7.9 MiB asset to serve; any file of that size works, and the measured break-even rates in
the writeup are tied to 8,324,402 B — recompute them if you use a different size.

## Before running anything

Every nested invocation must strip the outer session's Claude variables **by prefix** (unset `CLAUDECODE`,
`CLAUDE_PID` and every `CLAUDE_CODE_*`, plus `CLAUDE_EFFORT` and `AI_AGENT`), keeping only `CLAUDE_CONFIG_DIR`.
The drivers do this; anything new must too, or frames land in the wrong session and the run is void.

Snapshot and re-verify `$CLAUDE_CONFIG_DIR/settings.json`, `CLAUDE.md` and `~/.claude/CLAUDE.md` around every run.
Never restore `.claude.json` wholesale — the driving session writes it concurrently.

## Two gotchas that cost real time

- A trailing `\r` in the same `expect` `send` as a long prompt does **not** submit: Claude Code treats the fast
  burst as a paste and swallows the CR. Send text and Enter separately, with a drain between.
- At least four different first-run prompts exist (trust dialog, fullscreen-renderer write, Chrome extension,
  and the Skill-invocation dialog). Match a **single word**; multi-word regexes never match a boxed dialog.
