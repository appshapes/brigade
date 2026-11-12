# Know the team file

`.brigade.json`, committed at the repository's top level, names the team every session in that checkout talks to.

```json
{
  "adapter": "supabase",
  "url": "https://<ref>.supabase.co",
  "publishable_key": "sb_publishable_…",
  "team_ref": "6f0f2b41-…",
  "team_name": "ops",
  "sync": { "folders": ["docs/shared", ".brigade/state"], "state": ".brigade/state" },
  "polling": { "heartbeat_seconds": 100, "roster_seconds": 300 },
  "gateway": { "email": "brigade-team@example.com" }
}
```

- **Public values only.** The backend URL, the publishable key, the team's reference and name; the join secret is
  never in it. `team create` writes the first four; `sync` and `polling` you add by hand; `gateway` the installers
  write. `sync.state` names the state folder of the digests, a listed folder or a path under one ([Set up the
  digests](digests.md)); a plugin before 0.43.0 ignores it, syncs the folder and keeps no entry, and one before
  0.45.0 refuses a state under a listed folder (`state_not_listed`) and syncs nothing. `sync.board` set to `true`
  turns on the Brigade Board, the team's board of work items ([Turn on the Brigade Board](board.md)); a plugin
  of 0.49.0 or earlier ignores it and has no board.
- **`adapter`** names the backend's kind. `supabase` and `rest` are bundled with Brigade; any other name resolves
  to a command through each member's own `adapters.json` ([docs/adapter-authors.md](../../adapter-authors.md)).
- **A team on a Brigade server** has `"adapter": "rest"`, the server's `url` and no `publishable_key`. When no
  authority signed the server's certificate, the file also has `"certificate": "sha256:…"`, that certificate's
  SHA-256 fingerprint in lowercase hex, and a member's sessions then trust that certificate and no other. `team
  create --adapter rest` writes all of it. A file that names another certificate is a changed file: each member
  reviews the change with `team join`, which needs no secret for it. A plugin before the release that carries
  this reads such a file as broken and connects to nothing.
- **A member this version does not define** is ignored and named in one line at session start.
- **Size and versions.** A plugin of 0.10.0 or earlier reads the file against a closed schema and refuses the whole
  file for an unknown member (`team_file_unknown_field`) or a file over 4096 bytes (`team_file_too_large`); its
  sessions connect to nothing until that member updates. So every member's plugin must be 0.11.0 or later before
  a `sync` or `polling` member is committed. The VERSION column of `brigade sessions` shows who is behind.
- **A changed file** reaches a session at its next start or `/clear`. A pull that changes the file to point at a
  different team makes the next session print one line and attach to nothing until the member runs `team join`
  and reviews the change.
- **What the member cannot do.** It cannot point a session at another backend without the member's review, cannot
  widen what a session trusts, and its timer bounds are fixed in the plugin; each session names non-default values
  in one line at start.
