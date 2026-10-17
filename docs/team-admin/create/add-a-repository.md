# Add a repository

A team is not tied to one repository. Its identity is derived from the adapter, the backend URL and the team
reference, nothing about the repository, so the **same `.brigade.json`, committed in several repositories, makes
them one team**: sessions in any of them share one roster and message each other directly.

1. Copy `.brigade.json` from a repository already on the team into the new repository's top level.
2. Commit it, together with the `.claude/settings.json` marketplace entry so collaborators install with one
   command ([Install in Claude Code](../../team-member/install/claude-code.md)).
3. In a checkout of the new repository, `brigade team join` once (`/brigade:join` with no path in a session): it
   shows what the file names and you are joined. No secret on a machine that already holds the team's credential.

`brigade sessions` shows which repository each session is in, in its REPO column; `--here` narrows the roster to
one repository. A member who works across several repositories joins each checkout once and `cd`s between them;
nothing is shared and nothing is switched. Folder sync is per repository: the same folder listed in two
repositories of one team syncs as two folders.
