# Know what an update changes

## What your sessions share

An update can change what your sessions share, and the version's section in [CHANGELOG.md](../../../CHANGELOG.md)
says so. Two examples that already shipped: from 0.10.0 every session reports its Brigade version to the roster;
the release that added `brigade doing` lets your session's model publish one sentence about its work, where your
permission settings let Brigade ask ([What teammates see about your session](../use/your-session-on-the-roster.md)).

## Who is behind

The VERSION column of `brigade sessions` shows each session's plugin version. Two moments where it matters:

- **Before a project commits a `sync` or a `polling` member**, every member's plugin must be 0.11.0 or later. An
  older plugin reads `.brigade.json` against a closed schema and refuses the whole file: its sessions say
  `Brigade: not connected (config: team_file_unknown_field): fix .brigade.json.` and connect to nothing until that
  member updates.
- **Before a project names a state folder** (`sync.state`, 0.43.0), members on an older plugin still connect and
  sync the folder, but keep no state entry and are listed roster-only in the manager digest; 0.43.1 is the first
  release whose digest reaches a manager on Slack by their email address.
- **Around 0.16.0**, which changed how a synced folder's id is made: members on either side of it do not exchange
  files until the last one has updated ([Sync folders](../use/sync-folders.md)).

## Plugin versions and migrations are independent

A migration only ever appends RPC parameters with defaults, so an older adapter works unchanged on a migrated
project, and a newer adapter works on a project not migrated yet: it names a new parameter only when it has a value
for it, and when the project refuses one it sends the call again without it, drops that value rather than the call,
says so once on stderr, and tries again every ten minutes. What you lose until the owner migrates is only what the
migration adds, per migration: the MODEL and CONTEXT columns, the VERSION column, the label fill, the sync peer.
A blank VERSION cell is therefore an answer too: that plugin predates 0.10.0, or the project lacks the migration
that stores it.
