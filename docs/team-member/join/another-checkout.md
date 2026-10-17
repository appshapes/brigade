# Join another checkout

The credential is per team per machine, not per checkout. A second clone of the project, or another repository
that carries the same `.brigade.json`, needs `team join` once too, but no secret.

## A second clone of the same repository

In a session opened in the new clone, `/brigade:join` with no path; in a terminal, `brigade team join`. It shows
what the file names (in a terminal, you confirm) and you are joined. If a pull changes `.brigade.json` to point at a
different team, your next session prints one line saying so and attaches to nothing until you run `team join` and
review the change; a change of team needs `--secret-file` again.

## Another repository of the same team

A team is not tied to one repository: its identity comes from the adapter, the backend URL and the team reference.
The same `.brigade.json`, committed in several repositories, makes them one team, and sessions in any of them share
one roster. Join each checkout once and `cd` between them; nothing is shared and nothing is switched. Repositories
with different files are different teams.

`brigade sessions` shows which repository each session is in, in its REPO column: the repository's name from its
`origin` remote, else its directory's, unless the member withholds it (`share_workspace_label` off) or replaces it
(`workspace_label`).

## One machine, several Claude accounts

A machine with several `CLAUDE_CONFIG_DIR`s installs the plugin once per account; the joins are shared across them,
because the credential and the pins live in Brigade's own configuration directory, not Claude Code's.

## Where the credential lives

`~/.config/brigade/teams/<key>/session.json`, mode 0600 in a 0700 directory, refused if anyone else can read it.
`brigade team status` in a checkout prints the team, the backend URL, your principal reference and when the access
token expires, never a token; `brigade team list` shows every team joined on this machine and which checkouts are
pinned to each. `team leave`, `team reset` and `team revoke-credentials` act for every checkout of the team on this
machine; no command detaches one checkout. To stop using a team from one checkout, stop opening sessions there.
