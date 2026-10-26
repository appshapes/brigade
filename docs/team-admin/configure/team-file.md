# Know the team file

`.brigade.json`, committed at the repository's top level, names the team every session in that checkout talks to.

```json
{
  "adapter": "supabase",
  "url": "https://<ref>.supabase.co",
  "key": "sb_publishable_…",
  "team_ref": "6f0f2b41-…",
  "team_name": "ops",
  "sync": { "folders": ["docs/shared"] },
  "polling": { "heartbeat_seconds": 100, "roster_seconds": 300 },
  "gateway": { "email": "brigade-team@example.com" }
}
```

- **Public values only.** The backend URL, the publishable key, the team's reference and name; the join secret is
  never in it. `team create` writes the first four; `sync` and `polling` you add by hand; `gateway` the installers
  write.
- **`adapter`** names a backend other than the bundled Supabase one: `syncthing`, the other bundled adapter, or a
  name that resolves to a command through each member's own `adapters.json`
  ([docs/adapter-authors.md](../../adapter-authors.md)).
- **A team over Syncthing** ([Create a team over Syncthing](../create/create-over-syncthing.md)) has no `url` and
  no `publishable_key`; it has `devices`, the Syncthing device ids of its seed machines, which a joining machine
  connects to first. `team create --adapter syncthing` writes your machine's; add a member's by hand (the `peer`
  line of their `brigade sync status`) and commit, and a plugin before 0.39.0 refuses the file.

  ```json
  { "version": 1, "adapter": "syncthing", "devices": ["MFZWI3D-…"], "team_ref": "6f0f2b41-…", "team_name": "ops" }
  ```
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
