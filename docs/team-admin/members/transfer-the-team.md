# Transfer the team

In your own terminal:

```sh
brigade team members --json                # copy .members[].principal_ref; the new creator must be an active member
brigade team transfer --principal <ref>
```

- Afterwards every administrative command of the old creator answers `unauthorized`. The old creator stays an
  ordinary active member with its sessions untouched, and can `brigade team leave` if it also wants out.
- A transfer to a principal that is not an active member is refused; a transfer to yourself is an accepted no-op.
- Run by a member who is not the administrator, it is answered `unauthorized`, in the words `not an active member
  of this team`, which the backend gives everyone but the administrator; Brigade adds a line that says so
  ([Know where the authority lives](where-the-authority-lives.md#when-a-member-who-is-not-the-administrator-runs-one-of-its-commands)).
- Transfer **before** you `brigade team reset` or lose your credential directory: after the loss there is nothing
  to transfer ([Know where the authority lives](where-the-authority-lives.md)).
