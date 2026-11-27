# Antigravity CLI package (work items 532, 533, 534)

Goal: Google Antigravity CLI (`agy`) becomes Brigade's fifth harness, status "In trial". Frank's decisions of
2026-11-27: three work items (532 the package and delivery at the next model call; 533 the idle wake; 534 usage
from the transcript); the CLI form only; Frank's Codex and Grok sessions review each push, beside the code-review
skill.

Evidence: [docs/experiments/antigravity-participation.md](../../docs/experiments/antigravity-participation.md)
(work item 531, agy 1.5.18).

## Facts the design stands on (measured or read on 2026-11-27)

- Plugin: `plugin.json` at the package root (`$schema` https://antigravity.google/schemas/v1/plugin.json, `name`
  kebab-case, `version`), `hooks.json` at the root, `skills/<name>/SKILL.md`. Installed copies live in
  `~/.gemini/config/plugins/<name>/`. A plugin's hook commands run with the plugin root as cwd: use `./bin/...`.
  `agy plugin install <dir | github URL | name@marketplace>`, `agy plugin validate [--strict] [--json]`.
- `hooks.json` shape: `{"<hook name>": {"<Event>": [ {"type":"command","command":"…","timeout":N} ]}}` for
  SessionStart, PreInvocation, PostInvocation, Stop (no matcher); PreToolUse/PostToolUse take
  `[{"matcher":"…","hooks":[…]}]`. Wrapping events in `"hooks"` Claude-style is wrong (agy warns).
- Hook env: `ANTIGRAVITY_CONVERSATION_ID` only. The agent's commands also carry it, plus the address and CSRF
  token that `agentapi` needs. Hook parent process: the `agy` process.
- Hook stdin (camelCase): conversationId, cwd, workspacePaths, modelName, transcriptPath, artifactDirectoryPath,
  lastUserInput, hookEventName; PreInvocation + invocationNum, initialNumSteps; Stop + fullyIdle,
  terminationReason, executionNum, continuationActive, error.
- Hook stdout is JSON. SessionStart / PreInvocation: `{"injectSteps":[{"systemMessage":{"systemMessage":"…"}}]}`.
  Stop: `{"decision":"continue","reason":"…"}` goes on; anything else stops. Injected steps stay in the history:
  inject only what is new.
- PreInvocation runs before every model call; `invocationNum` 0 is the first of a turn.
- No session-end hook: the session ends when the `agy` process is gone.

## Design (532)

- Host `antigravity`, marker `ANTIGRAVITY_CONVERSATION_ID`, pointed, home-only, options file of its own, plugin
  root from the hook's cwd, frame of its own (or Claude's), dialect: Antigravity's JSON (a new kind), delivery:
  mailbox read at the next model call (no idle wake yet), stop: yes (Stop hook), history: none until 534.
- The dialect runs the shared start / prompt / stop logic, captures its text, and prints it as `injectSteps`.
  PreInvocation is the prompt hook: at invocationNum 0 it does what a prompt hook does; at a later call it only
  delivers messages that arrived, and prints `{}` when there is nothing new.
- Session key: the conversation id, with the agy process kept for liveness (as Codex's thread key).
- Package directory `antigravity/brigade/`: plugin.json, hooks.json, bin/ (bootstrap, VERSION, checksums.txt
  copies), skills/ (generated), README.md.
- Docs: registry entry, six pages, package page, options, the in-a-team agent page, changelog with "Who must act".

## Order

1. Code: host row, dialect, delivery at next call, session end on process exit; tests.
2. Package directory, skillgen row, `make skills-generate`, package check, Makefile target, CI step, release
   copies, pin if agy installs from a catalog.
3. Docs and registry; `make docs-check`.
4. Gate, package checks, live proof with Frank's agy session (dev build), code-review skill, Codex and Grok
   reviews, push, CI, landing.
