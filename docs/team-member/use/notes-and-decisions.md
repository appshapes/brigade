# Record notes and ask for decisions

A project whose `.brigade.json` names a state folder (`"sync": {"folders": [".brigade"], "state":
".brigade/state"}`) keeps one state entry per session in it, synced to every teammate's checkout, and a digest
is produced from it for every member who subscribes ([Set up the digests](../../team-admin/configure/digests.md)).
Two commands let your session put into that entry what the hooks cannot know: what it did, what comes next, what
it is blocked on, and the decisions it is waiting on, each addressed to a person; a third subscribes you to the
digest. Nothing from your prompts or transcript goes in; the model writes each line on purpose, as it writes its
roster line, and the same refusals apply (no credential shape, no local path).

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
brigade decision --to rjae@example.com --options "ship now | wait" --default wait --by 2026-10-31 --card 162 <<'EOF'
Ship 0.43.0 with the digest, or wait for the Stop hook?
EOF
```

The question travels on stdin. `--to` names the person it is for, by their label as the roster shows it
(`brigade sessions`), and is required: a decision with no addressee is refused (`invalid_input`, `to_needed`),
never guessed. The four other flags are optional: the options offered (at most 8, separated by `|`), the one
taken when the person says nothing (one of the options), the date an answer is needed by (`YYYY-MM-DD`), the
card. The command writes `decisions/<id>.json` into the state folder and sends the ask at once, in the labelled
shape the digest uses: when that person subscribed to the digest, through the first of their recipients a
gateway on the roster can carry (a Slack handle, channel or address through the Slack gateway, which finds a
person by their email; an address through the mail gateway when the team has no Slack gateway); otherwise, or
with no gateway there, to their own best session as a plain team message; with neither it is recorded and the
output says so, and the next digest carries it. A label two members share names nobody: the decision is recorded
unsent. The output names the decision and how it went:

```
recorded: decision d-20261029-a1b2c3
sent: to rjae@example.com at @rjae through the slack gateway, message 7f3c…
```

The person answers however they wish, on Slack, by mail, in person, or by a team message (ruling 4 of the design:
nothing is built for the answer). When the session has acted on it:

```bash
brigade decision --close d-20261029-a1b2c3 <<'EOF'
wait: the Stop hook goes in first
EOF
```

The close records the outcome and who closed it, and the next digest lists it under the decisions closed. Only
the asking session, or another session of the member who asked, closes a decision; a second close is a
`conflict`. `brigade decision` refuses with `config` when the project shares no state folder, and with
`invalid_input` for no `--to`, a default that is none of the options, a `--by` that is not a date, more than 8
options, or a question that breaks a note's rules.

## Subscribe to the digest

```bash
brigade report subscribe
brigade report subscribe --recipients "@rjae | rjae@example.com" --every 4h
brigade report subscribe --recipients "#eng-digests" --at 08:00 --tz America/Los_Angeles --only mine
brigade report unsubscribe
```

`/brigade:subscribe` is the one-line form, and `/brigade:subscribe off` unsubscribes. `brigade report
subscribe`, inside a session, writes your own entry into `reports.json` in the state folder, under the label your
session's entry carries: your recipients in order of precedence (a Slack handle, a channel, an email address;
none means your label when it is an address, which the Slack gateway finds you by, and else your own best
session), your period (`--every`, `24h` when unset, or `--at` with `--tz` for a daily digest), and `--only mine`
to list only the decisions addressed to you, the contributions staying the team's. A second `subscribe` replaces
the first; `unsubscribe` removes it. Nobody writes anyone else's entry: there is no `--label`. **The address
rule:** an email address, one you give or your label as the default, is written only when git ignores the
folder; add it to `.gitignore` or name a Slack handle or channel. A session with no state entry yet
(`no_state_entry`) or no roster label (`label_needed`) cannot subscribe.

Your digest then comes from whichever teammate's session is active, on your period, by your precedence, and a
decision addressed to you reaches the same recipients at once.

## The digest itself

`brigade report show`, inside a session, prints the digest you would get now — your own subscription's view, or
everything since the start when you have none; `--to <label>` another subscriber's — as plain text with a stable
label at the start of every line: the header (team, repository, period, producer), the decisions open (oldest
first, with whom each is for and its age) and closed in the period — under `--only mine`, only the ones addressed
to you — the commits shipped (one per hash, the carded ones first, at most 40, each under the session whose
checkout made it), the sessions blocked, active (at
most 30), ended in the period, held or quiet, and the sessions on the roster with no entry. Ask for it in words;
your session prints it verbatim. The period's digest is produced by whichever session is active, once per
subscriber ([Set up the digests](../../team-admin/configure/digests.md)).

## What leaves your machine

A note and a decision go into the project's state folder, which folder sync carries to every teammate's checkout;
a decision's ask goes to the person it names through the gateway their recipients name, and from there to Slack or
a mail provider, or to their session as a team message. Your digest is the watcher's and goes the same way, by
your recipients. What that opens: [docs/security.md, section 12](../../security.md#12-file-sync) and
[section 14](../../security.md#14-the-mail-and-slack-gateways).
