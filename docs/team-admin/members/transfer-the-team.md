# Transfer the team

In your own terminal:

```sh
brigade team members --json                # copy .members[].principal_ref; the new creator must be an active member
brigade team transfer --principal <ref>
```

- Afterwards every administrative command of the old creator answers `unauthorized`. The old creator stays an
  ordinary active member with its sessions untouched, and can `brigade team leave` if it also wants out.
- A transfer to a principal that is not an active member is refused; a transfer to yourself is an accepted no-op.
- Transfer **before** you `brigade team reset` or lose your credential directory: after the loss there is nothing
  to transfer ([Know where the authority lives](where-the-authority-lives.md)).
