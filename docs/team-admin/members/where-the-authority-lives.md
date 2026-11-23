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
- **A team that moved to a Brigade server** is owned by whoever took it in there by request, which is what the
  one command of a move, `brigade team move --to`, does for you ([Move the team to a server](move-the-team.md)).
  A team its operator took in, on a server older than 0.7.0, is owned by the first member who joined it there,
  which is why you join first ([With an older server](move-the-team.md#with-an-older-server)).

## When a member who is not the administrator runs one of its commands

`brigade team rotate-secret`, `brigade team revoke-member`, `brigade team transfer` and the record of a move
(`brigade team move`, `brigade team move --clear`) are the administrator's alone. Anyone else is answered
`unauthorized`, in the words `not an active member of this team`, and that includes an active member of the
team. The words are the same for a team that does not exist, for someone who was never a member or was
revoked, and for a member who is not the administrator, on purpose: a refusal that said more would tell a
stranger whether a team exists and who administers it. So the backend's answer cannot say that you are a member
and not the administrator. The administrator gets the same answer for a principal the backend does not know: a
reference that is mistyped, or a label where a reference belongs.

Brigade says so itself, on the line after the backend's answer, in the same words whoever asked:

```text
brigade: team transfer was answered unauthorized. A backend gives this one answer to everyone but the team's administrator, a member of the team included, and to the administrator for a principal it does not know, so that a refusal tells nobody whether a team exists, who is in it or who administers it. The administrator is the member who created the team, or its owner on a Brigade server; `brigade team members`, asked of the same team, still answers a member.
```

If `brigade team members` lists the roster of that team (give it the same `--team`), you are a member. Then
either the command is the administrator's to run, and you ask them or have them
[transfer the team](transfer-the-team.md) to you; or you are the administrator and the principal you named is
not one the roster shows. The answer on the wire and the exit status, 5, are unchanged, and an adapter that
refuses in words of its own is left to them.

Why anonymous credentials are unrecoverable by design, and what the directory is worth to an attacker:
[docs/security.md, section 9](../../security.md#9-running-a-team-and-losing-the-ability-to).
