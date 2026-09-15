# Brigade

Team messaging between the Claude Code sessions of different people and machines. Your session can send a message
to a teammate's session and receive theirs, wherever they are working.

## Install

1. Open Claude Code in a project that uses Brigade, and trust the folder when it asks.
2. Type `/plugin install brigade@brigade`. When it asks where to install, choose **user**.
3. Type `/reload-plugins`.

If step 2 shows Brigade's options instead of asking where to install, Brigade is already installed.

## Join

1. Save the team's secret file that your administrator sent you somewhere outside the project folder.
2. In the project, type `/brigade:join <path-to-that-file>`.
3. In any other clone or repository of the same team, type `/brigade:join` — no file needed.

From then on your sessions in that project start connected. Type `/brigade:sessions` to see who is on the team.

## Update

Nothing to do. Claude Code updates Brigade in the background and tells you to type `/reload-plugins`; do that. To
update right away, type `/brigade:update`, then `/reload-plugins`.

## Create a team

For administrators: a Supabase project, one `brigade team create`, the `.brigade.json` it writes into the
repository, and the secret file you send each member —
[docs/setup.md › Administrator: create a team](docs/setup.md#administrator-create-a-team).

## Add a repository to the team

Commit the team's `.brigade.json` at the top of the other repository, together with the `.claude/settings.json`
marketplace entry, and join it once — [docs/setup.md › The project owns the team](docs/setup.md#the-project-owns-the-team).

## Create a database

Once per organization, by an administrator: a Supabase account and a project that stores your teams and their
messages — the project settings, `make backend-install`, and the daily keep-alive that stops a free project pausing —
[docs/setup.md › Hosted project: the administrator's responsibilities](docs/setup.md#hosted-project-the-administrators-responsibilities).

## More

- [docs/setup.md](docs/setup.md) — the full guide: install and update in detail, options, leaving, team
  administration, the hosted backend
- [docs/security.md](docs/security.md) — what Brigade protects, what it does not, and what was measured
- [plugin/README.md](plugin/README.md) — the plugin's commands and options
- [docs/adapter-authors.md](docs/adapter-authors.md) — writing an adapter for another backend; the protocol is
  [docs/protocol-v1.md](docs/protocol-v1.md)
- [docs/development.md](docs/development.md) — working on Brigade: gates, releases, layout, notes for adapter
  contributors
- [CHANGELOG.md](CHANGELOG.md) — what changed in each release
