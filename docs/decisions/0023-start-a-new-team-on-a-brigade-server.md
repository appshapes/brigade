# 0023. Start a new team on a Brigade server

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

Every page and skill steered a new team to Supabase. The setup skills of the packages carried only the Supabase
form of `team create`, no skill named `--adapter rest`, and the test that holds the setup commands equal had no
witness for a server (work item 376, the repository review of 2026-11-21). Only one page of the Team Admin's had
the server form, the Project Owner's root named the server second, and the server's own page called it
experimental.

Meanwhile the owner's own teams run on servers, more are moving, and whether the two Supabase projects can be
deleted is asked (work item 330). The server's gateways are a hand port of the Supabase Edge Functions, with
nothing mechanical keeping the two in step, and they have drifted: a fix made in one has had to be made again
in the other.

The owner ruled on 2026-11-22, in two answers recorded on the work item: "Server first", and, for what the
server's page says in place of "experimental", "It is one instance with no standby yet; a standby on a second
node is planned."

## Decision

A new team starts on a Brigade server.

- The setup skills of the four packages lead with the server form of the command, `brigade team create --adapter
  rest --url https://<host>:8743 --name <team> --secret-file <path>`, and give the Supabase form after it.
- The Team Admin's pages lead with the server: the topic root's first action is to create the team on a server,
  and the Supabase page says where a new team starts.
- The Project Owner's root names the server first, as where a new team starts.
- No page calls the server experimental. Where one did, it says that the server is one instance with no standby
  yet and that a standby on a second node is planned.
- Supabase stays supported for the teams on it. Nothing is taken away: `team create` without `--adapter` still
  creates a team in a Supabase project, and its pages and skills still say how.
- The mail and Slack gateways of a Supabase project take fixes only. What is new for a gateway is built for the
  server's. Each of the two gateway pages says so.

## Alternatives considered

- **Two peers**, both first-class. Lost by the owner's ruling: it needs shared cases that both test suites read,
  the handlers run in a test, and every gateway change made twice.
- **Supabase first**, the server staying experimental until the standby exists (work item 312). Lost: it is not
  what the owner's own teams do.
- **Make the server the default of `team create`**, so that no `--adapter` means a server. Not taken: a command
  with a Supabase URL and key and no `--adapter` is what every existing page, script and habit runs, and changing
  what it means would break them for no gain. The default that changes is what a new administrator is shown
  first.
- **Remove the Supabase form from the skills.** Not taken: the teams on Supabase are supported, and an
  administrator of one reads the same skill.
- **Call the server "generally available" or the like.** Lost to the owner's sentence, which states the one
  limit plainly and names what is planned.

## Evidence

- None measured for this decision beyond the reads of the review. The owner's own teams have run on a server
  since 2026-11-13 (work item 272), and the check of a week of real use on one fell due on 2026-11-20 (work
  item 89).
- `scripts/ci/setup_docs_test.go`: the server form is a witness that the four setup skills, the Team Admin's
  topic root and its server page must carry word for word.
- Not shown: a new administrator creating a team on a server by following the skill in a live session.

## Consequences

- A new administrator who asks a session how to set Brigade up is shown the server form first, and needs a
  server's URL from their Project Owner before a Supabase project is mentioned.
- A change to a gateway is made once, in the server. A fault found in a Supabase gateway is still fixed there
  (work item 388 was one, work item 271 is another).
- Work item 103, which would move the Supabase functions off the Deno runtime, is read as a fix or not when its
  date nears: steps that only keep the functions' tooling current are not fixes.
- The server's one limit is on its page until the standby of work item 312 exists; that sentence changes then.
- Each package's `setup` skill changed with this, Codex CLI's among them, so Codex asks its members to review
  Brigade's hooks once more.
- Revisit when the standby exists, and if a team that cannot run a server needs a feature the Supabase gateways
  do not get.

## References

- Work item 376 on the Brigade Board, with the owner's two rulings of 2026-11-22 as comments; work items 89, 103,
  271, 312, 330, 342 and 388.
- `docs/team-admin/create/README.md`, `docs/team-admin/create/create-on-a-server.md`,
  `docs/project-owner/README.md`, `docs/project-owner/server/README.md`.
- `docs/project-owner/configure/mail-gateway.md` and `slack-gateway.md`, the two pages that say fixes only.
- The four `skills/setup/SKILL.md`; `scripts/ci/setup_docs_test.go`.
