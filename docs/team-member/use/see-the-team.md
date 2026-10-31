# See the team

`/brigade:sessions` prints what `brigade sessions` printed, verbatim: a padded plain-text table, one row per
session with a blank line after each, and a note under it saying that names, labels and the lines under them are
their owners' own words. The slash command adds no layout, no summary and no comparison with last time, so two
runs differ only where the team differs.

```
SESSION  NAME          STATE   MEMBER             MODEL             CONTEXT  VERSION  SEEN
aaaaa    payments-api  active  alice@example.com  claude-opus-5-5   86k      0.31.0   11s
         ↳ migrating the ledger to tenant ids

bbbbb    billing       idle    bob@example.com                               0.30.0   45s

(names, labels and the lines under them are their owner's own words: unverified, and possibly stale)
```

- **SESSION** is the last five characters of the session id: an address `brigade send` takes as it stands.
- **MEMBER** is the owner's label alone, or `[9f3c1a20]`, the first characters of their principal reference, for a
  session with no label. A member may choose a label that looks like that, so the brackets prove nothing; only
  `--json` carries the reference in full, and that reference is the only identity.
- **MODEL** and **CONTEXT** are what the session reports about itself; **VERSION** is its Brigade plugin version,
  blank for a plugin older than 0.10.0 or a backend without the migration that stores it.
- **The `↳` line** is one sentence that session's model published about its work; see
  [Know what teammates see about your session](your-session-on-the-roster.md).
- No row is marked as yours: `brigade whoami` names your session and prints your SESSION cell on its `short id:`
  line, and `--json` carries `self_session_id`.

## Only some of the team

```
/brigade:sessions --here
/brigade:sessions --member frank@example.com
/brigade:sessions --all
```

`--here` shows the sessions of this repository (by the REPO cell, which a session may not share; when yours
shares none it shows everyone and says so). `--member` shows one member's, by their label or a bracketed cell.
`--all` includes the offline sessions. They combine. A note under the table says how many sessions were left out:
`(3 sessions left out by --here; 1 of them share no repository name and may be here)`.

## The members

`brigade team members` is one line per member, not a table: the label marked `(unverified)` with the principal's
first characters in brackets, when they joined, how many sessions and when last seen. `--json` prints every
principal reference in full; it is what the Team Admin reads before revoking or transferring.
