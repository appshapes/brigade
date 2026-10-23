# Rotate a leaked secret

Order matters:

```sh
brigade team rotate-secret --secret-file ~/brigade-<team>.secret   # the file is 0600; the old secret dies now
# the result's secret_version is the NEW version; the version before the rotation is that number minus one
# hand the new secret to every member you want to keep, over a password-grade channel
brigade team members                                                # the roster before the eviction
brigade team revoke-member --max-version <secret_version minus one>
```

- **Rotation alone revokes nobody.** Everyone already in stays in, which is what makes it safe to run first;
  members that are never revoked keep working through a rotation without noticing it.
- **The eviction** removes every member who joined at or below that version, except you, closes their sessions,
  and lets them come back with the new secret. It never bans; a specific principal is banned one at a time with
  `--principal … --ban`. `brigade team members` does not show a member's version; running it before and after
  shows who was evicted.
- **The new secret is written to the file and nowhere else.** It is never printed, `--secret-file` is mandatory
  and must be an absolute path, or one that starts with `~/`, outside the repository, and if the file cannot be
  written after the rotation the secret is gone and the only fix is to rotate again.
- `rotate-secret` runs inside a session or in a terminal; the eviction runs in a terminal.

Then [Hand out the secret](../create/hand-out-the-secret.md) again, to the members you kept.
