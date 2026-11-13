# Rotate a leaked secret

Order matters:

```sh
brigade team rotate-secret --secret-file ~/brigade-<team>.secret   # the file is 0600; the old secret dies now
# the result's secret_version is the NEW version; the version before the rotation is that number minus one
# hand the new secret to every member you want to keep, over a password-grade channel
brigade team members --json                                         # the roster before the eviction, with each member's joined_secret_version
brigade team revoke-member --max-version <secret_version minus one>
```

- **Rotation alone revokes nobody.** Everyone already in stays in, which is what makes it safe to run first;
  members that are never revoked keep working through a rotation without noticing it.
- **The eviction** removes every member who joined at or below that version, except you, closes their sessions,
  and lets them come back with the new secret. It never bans; a specific principal is banned one at a time with
  `--principal … --ban`. `brigade team members --json` shows the version each member joined with, as
  `joined_secret_version` (from 0.53.0), so you can see who the eviction will remove before you run it.
- **The new secret is written to the file and nowhere else.** It is never printed, `--secret-file` is mandatory
  and must be an absolute path, or one that starts with `~/`, outside the repository, and if the file cannot be
  written after the rotation the secret is gone and the only fix is to rotate again.
- `rotate-secret` runs inside a session or in a terminal; the eviction runs in a terminal.
- **On a Brigade server that carries the gateways**, a gateway is a member that joined with the old secret, so
  the eviction removes it too. Give the new secret file to whoever runs the server before you evict
  ([Open the gateways](../../project-owner/server/gateways.md#after-the-join-secret-is-rotated)).

Then [Hand out the secret](../create/hand-out-the-secret.md) again, to the members you kept.
