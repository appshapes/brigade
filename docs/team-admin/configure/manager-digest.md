# Set up the manager digest

```json
"sync": { "folders": [".brigade/state"], "state": ".brigade/state" }
```

A project that names one of its synced folders as its **state folder** has every session of its checkouts keep
one state entry there, `sessions/<session id>.json`, synced to every teammate's checkout by folder sync
([Set the folders to sync](sync-folders.md)): who the member is, the session's name, repository, branch and host,
when it started, last prompted and ended, its inbound policy, the commits its author made since it started (hash,
subject, the card number the subject starts with, time), the doing lines it published, and the notes and decisions
its model records on purpose ([Record notes and ask for decisions](../../team-member/use/notes-and-decisions.md)).
Nothing from a prompt or a transcript. From the folder, the team's own active sessions produce the manager
digest: a report of the decisions waiting on the manager and the meaningful contributions since the last one,
delivered through the team's Slack or mail gateway. No manager node runs anywhere: a quiet team reports nothing.

## The rules

- `state` names one of `folders`, spelled exactly as the list spells it. A `state` that is not a string or
  names no listed folder makes the whole `sync` member unusable (`state_invalid`, `state_not_listed`), as any
  other broken rule does: the session connects, without file sync, and says why.
- The folder is a directory of the checkout: list it in `.gitignore`. The entries are a few KiB each; a session's
  entry keeps the last 100 commits, 50 doing lines and 20 notes.
- A member whose `sync` option is off shares no entry: the digest lists their sessions from the roster alone.
- **Retention.** A live entry stays while its session lives; an ended one 14 days; one whose machine never came
  back to end it 14 days after its last write; a decision 30 days after its close, and for good while open. Each
  machine prunes its own at session start, and any session's watcher prunes the folder hourly.

## What each machine writes

The hooks write the entry: session start creates it (and refreshes it on `/clear` and a resume), each prompt
stamps it and lists the new commits when HEAD moved (the one `git log` Brigade runs), session end records the
end. The canonical copy lives under Brigade's private state directory and every write projects it into the
folder, whole-file, so an entry a teammate's machine deleted comes back at the writer's next hook, and one writer
per file means the sync engine makes no conflict copy. A folder entry is a teammate's text to every reader: capped
and sanitised, never trusted as a fact.

## What this opens

Every teammate's machine holds every session's entry, as it holds every synced folder
([docs/security.md, section 12](../../security.md#12-file-sync)): the members' labels, session names, branch
names, commit subjects, doing lines and notes of the team, and the decisions it asks its manager. A decision's
ask leaves the backend through the gateway for Slack or a mail provider
([section 14](../../security.md#14-the-mail-and-slack-gateways)).
