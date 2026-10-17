# Back up the credential

`teams.created_by` is the only administrative authority of a team, and your credential directory is the only
credential that exercises it:

```
~/.config/brigade/teams/<key>/        # or under the config_dir option; mode 0700, session.json inside at 0600
```

Anonymous credentials are unrecoverable by design. `brigade team reset`, a lost or deleted `session.json`, or the
backend's refresh-token reuse detection each ends secret rotation, member revocation and transfer for that team,
**permanently**. The only recovery is a new team and a fresh invitation to everyone.

- Keep a 0700 backup of the directory somewhere you control.
- If you will hand the team over, `brigade team transfer` first ([Transfer the team](../members/transfer-the-team.md));
  after the loss there is nothing to transfer.
- `brigade team leave` does **not** end your administration: `created_by` survives a leave, and a rejoin with the
  current secret restores it. `brigade team reset` does end it, which is why the order in
  [Leave the team](../../team-member/leave/leave-the-team.md) matters.

What the credential is worth to an attacker: [docs/security.md, section 9](../../security.md#9-running-a-team-and-losing-the-ability-to).
