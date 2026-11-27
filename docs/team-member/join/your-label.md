# Choose your label

Your sessions carry a display label. Teammates see it in the MEMBER column of `brigade sessions`, and
`brigade team members` shows it beside your principal reference: `alice@example.com [9f3c1a20]`.

## The default

The default is **the email address of the Claude account on this machine**, on every harness: in Claude Code the
account this install is signed in to, and in a Muse Code, Codex CLI, Grok Build or Antigravity CLI session the account
Claude Code is signed in to on the same machine. Brigade reads it from Claude Code's own configuration (`.claude.json`, under
`CLAUDE_CONFIG_DIR` when the session's environment carries one, else in your home directory), never writes it,
and sends it with the `team join` that makes you a member. So joining shares that address with everyone on the
team and with whoever runs its backend. On a machine with no Claude account there is no default: the join sends
no label.

The option that changes it is `label`: the plugin option in Claude Code, and the `label` key of
`muse-options.json`, `codex-options.json` or `grok-options.json` under `~/.config/brigade` in the others.

## The option

| `label` | what teammates see |
| --- | --- |
| `account` (the default) | your Claude account email |
| `none` | no label; your sessions are listed by their principal reference alone |
| any other text | that text |

Set it **before** you join: a `team join` sends whatever the option resolves to and prints no label line.
`--label` on `brigade team join` or `brigade team create` overrides the option for that one command. Changing the
option later changes what your next first join on this machine sends; it does not rewrite the label a team already
holds for you, and joining a second checkout of a team you are already on sends no label at all
([Correct your label](#correct-your-label) is how a label a team holds is changed). In your own
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

## Join from a terminal

`brigade team join` in your own terminal reads the default from the Claude account of the profile that terminal
names: the `.claude.json` under `CLAUDE_CONFIG_DIR` when the terminal's environment sets it, else the default
profile's, in your home directory. A terminal that names no profile sends the default profile's address,
whichever profile your sessions of that team run under. On a machine with several Claude profiles, name the label
on the command:

```sh
brigade team join --label 'you@example.com' --secret-file ~/brigade-<team>.secret
```

`BRIGADE_LABEL` in the terminal's environment does the same. A join prints no label line: `brigade team members`
shows the label the team holds for you.

## Correct your label

In a checkout that is joined, name the label you want on the join and give it the team's join secret:

```sh
brigade team join --label 'you@example.com' --secret-file ~/brigade-<team>.secret
```

```
label: team "ops" holds "you@example.com" for this machine's membership now; this machine had joined with "other@example.com"
```

- **It is a join again, as the member you are.** The team keeps your membership and your principal reference and
  takes the label. So it needs the join secret, in a file outside the project or typed at a terminal's prompt
  without echo; in a session, `--secret-file` is the only way. Ask your administrator for the file when you no
  longer have it. Nobody can send it for you: the join is made with your machine's own credential.
- **Without `--label` nothing changes.** `brigade team join` in a checkout that is joined answers
  `already joined`, and a second checkout is joined without a label, whatever the `label` option says. With the
  label the machine joined with, the command answers `unchanged:` and sends nothing. The secret is read before
  anything is recorded: a command that cannot go through leaves the checkout as it was.
- **Teammates see it at once**: the roster, `brigade team members` and the sender line of your messages read the
  label the team holds.
- **What is kept under the earlier label stays there.** A digest subscription: run `brigade report subscribe`
  again in a session that was running when you changed the label, which replaces it; from a session started
  since, subscribe again and take the earlier entry out of `reports.json` by hand
  ([Notes and decisions](../use/notes-and-decisions.md)). Work items assigned to the earlier label keep it:
  assign them again.
- **On a Supabase project and on a Brigade server alike.** Nothing is applied to the backend for it.

## Where your label came from

`brigade whoami`, in a session, prints a `label:` line: the label this machine's membership carries, when it was
sent and where it was read from (`read from the Claude account in <file>`, `from the label option`, `from --label`,
or `typed at team create's prompt`). When the label came from an account, an `account:` line under it says whether
this session's Claude account is that label, and when it is not, why: another profile joined, or this profile has
signed in to another account since. It reads the account where a session start reads it: this profile's own
Claude Code config, then the home directory's. When the profile names no account and the home directory's is
read, the line says so (`this profile names no Claude account, and a start of this session reads the home
directory's`), and `--json` carries `session_account_from_home`. A membership joined before
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
