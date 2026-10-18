# Choose your label

Your sessions carry a display label. Teammates see it in the MEMBER column of `brigade sessions`, and
`brigade team members` shows it beside your principal reference: `alice@example.com (unverified) [9f3c1a20]`.

## The default

In Claude Code the default is **the email address of the Claude account this install is signed in to**. Brigade
reads it from Claude Code's own configuration (`.claude.json`, under `CLAUDE_CONFIG_DIR` when set), never writes
it, and sends it once, with the `team join` that makes you a member. So joining shares that address with everyone
on the team and with whoever runs its backend. A Muse Code or Codex CLI join reads no Claude configuration: its
label is the `label` key of `muse-options.json` or `codex-options.json`, or nothing.

## The option

| `label` | what teammates see |
| --- | --- |
| `account` (the default) | your Claude account email |
| `none` | no label; your sessions are listed by their principal reference alone |
| any other text | that text |

Set it **before** you join: a `team join` sends whatever the option resolves to and prints no label line.
`--label` on `brigade team join` or `brigade team create` overrides the option for that one command. Changing the
option later changes what your next first join on this machine sends; it does not rewrite the label a team already
holds for you, and joining a second checkout of a team you are already on sends no label at all. In your own
terminal, `BRIGADE_LABEL` stands in for the option; inside a session Brigade ignores it, as it ignores every
`BRIGADE_*` variable.

A member who joined before labels existed has an empty label, filled from the default at the next session start
after the backend has the matching migration, and only while the membership has none; a label you chose is never
touched.

## What a label is not

A label proves nothing: anyone can pick any text, copy someone else's, or choose one that looks like the bracketed
principal form an unlabelled session gets. The principal reference, which `brigade sessions --json` and
`brigade team members --json` print in full, is the only identity; `brigade team members` marks every label
`(unverified)`.
