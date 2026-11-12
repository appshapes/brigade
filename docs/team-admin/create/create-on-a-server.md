# Create the team on a server

A team can live on a [Brigade server](../../project-owner/server/README.md), a program the Project Owner runs on
a machine of your organisation's own, in place of a Supabase project. Everything a member does afterwards is the
same. The server is experimental. A team on one has the mail and Slack gateways when the Project Owner has opened
them ([Open the gateways](../../project-owner/server/gateways.md)), from 0.2.0 of the server.

## What you need

| Value | Looks like | Where to get it | Who may have it |
| --- | --- | --- | --- |
| Server URL | `https://<host>:8743` | the Project Owner | everyone on the team; committed in `.brigade.json` |
| Certificate fingerprint | `sha256:` and 64 hex digits | the Project Owner, from the server's start line; only for a certificate members' machines do not already trust | everyone on the team; committed in `.brigade.json` |
| Creation token | a file the Project Owner gives you | the Project Owner; only if the server requires one | you; keep the file outside the repository |
| Join secret | none | the file `team create` writes | members, over a password manager only |

Every member's plugin must be 0.52.0 or later: an earlier one reads a server's team file as broken and connects
to nothing. The VERSION column of `brigade sessions` shows who is behind.

## The command

Inside the project checkout, from a session (with the `!` prefix) or from your own terminal:

```sh
!brigade team create --adapter rest --url https://<host>:8743 \
  --certificate sha256:… --name <team> --secret-file ~/brigade-<team>.secret
```

then `git add .brigade.json && git commit && git push`.

- `--adapter rest` is what says the backend is a Brigade server. There is no `--key`: a server has none.
- `--certificate` is the fingerprint of the server's own certificate. Leave it out when the server's certificate
  comes from a public authority. It is read as you copy it, from the server's start line or in the colon form
  `openssl` prints, and is written to the file in one spelling.
- `--create-token-file <path>` names the file that holds the creation token, when the Project Owner set one. The
  token is read from the file and is never an argument, and nothing of it is written to `.brigade.json`.
- `--name`, `--label`, `--secret-file` and `--force` are as in [Create the team](create-the-team.md).
- `.brigade.json` then holds `"adapter": "rest"`, the URL, the fingerprint when you gave one, and the team's
  reference and name: public values only.

## What the fingerprint does

With a fingerprint in the file, a member's sessions accept the certificate that has it and no other, whoever
signed it and whatever name it carries. A member sees the fingerprint in the line `team join` asks them to
confirm. If the server's certificate is replaced, put the new fingerprint in `.brigade.json` and commit it; each
member's next session then prints one line and connects to nothing until they run `brigade team join`, which
shows the old and the new fingerprint and needs no secret.

## Check it worked

`brigade team status` in the checkout names the team and the server's URL. After a member joins, `brigade
sessions` lists their session.

## Then

[Hand out the secret](hand-out-the-secret.md) and [Back up the credential](back-up-the-credential.md), as for any
team.
