# Choose your label

Your sessions carry a display label. Teammates see it in the MEMBER column of `brigade sessions`, and
`brigade team members` shows it beside your principal reference: `alice@example.com [9f3c1a20]`.

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

## Several Claude profiles on one machine

Brigade keeps one credential store per machine user (`~/.config/brigade`; `%APPDATA%\brigade` on Windows), not one
per Claude profile. Every Claude profile on the machine, whatever `CLAUDE_CONFIG_DIR` it runs under, is therefore the
same member, and its sessions carry the label of the profile that joined. A session signed in to another account
still shows that label.

To make a profile a member of its own, set its `config_dir` plugin option to a directory of its own, then join from
a session of that profile: it gets its own principal and sends its own account's email. Teammates see it as another
member.

## Where your label came from

`brigade whoami`, in a session, prints a `label:` line: the label this machine's membership carries, when it was
sent and where it was read from (`read from the Claude account in <file>`, `from the label option`, `from --label`,
or `typed at team create's prompt`). When the label came from an account, an `account:` line under it says whether
this session's Claude account is that label, and when it is not, why: another profile joined, or this profile has
signed in to another account since. It reads only this profile's own Claude Code config. A membership joined before
Brigade recorded the source says `no record says where it came from` and the date this machine's credential for the
team dates from. `brigade whoami --json` carries the same facts in its `label` member, without the file path.

The source is recorded in `label.json` beside the membership's `team.json`, written by the `team create` or
`team join` that sent the label. It holds the source, the Claude Code config file, the time and a SHA-256 of the
label, never the label: a record whose digest does not match the label `team.json` holds is about an earlier label
and is not shown.

## What a label is not

A label proves nothing: anyone can pick any text, copy someone else's, or choose one that looks like the bracketed
principal form an unlabelled session gets. The principal reference, which `brigade sessions --json` and
`brigade team members --json` print in full, is the only identity. No line you read marks a label
`(unverified)`: `brigade team members`, a Slack message and a mail show the label alone.
