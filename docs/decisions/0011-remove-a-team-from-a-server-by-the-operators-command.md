# 0011. Remove a team from a server by the operator's command

- **Status:** Accepted
- **Date:** 2026-11-15
- **Supersedes:** ADR 0010, in part: in its Decision point 4, that a server has no command that removes a team.
  The rest of ADR 0010 stands.

## Context

A team's reference is public: every committed team file carries it as `team_ref`. A Brigade server takes a team
in by request for whoever presents a join secret that carries the reference, and holds that caller to the
creation token and to nothing else ([ADR 0006](0006-take-a-team-in-and-open-its-move-by-request.md)). So on a
server that asks for no creation token, a stranger can take in a team under a reference that is not theirs, with
a join secret of their own invention. The team whose reference it is then finds it taken: its take-in is
answered `conflict` with the reason `other_join_secret`. ADR 0006 said so in its Consequences, and that no
command of the operator's removed such a team yet; `docs/security.md` section 20 said the same. A server that
asks for a creation token is not exposed.

Facts read from the code on 2026-11-15:

- The operator had two commands on a team that is there, and neither frees a reference. `--set-owner` names an
  active member, and the real team has no member on the stranger's team. `--take-back` gives the team a new join
  secret, and the real team must come in under the secret its members and its gateways hold.
- The server keeps the hash of a join secret and cannot ask the backend a team lives on who the team is: the two
  share no identity (`.context/plans/brigade-server.md`, section 4.8). Two secrets that carry one reference are,
  to the server, two secrets.
- A credential names a principal, and a principal may be a member of several teams on one server. A revocation
  ends a membership and never a credential (the plan's section 4.8; `docs/security.md`, section 20).
- A server's log refuses an entry of a kind it does not know, a snapshot names a format, and since
  [ADR 0010](0010-keep-the-record-of-a-teams-move-on-the-server-it-leaves.md) the format follows what the state
  holds. `State::apply` cannot fail and reads no clock.
- ADR 0010 said in its Decision point 4 that a server has no command that removes a team, and in its
  Consequences that a server gaining a way to end a team "would decide what becomes of the record" of where a
  team went.

Points 1 to 4 below were settled on work item 332 before it was built. Points 5 to 10 were left to the session
that built it, which decided them on 2026-11-15 on its own recommendation, under the owner's standing rule that
a session does; the owner may overrule them.

## Decision

The server's operator removes a team with one command on the server's data directory, and nothing else removes
one.

1. **The command** is `brigade-server --data <dir> --remove-team <team_ref>`, run between a stop and a start of
   the server, as `--take-back` and `--set-owner` are. No request removes a team: the REST contract and BAP/1
   do not change, and `/v1/info` lists what it listed.
2. **It removes the team with everything the server holds of it**: its name, the hash of its join secret, its
   owner, every membership it ever had, with the bans among them, every session with the messages it held, the
   ticket of its open move, the retry key it kept, and the record of where it went. That is what becomes of the
   record ADR 0010 left open: it goes with the team, and a member who has not followed by then follows
   nothing.
3. **The reference is free afterwards.** A take-in under it is answered as on a server that never held the
   team, for whoever may take a team in there. The state keeps no mark of the removal.
4. **It is one record of the log**, `team.removed`, which names the team and nothing else of it: what goes is
   what the state holds of the team when the record is applied. It is applied as every record is, so a restart
   and a replay reach the same state. What the log and the kept snapshots already hold of the team stays at rest
   until the server lets them go, as ADR 0010 says of a cleared ticket.
5. **The principals that were its members stay, with their credentials.** They are the server's and not the
   team's. A member of the removed team is answered, with the credential it holds, what any caller is answered
   about a team that does not exist: the byte-identical `unauthorized` for the team and for a session it had
   there, and the uniform `not_found` for such a session when its principal is still a member of another team.
   Its secret joins nothing, and leaving is the success it always is.
6. **No snapshot format, and server 0.9.0.** A state after a removal holds nothing of the team, so its snapshot
   names the format it would name anyway. A server 0.8.0 therefore starts on a data directory on which a team
   was removed whenever the newest snapshot was written after the removal, which the command's own end does,
   and then holds the state a 0.9.0 holds. Where it has to read the entry it refuses it, as a kind it does not
   know, and does not start.
7. **What the command refuses.** A reference the server does not hold, compared as it was given: `not_found`,
   status 1, and nothing changes. A server that runs on the directory: the directory's lock, status 1. Any
   other of the operator's commands and any option that serving takes beside it: the usage, status 2, before
   the directory is touched. It asks no question and takes no second argument. Two refusals more came of its
   verification, because the command cannot be undone: the option given twice is the usage and status 2, with
   neither team removed; and a `--data` that holds no record key is no server's directory, status 1, with
   nothing made in it.
8. **It says what it removed in one line**, on its own standard error: the team's name, its reference, how many
   members its roster showed, its owner's principal reference with the label the roster showed, and that a
   record of where it went was removed when one was. No secret and no hash of one is in it. It is the one
   account of the removal outside the log: the service's journal has none, because the command is not the
   service.
9. **Gateways that were members of the team go with it**, as the members they are. The command reads no
   gateway configuration and leaves what the gateways keep for themselves as it is, so what becomes of the
   configuration is the operator's, and it depends on whose entries the file holds. In the stranger's case
   the entries are the real team's: its gateways were never members of the stranger's team, the entries stay,
   the server says of each gateway in one line, by the reference, that its join is refused, and the gateways
   join by themselves once the real team is there. Where the gateways were members of the removed team and no
   team of that reference is coming back, the operator takes the entries out, and changes the request URLs at
   Slack and the webhook at the mail provider, which leaving an entry out of the file does not do; a server
   that is started with the entries left in starts, says of each gateway that it is no member and that its
   join is refused, and serves everything else.
10. **It does not close the way the reference was taken.** On a server that still asks for no creation token,
    the reference is free for the stranger too. The operator's page says what closes it before the server
    serves again: a creation token, or the operator's own `--import-team` of the real team.

## Alternatives considered

- **A take-in that proves more than the secret**, such as a voucher from the backend the team leaves. It lost
  because there is nothing for the server to check it against. The reference is public, a join secret is the
  one thing a server can hold a caller to, and the server cannot ask the old backend who the team is: it would
  have to call a backend the caller names, which a stranger can run as well as anyone, or hold credentials of a
  Supabase project. And it would not remove a team that is already there.
- **Letting the real team's take-in take the place of the team that holds the reference.** The server cannot
  know which of two secrets is the real team's. A take-in that displaces a team is the same attack turned
  round: a stranger displaces the real team.
- **A take-back to a secret the operator names**, which would hand the stranger's team to the real one. The
  real team would inherit what the stranger's team held under its reference: its name, its sessions and their
  messages, its bans, its move and the record of where it went. The real team is to come in as on a server that
  never held it.
- **A request by which a team's owner ends its team.** It would be worth having by itself, and ADR 0010 names
  the lack of it. It does not serve this case, where the owner is the stranger, and it is a change to the
  contract. It is not built here.
- **A creation token on every server.** It closes the exposure where it starts, and it is against the owner's
  ruling that a server is open by design and closed by choice (`docs/security.md`, section 20). A server
  without a token stays possible, and can now be mended.
- **Making the members' credentials unknown with the team.** A credential names a principal that may be a
  member of other teams, which the removal must not touch. Sparing the principals that have another membership
  would have `apply` read every team, and would answer two members of one removed team differently. And an
  orphaned principal holds nothing: on a server with no token, one request makes anyone a principal.
- **Barring the reference after the removal** until the operator frees it. It would keep the stranger from
  taking the reference again, and the real team as well, unless the command also took the real team's secret.
  It is state that a snapshot would have to carry in a format of its own, for something the operator has two
  commands for already.
- **A snapshot format of its own for a state after a removal**, so that no server 0.8.0 starts on a directory
  on which the command ran. The state holds nothing to mark, so it would take the bar above. And nothing is
  lost where a 0.8.0 starts from a snapshot written after the removal: it holds the same state.
- **A second argument or a question that confirms the team**, because the command cannot be undone. It lost to
  the form the other commands have and to what already stands in the way of a mistake: the reference is copied
  from a team file, one the server does not hold is refused, the line says what was removed, and the page tells
  the operator to copy the data directory first where there is doubt.

## Evidence

All on 2026-11-15, on this machine (macOS, Apple silicon), in the worktree of work item 332.

- The rules (`server/crates/brigade-core/tests/rules.rs`):
  `the_operator_removes_a_team_with_everything_it_holds_and_frees_its_reference` holds that the state after the
  removal is the state before without the team and its sessions, that a second team is the bytes it was, that
  nothing the state holds names anything the team held, what its members are answered, that the real take-in
  is answered as on a server that never held the team, and that a replay and a snapshot with the log after it
  make the same state; `a_removed_team_keeps_no_retry_key_and_leaves_no_way_back_to_one` holds the retry key.
  The walk of twenty thousand requests removes teams among them: 422 removals, 29 of them of a team that kept
  a retry key.
- The server as a process (`server/crates/brigade-server/tests/server.rs`), on the file log and on the Cavalry
  feed: a stranger takes a reference in, the real take-in is `conflict`, the command is refused while the
  server runs, for a reference it does not hold and beside every other command, it removes the team and says
  the line the page gives, the stranger's credential is answered `unauthorized`, a second team is untouched, a
  start from the command's snapshot and a start that reads the entry from the log reach the same state, and
  the real take-in then owns the team. A second test holds what keeps a freed reference for its team: the
  operator's `--import-team` before the start, and a creation token.
- A team with gateways (`server/crates/brigade-server/tests/gateway.rs`). Where they were its members: the
  server that is started with the configuration as it was says the two lines for each gateway and serves the
  rest; a Slack request is answered 500 and a mail 502 where the removed team was the gateway's only one; and
  the gateways join a team of the same reference again with the credentials they kept. In the stranger's case,
  where the configuration names the real team: the gateways are no members of the stranger's team, say one
  line each by the reference before the removal and after it, and join at the start once the operator took
  the real team in. That a gateway joins within a minute of a take-in on a server that runs is held with the
  turns in the test's hands (`turns.rs`), and took 55 seconds through the built server by hand.
- A removed team whose gateways serve another team as well, through the built server by hand: the other team
  was served, a mail to its inbox was answered 200 and one to the removed team's 502, and a person on Slack was
  told that one team's gateway is not connected at the moment, so its sessions cannot be reached.
- The two refusals that came of the verification, through the built server on both logs: the option given
  twice ended with status 2 and removed neither team, with nothing in the directory touched; a path that was
  not there was not made, and a directory that held something else was as it was.
- A server 0.8.0, built from the commit this work started from, whose `server/` is the tree of the tag
  `server-v0.8.0`, run by hand on data directories this build wrote, on both logs. On a directory as the
  command left it, it started, held no team of the reference and took one in. With the snapshot the command
  wrote taken away, it wrote `the log could not be read from the snapshot's position, 730: not an entry:
  unknown variant `team.removed`, expected one of …` (768 on the feed) and ended with status 1; this build
  then started on that directory, read the one entry, wrote the snapshot again and held no team of the
  reference; stopped, it left a directory on which the 0.8.0 started.
- Each test was run against the one change it exists to catch, and failed: a removal that leaves the team's
  sessions named, one that leaves the retry key's way back, a record under another name, a command that is not
  counted among the commands that end, an entry that is not handed to the log, credentials made unknown with
  the team, a line that names an owner where there was none, a reference that is trimmed in the rules, in the
  command or on the command line, the option read twice with the last one standing, and a directory that is
  opened before its record key is looked for.
- Not measured: what a member's own `brigade` session prints once its team is removed; what the Team Admin's
  `brigade team move --to` prints when it is run again after a team was removed from a server it left, which
  is read from the command's code; and a removal on Linux.

## Consequences

- An operator can free a reference that was taken in wrongly, and the real team then comes in as on a server
  that never held it. A server with no creation token is mendable by this and no safer: until it has a token,
  or holds the real team, the stranger can take the reference again.
- Whoever runs a server can end any team on it with one command, and nothing brings the team back. They could
  already read everything in it and take any team from its owner; `docs/security.md` section 20 states the
  addition.
- A team that moved away can be removed from the server it left. The record goes with it, so the operator
  removes it once every member has followed. The Team Admin's machine keeps its membership of the team there,
  and is refused from then on as any former member is: a later move that is run again after it stopped then
  needs the backend it leaves named with `--from`, which its refusal prints (read from
  `internal/harness/commands/teammoveto.go`, not run).
- The log has a kind of record that a server before 0.9.0 does not read. Going back to 0.8.0 works where the
  newest snapshot was written after the removal, and stops with the record's name where it was not.
- What a removed team held stays in the data directory, in the snapshots that are still kept and in the log's
  earlier entries, and in every copy made before. The command frees a reference; it erases nothing.
- Principals that are members of nothing stay on the server, as they do when a member leaves.
- A team's owner still cannot end its team, and no request removes one. We would revisit this if owners need to
  end a team by request, if a removal has to erase what the log holds, or if a freed reference has to stay
  barred until the operator says otherwise.

## References

Work item 332; `docs/project-owner/server/name-the-owner.md` ("Remove a team") and
`docs/project-owner/server/gateways.md` ("After a team is removed"); `docs/security.md` sections 20 and 21;
`docs/developer/change/server.md` ("Removing a team"); `server/CHANGELOG.md` section 0.9.0;
`.context/plans/brigade-server.md` section 4.8; ADRs 0006 and 0010.
