# Know where the authority lives

One member is the administrative authority of a team: its creator, or whoever the creator transferred it to
(`teams.created_by` in a Supabase project, the team's owner on a Brigade server). That member's credential
directory (`~/.config/brigade/teams/<key>`, or under the `config_dir` option) is the only credential that
exercises it. There is no second role. On Supabase there is no recovery flow; on a Brigade server whoever runs
the server can name a new owner.

- **What ends it:** `brigade team reset`, or a lost or deleted credential file (`session.json` for a Supabase
  team, `credential.json` for a team on a Brigade server); on Supabase also the backend detecting a reused
  refresh token. Each ends secret rotation, member revocation and transfer for that team: for good on Supabase,
  and on a Brigade server until whoever runs it names you again.
- **What does not:** `brigade team leave`. The team remembers who administers it across a leave, so a rejoin
  with the current secret restores your administration.
- **What to do about it:** keep a 0700 backup of the directory ([Back up the credential](../create/back-up-the-credential.md)),
  and transfer before any step that ends it.
- **After the loss, on Supabase:** create a new team and re-invite everyone. Nothing else restores it.
- **After the loss, on a Brigade server:** join the team again with the secret, then ask whoever runs the server
  to name you its owner ([Name a team's owner](../../project-owner/server/name-the-owner.md)). Your credential
  is gone with your machine all the same; what the server's operator restores is the role, to the member you
  are after joining again.
- **A team that moved to a Brigade server** is owned by the first member who joined it there, which is why the
  Team Admin joins first ([Take in a team that exists](../../project-owner/server/take-in-a-team.md)).

Why anonymous credentials are unrecoverable by design, and what the directory is worth to an attacker:
[docs/security.md, section 9](../../security.md#9-running-a-team-and-losing-the-ability-to).
