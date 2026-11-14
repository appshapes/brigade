# 0003. Follow a team that moved, on the old backend's record and a move ticket

- **Status:** Accepted
- **Date:** 2026-11-13
- **Supersedes:** none

## Context

A team can move from a Supabase project to a Brigade server and keep its join secret (work item 273, part 1;
`docs/project-owner/server/take-in-a-team.md`). Until this decision every member then ran `/brigade:join <path>`
once on each machine, and again in each other checkout of the team (work item 290). The owner asked for a move
with no action by the members: "the whole point of an adapter is to be able to swap it without affecting the
clients" (2026-11-12, on work item 273). Three facts stood in the way, read from the code on 2026-11-13:

- A member's consent is a pin for each checkout that holds the adapter, the URL, the certificate and the team's
  reference. A team file that names another backend stops matching, and `docs/security.md` said "a change of team
  still needs the secret file".
- Only a command wrote that pin. The hooks could only read it, by the lint rule `hook-reads-the-store`.
- No machine keeps the join secret, and the server admits by the join secret's hash alone. What a member's machine
  still holds of the old team is its session on the old backend and its label.

A hostile URL in a commit, or in the branch of a pull request a member checks out, must be sent nothing. So the
commit cannot be what says where the team went. The one thing a member's machine already trusts is the backend
the member joined the team on.

The owner ruled on 2026-11-13, in the conversation of the session `89-server-4` and recorded on work item 315:
"315: agreed", on three questions put with a recommendation each. A session may follow a team on the word of its
administrator recorded on the old backend, with a line at session start in place of the member's confirmation and
an option that turns it off; the server admits the member by a ticket; and the migration this needs is applied to
a hosted project only at his word, project by project. The session that built it, `89-server-5`, made the
remaining choices under his standing rule that a session decides on its own recommendation.

## Decision

A member's session follows its team to another backend when, and only when, the backend the member joined the
team on holds its administrator's record of exactly that move.

1. **The ticket.** The server's operator opens a move for a team with `brigade-server --open-move <team_ref>
   --ticket-file <file>`, run while the server is stopped. The ticket is a second join secret of that team, in
   the join secret's format, so `/v1/team/join` carries it unchanged and REST contract version 1 keeps its
   shapes. The server keeps its hash and its end: 30 days unless `--ticket-days` says otherwise, from 1 to 365.
   A join the ticket admits never makes the team's owner. `--close-move` ends it, a second `--open-move`
   replaces it, and a rotation of the team's join secret or a take-back closes it.
2. **The record.** The team's administrator records the move on the old backend with `brigade team move
   --ticket-file <path>`: where the team went (adapter, URL, certificate) and the ticket. The destination is the
   backend the project's team file names and is never typed. The command answers the team's creator only,
   refuses inside a session, and refuses until the administrator's machine has joined the new backend, so the
   administrator joins there first, with the secret file, and owns the team. `--clear` removes the record and
   `--show` reads it; `--from` names the backend whose record is meant where the machine holds more than one
   membership of the team, the backend the team file names included.
3. **The protocol.** BAP/1 gets the capability `team.move` and two commands, `team move` and `team moved`
   (4.4.10, conformance cases C-51 and C-52). The ticket travels in a file both ways and is in no argument,
   result or error. The Supabase adapter and the fs adapter carry them; the `rest` adapter does not, because a
   record is kept by the backend a team leaves. On Supabase the record is one table closed to every role and
   three functions, in the migration `20261114120000_team_move.sql`; an adapter on a project without it reads
   "no move".
4. **The follow.** At session start, when the checkout's pin and the team file name the same team at different
   backends, the hook asks the old backend, with the session the member holds there, whether the team moved. It
   follows only when the record names what the file names: adapter, URL, certificate and team reference, each
   compared for equality. Then it joins the new backend with the ticket under the label the member had, writes
   the binding and the pin (which notes the backend it moved from), and attaches. A machine that already holds
   the new backend's membership writes the pin and joins nothing, and one that held it and left is not joined
   again. Only a session's start follows: the prompt hook's registration retry never does. An adapter that does
   not advertise `team.move` is asked nothing.
5. **What the member is told.** One line at session start, before any other, in place of a confirmation: the
   team, the new backend and the certificate trusted there, that the administrator recorded the move on the
   backend the member joined, how to leave, and how to turn the follow off. A session that was running when the
   file changed stays where it is and is told once, at its next prompt.
6. **The option.** `follow_team_move`, on unless the member sets it to `off`. Off, or a value that is neither
   word, never follows. A session that names its adapter itself (`adapter_command`) never follows either.
7. **Everything else is as before.** No record, a record that names anything else, a session on the old backend
   that has ended, a server that refuses the ticket: the drift line, which echoes nothing from the file, and
   `/brigade:join <path>`.
8. **The store's writer.** The follow lives in `internal/harness/teammove`, the one writer of the team store a
   hook reaches. The hook package still may not import `teamstore/write`: the rule now means that a hook never
   writes the store on a team file's word, only behind the comparison of point 4. A second lint rule lets no
   package but the commands and this one import the writer, so no third door opens unnoticed.
9. **Hosted projects.** The migration reaches a hosted Supabase project only at the owner's word, project by
   project.

## Alternatives considered

- **The member's Supabase session as the voucher**, which work item 273 named as the candidate: the session is
  sent to the server, which checks it. It lost on four counts. It needs the record anyway, or a hostile URL
  collects sessions. A valid session proves an account of the project and never a member of the team (measured,
  below), so the server would also have to ask the project about membership with the member's own token. It puts
  every member's live session of the old team on the server, the administrator's included. And it needs a new
  request in the frozen REST contract and a server that calls out to Supabase. What it would have gained is that
  ownership and bans could follow exactly.
- **The join secret itself in the record**, with no change to the server. The old database would hold the secret
  in the clear, where today it holds a bcrypt, and ending the move would mean rotating the secret.
- **One join for each machine, as before**: fix work items 289 and 290 and write the page. It is what the owner
  asked to be rid of.
- **A typed destination for `team move`** (`--url`, `--adapter`). It lost to reading the team file: a record
  that differs from the file by one character is followed by nobody, and the administrator would find out from
  members.
- **Recording before the administrator joins.** With the follow on, the administrator's own next session would
  come in by the ticket, and a ticket never makes an owner: the team would stay without one until someone joined
  with the secret. So the record is refused until the administrator's machine has joined.
- **Running the follow as a child command of the hook**, so that "every writer is a command" stayed literally
  true. It lost to one package that both the hook and its tests call: the argument is the comparison, not the
  process boundary, and a child would need the session's environment rebuilt for it.
- **Leaving a move open across a rotation.** A rotation is how an owner ends everything that admitted before it;
  a ticket that outlived it would be a way in that the owner believed closed.

## Evidence

- Local stack, Supabase CLI 2.116.0, 2026-11-13: a session is signed with ES256 and the public keys are served,
  so a server could check a signature. An anonymous sign-up with the publishable key alone, which every committed
  team file carries, returned a valid session with a life of one hour. No hosted project was asked.
- 2026-11-13, this machine, the built `brigade` and the built server: a member's session followed its team with
  no command run, from the fs adapter and from the local Supabase stack
  (`TestATeamsMembersFollowItToAServerThroughTheRealBinary` and
  `TestATeamsMembersFollowItFromSupabaseToAServerThroughTheRealBinary`, `internal/harness/e2e/rest_test.go`).
  In the same tests a session before the record, one whose team file names a server the record does not, and one
  after the record was cleared attached to nothing, and the member the ticket let in could not administer the
  team.
- The same day: the migration's proof `supabase/tests/team_move.sql`, 66 assertions; conformance cases C-51 and
  C-52 against the fs adapter and against the Supabase adapter on the local stack; the server's own tests of the
  ticket. Eleven one-line changes to the harness, the adapters and the commands were each caught by a test.
- The same day, a review of the whole change by a second agent found ten points. Nine are fixed in it: the follow
  ran from the prompt hook's retry as well, under a budget it could outlast; a profile left at the new backend
  under another certificate refused the join; a record could not be read or cleared once the team file pointed
  back; a member who had left was joined again; an adapter without the capability was asked all the same; and
  four smaller ones. The tenth, that the prompt hook reads the team file at every prompt, was measured instead:
  27 microseconds.
- Not measured: a hosted project, and a team with members on more than one machine. That proof waits on the
  owner's word for one project.

## Consequences

- A team's administrator, and whoever controls the old backend, can send every member's sessions to a server of
  their choosing, where its operator reads what they send. Before this it took each member's own command. That
  goes on after the old project is gone.
- Whoever can read the old database can read the ticket, and join the new team with it until it ends, as a member
  and never as its owner. Every active member of the old team can read it too; each of them holds the join secret
  already. A member revoked or banned on the old team reads no record.
- Waiting messages do not carry over, and the administrator moves the team at a quiet moment.
- A member whose session on the old backend has ended follows nothing and joins as before.
- The hook now transmits a secret and writes the store, in one place. A change to `internal/harness/teammove`
  is a change to what `docs/security.md` section 21 argues.
- The protocol has two more commands and the Supabase schema one more table and three more functions to keep
  compatible in both directions.
- We would revisit this if a team needed ownership or bans to follow exactly, which is what the first
  alternative offered, or if a second kind of backend wanted to be moved to.

## References

Work item 273 (the build) and work item 315 (the question, the options and the owner's ruling);
`.context/plans/brigade-server.md` section 4.9; `docs/security.md` section 21; `docs/protocol-v1.md` 4.4.10 and
4.7; `supabase/migrations/20261114120000_team_move.sql`; `internal/harness/teammove`; ADR 0001.
