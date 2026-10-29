# Record notes and ask for decisions

A project whose `.brigade.json` names a state folder (`"sync": {"folders": [".brigade/state"], "state":
".brigade/state"}`) keeps one state entry per session in it, synced to every teammate's checkout, and a manager
digest is produced from it ([Set up the manager digest](../../team-admin/configure/manager-digest.md)). Two
commands let your session put into that entry what the hooks cannot know: what it did, what comes next, what it
is blocked on, and the decisions it is waiting on. Nothing from your prompts or transcript goes in; the model
writes each line on purpose, as it writes its roster line, and the same refusals apply (no credential shape, no
local path).

## Notes

```bash
brigade note <<'EOF'
Done: the state folder and the entry; the facts layer in the three hooks
EOF
```

One line of at most 1,000 characters on stdin, starting with one of three labels the digest reads: `Done:` for
what shipped, `Next:` for what the session takes up next, `Blocked:` for what it waits on. The entry keeps the
last 20; the digest shows the latest, and lists every session with a `Blocked:` note under its own heading.
Nothing is sent to the backend: the line goes into the entry's two copies (the private one under Brigade's state
directory, the projection in the folder) and reaches teammates through folder sync.

`brigade note` refuses with `config` when the project shares no state folder, or when this session's entry is
not there yet (the next prompt creates it), and with `invalid_input` for an empty line, one over the cap, one
that is not UTF-8, a credential shape or a local path.

## Decisions

```bash
brigade decision --options "ship now | wait" --default wait --by 2026-10-31 --card 162 <<'EOF'
Ship 0.43.0 with the digest, or wait for the Stop hook?
EOF
```

The question travels on stdin; the four flags are optional: the options offered (at most 8, separated by `|`),
the one taken when the manager says nothing (one of the options), the date an answer is needed by
(`YYYY-MM-DD`), the card. The command writes `decisions/<id>.json` into the state folder and sends the ask at
once, in the labelled shape the digest uses, through the first of `reports.json`'s recipients whose gateway is on
the roster: a Slack handle or channel through the Slack gateway, an address through the mail gateway, in the
file's order. With no gateway there it goes to the manager's own best session as a plain team message; with
neither it is recorded and the output says so, and the next digest carries it. The output names the decision
and how it went:

```
recorded: decision d-20261029-a1b2c3
sent: to @rjae through the slack gateway, message 7f3c…
```

The manager answers however they wish, on Slack, by mail, or in person (ruling 4 of the design: nothing is built
for the answer). When the session has acted on it:

```bash
brigade decision --close d-20261029-a1b2c3 <<'EOF'
wait: the Stop hook goes in first
EOF
```

The close records the outcome and who closed it, and the next digest lists it under the decisions closed. Only
the asking session, or another session of the member who asked, closes a decision; a second close is a
`conflict`. `brigade decision` refuses with `config` when the project shares no state folder or its
`reports.json` names no manager (`brigade report configure` writes it), and with `invalid_input` for a default
that is none of the options, a `--by` that is not a date, more than 8 options, or a question that breaks a
note's rules.

## What leaves your machine

A note and a decision go into the project's state folder, which folder sync carries to every teammate's checkout;
a decision's ask goes to the manager through the gateway the recipients name, and from there to Slack or a mail
provider. The digest itself is the watcher's and goes the same way. What that opens:
[docs/security.md, section 12](../../security.md#12-file-sync) and
[section 14](../../security.md#14-the-mail-and-slack-gateways).
