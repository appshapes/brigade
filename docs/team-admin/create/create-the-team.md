# Create the team

## What you need

| Value | Starts with | Where to get it | Who may have it |
| --- | --- | --- | --- |
| Project URL | `https://<ref>.supabase.co` | the Project Owner, or the Connect dialog of the project | everyone on the team; committed in `.brigade.json` |
| Publishable key | `sb_publishable_` | the Project Owner (the last line of `make backend-install`), or Project Settings → API Keys | everyone on the team; committed in `.brigade.json` |
| Secret key | `sb_secret_` | Project Settings → API Keys | nobody; Brigade never needs it |
| Join secret | none | the file `team create` writes | members, over a password manager only |

The project must be one the Project Owner created for Brigade alone and deployed Brigade into
([Install the project](../../project-owner/install/README.md)): anyone who can read its database can read every
message of every team in it. A project that already holds a Brigade team can hold another: the same URL and key.

## The command

Inside the project checkout, from a session (the `!` prefix runs a command on the shell tool's PATH, where
`brigade` already is) or from your own terminal:

```sh
!brigade team create --url https://<ref>.supabase.co --key sb_publishable_… \
  --name <team> --secret-file ~/brigade-<team>.secret
```

then `git add .brigade.json && git commit && git push`.

- `team create` creates the team, writes `.brigade.json` at the repository's top level and stores your credential
  locally, so your sessions attach at once. It runs only inside a git checkout; with no checkout of the project
  on this machine, run it in an empty `git init` directory and copy the file into each repository.
- `--secret-file` is **required** and must be an absolute path, or one that starts with `~/`, outside the
  repository: the join secret is written there, mode 0600, and never reaches your scrollback, the conversation
  or the repository.
- Its output names the team, the file and the display label it sent for you (`sent your display label:
  alice@example.com (unverified) [9f3c1a20]`), never the secret. `--label` sets another label for this command;
  the option is [Choose your label](../../team-member/join/your-label.md).
- `--force` rewrites an existing `.brigade.json` and carries its `sync` member into the new file when it can.
- `.brigade.json` carries only public values: the backend URL, the publishable key, the team's reference and name.
  The join secret is never in it, so committing it is safe.

## Check it worked

`brigade team status` in the checkout names the team and the project URL, your principal reference and when your
access token expires, never a token. After a member joins, `brigade sessions` lists their session.

## Then

[Hand out the secret](hand-out-the-secret.md) and [Back up the credential](back-up-the-credential.md). On a free
plan the Project Owner arms the keep-alive, or the project pauses after about seven days.
