# Know where the authority lives

`teams.created_by` is the only administrative authority of a team, and the creator's credential directory
(`~/.config/brigade/teams/<key>`, or under the `config_dir` option) is the only credential that exercises it.
There is no second role and no recovery flow.

- **What ends it, permanently:** `brigade team reset`; a lost or deleted `session.json`; the backend detecting a
  reused refresh token. Each ends secret rotation, member revocation and transfer for that team.
- **What does not:** `brigade team leave`. `created_by` survives a leave, so a rejoin with the current secret
  restores your administration.
- **What to do about it:** keep a 0700 backup of the directory ([Back up the credential](../create/back-up-the-credential.md)),
  and transfer before any step that ends it.
- **After the loss:** create a new team and re-invite everyone. Nothing else restores it.

Why anonymous credentials are unrecoverable by design, and what the directory is worth to an attacker:
[docs/security.md, section 9](../../security.md#9-running-a-team-and-losing-the-ability-to).
