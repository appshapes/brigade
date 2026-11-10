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
card. The command writes `decisions/<id>.json` into the state folder and sends the ask at once: when that person
subscribed to the digest, through the first of their recipients a
gateway on the roster can carry (a Slack handle, channel or address through the Slack gateway, which finds a
person by their email; an address through the mail gateway when the team has no Slack gateway); otherwise, or
with no gateway there, to their own best session as a plain team message; with neither it is recorded and the
output says so, and the next digest carries it. A label two members share names nobody: the decision is recorded
unsent. The output names the decision and how it went:

```
recorded: decision d-20261029-a1b2c3
sent: to rjae@example.com at @rjae through the slack gateway, message 7f3c…
```

The ask is the same text on Slack, by email and as a session message:

```
Ship 0.43.0 with the digest, or wait for the Stop hook?

- ship now
- [default] wait

Answer by 2026-10-31.
```

The question, then the options one to a line with the default marked `[default]`, then `Answer by <date>.` when a
date was given. The sender line above it names the asking session and its member. The decision id, the addressee,
the asker, the repository and the card are not in it: the id is in this command's output and names the decision's
file. A mail's subject is the question; a session message's summary is `Decision for <label>: <question>`, which
tells the receiving session's model whom to show it to. The gateways show an ask from a plugin of 0.47.2 or older,
which still sends the labelled lines, in the same shape.

### A decision for the session's own user

When `--to` names the member the asking session belongs to, that person is the one in the conversation. The
session asks them there, the host's own way, and the command records the decision without sending it:

```
recorded: decision d-20261110-a1b2c3
held: rjae@example.com is this session's own user, so ask them here in the conversation; it is sent to them at 02:25 UTC (in 10m0s) only if no prompt arrives here and it is still open — close it with the outcome when they answer
```

The asking session's watcher ends the hold. A prompt in that conversation after the ask ends it for good with
nothing sent; so does a close, from that session or from another session of the member. Otherwise, once the wait has passed, the ask goes out by the road above (the first
gateway of the member's recipients on the roster, else another session of theirs, never the asking one), and the
session is told at its next prompt. The wait is ten minutes, or what the member's subscription sets
([`--wait`](#subscribe-to-the-digest)); a wait of `0` sends at once, as before.

- **Whose user.** The label is the one the session's own entry carries, or one whose roster sessions all belong to
  the asking session's member: one machine user is one member, whichever Claude profile is signed in. A label a
  second member also carries is not held. A decision for anyone else is always sent at once: no conversation of
  the asking session reaches them.
- **A prompt** is whatever starts a turn in that conversation: one the user types, and also a team message or a
  finished background task that wakes the session. Brigade reads no prompt, so it cannot tell them apart; a hold
  such a turn ends is not sent, and the decision stays open.
- **A hold needs a conversation and a watcher.** A non-interactive session, or one whose watcher is not running
  or is another version's, sends at once.
- **Nothing sent.** A session that is not running when the wait ends sends nothing. With no gateway of the
  member's recipients and no other session of theirs on the roster, or a send that fails, the ask is tried again
  for fifteen minutes after the wait and then given up, and the session is told. Either way the decision is open,
  and the digest lists it.
- **The digest** leaves a decision out while its hold runs, and afterwards marks one that was never sent `asked in
  the conversation`.

The person answers however they wish, on Slack, by mail, in person, by a team message, or in the conversation
(ruling 4 of the design: nothing is built for the answer). When the session has acted on it:

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
brigade report subscribe --recipients "@rjae" --wait 30m
brigade report unsubscribe
```

`/brigade:subscribe` is the one-line form, and `/brigade:subscribe off` unsubscribes. `brigade report
subscribe`, inside a session, writes your own entry into `reports.json` in the state folder, under the label your
session's entry carries: your recipients in order of precedence (a Slack handle, a channel, an email address;
none means your label when it is an address, which the Slack gateway finds you by, and else your own best
session), your period (`--every`, `24h` when unset, or `--at` with `--tz` for a daily digest), `--only mine`
to list only the decisions addressed to you, the contributions staying the team's, and `--wait` for how long a
decision one of your own sessions asks you is held before it is sent (`10m` when unset, `24h` at most, `0` for at
once). A second `subscribe` replaces the first, every flag of it; `unsubscribe` removes it. Nobody writes anyone else's entry: there is no `--label`. **The address
rule:** an email address, one you give or your label as the default, is written only when git ignores the
folder; add it to `.gitignore` or name a Slack handle or channel. A session with no state entry yet
(`no_state_entry`) or no roster label (`label_needed`) cannot subscribe.

Your digest then comes from whichever teammate's session is active, on your period, by your precedence, and a
decision addressed to you reaches the same recipients at once — or, when one of your own sessions asked it, after
your wait and only if you did not answer in that conversation
([A decision for the session's own user](#a-decision-for-the-sessions-own-user)).

## The digest itself

`brigade report show`, inside a session, prints the digest you would get now — your own subscription's view, or
everything since the start when you have none; `--to <label>` another subscriber's — as plain text: the header
(team, repository, period, producer), then one section per heading.

- **Decisions open** (oldest first) and **closed in the period**; under `--only mine`, only the ones addressed to
  you. An open one is its question, its options one to a line with the default marked `[default]`, then one line
  saying whom it is for, who asked, how long it has been open, `asked in the conversation` when it was held for
  the asker's own user and never sent, and the date an answer is needed by. A closed one is its question, then who
  closed it and the outcome. A decision whose hold still runs is not listed.
- **Shipped**: the commits, one per hash, the carded ones first, at most 40, each under the session whose checkout
  made it.
- **Blocked**: every session whose latest note starts `Blocked:`.
- **Active now**: one block per session, its name and member on the first line, then `doing:` and `note:` lines
  for its doing line and its latest note, at most 30 sessions. A session that published neither is left out and
  not counted.
- **Ended in the period**: the same shape, with the commits credited to the session and its last note. A session
  that ended with no commits and no note is counted on the last line and not listed.
- **Held or quiet**, and the sessions on the roster with no entry.

A session on the roster is named as the roster names it now, with its member's label. One that has ended keeps
what its own entry recorded; an entry that recorded no member and only the repository's name reads `an unnamed
<host> session`. Ask for the digest in words; your session prints it verbatim. The period's digest is produced by
whichever session is active, once per subscriber ([Set up the digests](../../team-admin/configure/digests.md)).

## What leaves your machine

A note and a decision go into the project's state folder, which folder sync carries to every teammate's checkout;
a decision's ask goes to the person it names through the gateway their recipients name, and from there to Slack or
a mail provider, or to their session as a team message; one for the asking session's own user only after their
wait. Your digest is the watcher's and goes the same way, by
your recipients. What that opens: [docs/security.md, section 12](../../security.md#12-file-sync) and
[section 14](../../security.md#14-the-mail-and-slack-gateways).
