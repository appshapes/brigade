# Gate what your session sends

Claude Code's permission rules are how you gate what a model may send. Three rules behave differently.

```json
{ "permissions": { "allow": ["Bash(brigade:*)"] } }
```

Removes the prompts in Manual mode. It does nothing in bypass mode, where nothing prompts anyway. It is also what
lets Brigade remind the model to keep its roster line current outside `bypassPermissions`
([What teammates see about your session](../use/your-session-on-the-roster.md)).

```json
{ "permissions": { "ask": ["Bash(brigade send*)"] } }
```

Prompts you in Manual mode **and in bypass mode**, with exactly two choices, `Yes` and `No`, no "don't ask again".
But it **denies** the call in `claude -p` and under `dontAsk`, so an unattended worker carrying it sends nothing.

```json
{ "permissions": { "deny": ["Bash(brigade send*)"] } }
```

Blocks sending in **every** mode, `bypassPermissions` included. No hook and no plugin can override an ask or a deny.

## What the send rules do not cover

- `brigade doing`, the one-sentence roster line, is a different command. Gate it with `share_doing: false`, or
  with an ask or deny on `Bash(brigade doing*)`, or name every verb at once with `Bash(brigade:*)`. An ask or deny
  Brigade can read there also stops its reminder: it never asks the model to run a command your settings say would
  prompt or be refused.
- File sync: no rule sees it; `sync: off` is the switch.
- The in-session `team create`, `team join` and `team rotate-secret`: a `Bash(brigade:*)` allow covers them too.
  For a dialog on them in every mode, add per-verb rules (`"ask": ["Bash(brigade team create*)", …]`), not
  `Bash(brigade team*)`, which would also gate `brigade team members`.

## The text-matching limit

The rules match the whole command text, so the cached binary's full path and `sh -c '…'` are different text that
neither rule sees. Brigade's session-start line names only the bare `brigade` (15 of 15 idle wakes replied in the
bare form, measured), the skills forbid the evasive spellings, and a model that deliberately evades the rule is a
residual risk this version accepts. A command over 10,000 characters always prompts in Manual mode and is denied
in `claude -p`: send anything over about 8 KB with `brigade send --body-file <path>`.

The whole argument: [docs/security.md, section 5](../../security.md#5-sending-what-the-ask-and-deny-rules-stop-and-what-they-miss).
