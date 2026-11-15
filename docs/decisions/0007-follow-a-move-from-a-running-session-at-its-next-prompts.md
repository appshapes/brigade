# 0007. Follow a move from a running session at its next prompts

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** ADR 0003, in part: its Decision point 4's sentence "Only a session's start follows: the prompt
  hook's registration retry never does", and its point 5's sentence that a session that was running stays where
  it is. The rest of ADR 0003 stands, the anchor first: a team file is believed only when the backend the member
  joined the team on holds the administrator's record of exactly that move.

## Context

Since ADR 0003 a member's session follows a team that moved, when it starts. A session that was already running
stayed on the backend the team left until it ended, and was told once to start a new one. Work item 327 records
what that cost in a real move on 2026-11-14: for hours the new server had no member session while mail and Slack
already arrived there. The owner asked whether "sessions already open ... can automatically reappear on the
roster. This is just data, so technically it seems possible."

Read from the code that day:

- A prompt hook has 4.5 seconds under a timeout of 5. A follow asks the old backend, may join the new one and may
  wait up to 10 seconds for a lock, and `teammove.Follow` puts no deadline on its calls. That is why ADR 0003
  kept the follow out of the prompt hook.
- A session's map names its team key, its session and its adapter. Its watcher takes the team key from its own
  environment, stops when the map names another session or key, and closes its session when it stops. A watcher
  that starts beside a live one for another session retires it.
- `/clear` fires the session start again in the same process, so a running session already followed there, on a
  path no test covered. After the follow the hook heartbeated the old session's id on the new backend, and any
  answer but `not_found` left a map naming the new backend and the old session.
- Grok Build discards what a prompt hook prints. A Muse Code or Codex CLI hook finds its session through its
  parent process.

The design below is the recommendation that the session `89-server-7-roadmap-and-week-check` recorded on work
item 327 on 2026-11-14, after an agent's assessment of the code read above: a follow in two steps at the
session's next prompts. It stands under the owner's standing word that a session decides on its own
recommendation, and so do the choices the build made inside it, which are the building session's.

## Decision

A session that is running follows its team at its next prompts, in two steps, on the same record a session's
start follows on.

1. **The follow, off the hook's budget.** At a prompt of a session whose own checkout (point 8) has a team file
   that names another backend for the team it joined, and under the two gates the member's options set (the
   option `follow_team_move`, and a session that does not name its adapter itself), the hook starts a detached
   child, `brigade hook follow`, and returns. The child does what a start's follow does and nothing else: it
   asks nothing of an old backend whose adapter does not advertise `team.move`, and otherwise asks it, compares
   the record with the file member by member, joins or reuses the membership, writes the binding and the pin.
   Its environment is built from scratch and carries neither the inbox socket nor the messaging token, and what
   it needs it takes as arguments, none of them a secret. Run inside a session it refuses.
2. **Once a minute.** At most one follow a minute is started for a checkout, whichever of its sessions is
   prompted, and the stamp that holds the bound is written before the child is started. A team file with no
   record behind it earns one question to the old backend a minute while its sessions are prompted, and nothing
   to the address it names.
3. **The re-attachment, in the hook.** A later prompt finds the checkout's pin matching the team file while the
   session's map still names the backend the team left. The hook registers the session anew on the new backend,
   under the name it has, with no resume hint and no heartbeat of the id it had; it writes the map and starts
   the watcher, which retires the old one, which closes the old session. A request for quiet made in the session
   is kept. The doing line is asked for again. The registration ends a second short of the hook's budget: Claude
   Code discards what a prompt hook printed when the hook reaches its timeout, so a registration answered later
   than that is given up as one that failed. A second is not always enough for what follows a registration, so
   the hook leaves a notice before it registers the session and removes it as its last act, and it does not
   wait for the new watcher's pidfile. A prompt that finds a notice over a map that names another session says
   the move itself: a session that moves is always told, by the hook that moved it or by its next prompt.
4. **What the member is told.** One line when the file changes, which echoes nothing from it. Then the line a
   session start says when it follows, at the first prompt that finds the checkout followed, whether or not the
   session can move at that prompt. A follow that had nobody to tell leaves a mark for the checkout before it
   writes the pin, so that a session which starts there after the asking one ended says the line instead.
5. **Held messages stay, so the session stays.** Messages that wait for the session on the backend it is leaving
   are not carried. While any is held, or released and not yet delivered, the session stays where it is, and
   says so once in a conversation. A session's start in the same process, which `/clear` fires, keeps it there
   as a prompt does: it registers nothing, keeps the map and the watcher for the conversation that begins, and
   says the same lines before the context line of the session where it is.
6. **A registration that fails leaves the map untouched.** The session goes on where it was, is told once, and
   the attempt is made again a minute later. That holds at a prompt and at a start of the same process alike: a
   running session that could not be registered on the new backend is told so, and not that it is
   `not connected`. A registration that was made and not recorded is left to its lease.
7. **The same for a session that is attached to nothing.** The prompt hook's registration retry, which ADR 0003
   kept from following, starts the same child, says once that the backend the member joined is being asked, and
   registers at the first prompt after the child followed.
8. **A session moves with the checkout it registered in, and with no other, wherever it works.** The session's
   map names the checkout its registration was resolved from, at a start and at the prompt hook's retry alike.
   At every prompt both steps read the team file and the pin of that checkout, and nothing about a move is read
   from the directory the prompt names, which follows the model (a `cd`, a worktree, another repository).
   Reading that directory was wrong both ways. While a move is under way a member can have two checkouts of a
   team on two backends, one with the changed file and one without, and a session that moved on whichever its
   prompt came from went back and forth between them, a new session each time. And a session that looked at its
   own checkout only when a prompt came from it never followed while it worked anywhere else, which is where a
   session that was started in a main checkout and works in a worktree spends its life. So a session follows
   when its own checkout does, from wherever its prompts come; the registration of the move is resolved from
   that checkout; and no other checkout's file or pin moves it. A start of the same process (`/clear`) over a
   session that is running, which its live watcher shows, is that session's wherever its document's directory
   is. From another checkout it neither moves the session nor makes it that checkout's: it follows for that
   checkout, as a start in it always has. From a directory that resolves no team (a checkout that is not
   joined, no team file, a team file with no record behind it) it says nothing of a session that is not
   connected. Either way it keeps the map and the watcher for the conversation that begins and says the
   context line of the session where it is. With no watcher alive, a start in another checkout is of a session
   that begins there: it registers where it is and is that checkout's, which is what a Codex CLI thread that
   is resumed in another checkout is. In its own checkout a pin that matches the team file is the member's
   consent however it was written: after `brigade team join` there, the running session moves at its next
   prompt, and says so in a line of its own. A map written before it named a checkout is given the first one a
   prompt or a start comes from that is joined on this machine to the session's team, and is that checkout's
   from then on.
9. **A resume hint goes only to the backend that issued it**, at a prompt and at a session's start: the map that
   remembers a session by its native id now carries the team key, and a map that names another key is neither
   resumed nor heartbeated. This is the fix of the `/clear` path.
10. **Grok Build is left as it is.** A session there follows when it starts.

## Alternatives considered

- **Leave it as ADR 0003 has it.** It is what the move of 2026-11-14 showed to be too slow: a team's sessions
  are long-lived, and nobody restarts them on the day.
- **Follow inside the prompt hook.** The follow can outlast the hook's timeout, and a hook that is killed
  between the join and the pin leaves a half-state at every prompt. ADR 0003's review had already found the
  prompt's retry following under a budget it could outlast.
- **Have the watcher follow.** It runs for as long as the session does and has no budget to fear. It lost
  because the watcher holds the messaging token and the inbox socket: the one process that can inject into the
  session would also be the one that sends a ticket to a new backend and writes the store. The child holds
  neither.
- **Move the session in the child as well**, so that one prompt suffices. The child cannot say a line, and a
  session whose id changed with nothing said would go on answering under an id its teammates no longer reach.
  The line and the registration belong to a hook of the session.
- **Carry held messages over.** A message is addressed to a session of one backend, and the ticket admits a
  member, not a mailbox. Waiting is the choice that loses nothing.
- **Ask the old backend at every prompt.** One follow a minute for a checkout is enough for a move, and a file
  that names a backend nobody recorded would otherwise turn every prompt into a request.
- **A bound for each session instead of each checkout.** Ten sessions in one checkout would ask ten times a
  minute about one pin.
- **Decide a session's move on the checkout its prompt comes from.** It is what was built first, and the
  verification of 2026-11-14 showed a session going back to the backend its team left when its prompt came from
  a checkout that had not followed yet, and forth again from one that had.
- **Look at a session's own checkout only when a prompt comes from it.** It is what was built second. It ended
  the back and forth, and a session whose working directory had left its checkout never followed: the second
  verification showed one staying on the old backend, silent, its checkout followed. The session that led this
  work was such a session, started in the main checkout and working in a worktree.
- **Wait for the new watcher's pidfile, and trust the registration's second.** The wait was up to half a second
  of the time a move has to be said in and nothing read its result, and what follows a registration was
  measured longer than the time left to it. A notice costs one small file.

## Evidence

- 2026-11-14, this machine (macOS, arm64), the built `brigade`, the fs adapter as the backend the team leaves and
  the built server 0.6.0 as the one it goes to
  (`TestRunningSessionsFollowTheirTeamToAServerThroughTheRealBinary`, `internal/harness/e2e/rest_move_test.go`):
  a session attached on the old backend followed in two prompts and was listed on the server's roster under its
  name; a new watcher served it, the old one was gone and had closed the old session, and a message sent on the
  server reached it. A team file naming an address the test listened on, with no record, earned one `team moved`
  to the old backend and no connection to that address, and two more prompts within the minute asked nothing. A
  quiet session stayed quiet. A session holding one message stayed until the message was released. `/clear` in a
  second checkout ended with a map naming the server and a session the server lists. The administrator's own
  session moved on her own join.
- The same run, three times: the prompt that started the follow took 30 to 55 ms as a whole process, and the
  prompt that moved the session 138 to 382 ms, over loopback.
- Each half-state has a test in `internal/harness/hook/move_prompt_test.go`: a child stopped before the pin, a
  session that ended between the steps, a registration made and not recorded, a new backend that cannot be
  reached.
- The same day, a second session verified the work against backends that never answer
  (`internal/harness/hook/move_budget_test.go`). With the old backend accepting the follow's question and
  holding it, the prompt that started the child, the prompt after it and the registration retry of a session
  attached to nothing each returned in a small part of the hook's budget, the child still waiting; so did both
  steps with the follow's lock held by another process. With the new backend holding the registration, the
  prompt ended at the registration's deadline, the map and the watcher as they were. Its other tests
  (`move_races_test.go`) are of two sessions of one checkout, a session start beside a child that joined,
  `/clear` between the two steps, and the stamp that holds the once-a-minute bound.
- That verification made 62 one-line changes of its own to the same rules. Sixteen passed the tests as they
  were, among them the bound made a second, a stamp from the future bounding for ever, a child started without
  its stamp, and one held message not keeping a session. The tests it added catch fifteen; the one left was the
  last line of a join made inside a Grok Build session, which has its test since.
- What it found and this decision now says, each with a test that failed first: a session going back and forth
  between two checkouts of one team, at its prompts and at a `/clear` (`move_checkout_test.go`; point 8);
  `/clear` moving a session away from
  the messages held for it, and saying `not connected` of a session that was attached (`move_start_test.go`;
  points 5 and 6); a registration answered in the last second of the budget (`move_budget_test.go`; point 3);
  the retry of an unattached session saying nothing of the follow it started (point 7). And two follows of one
  team in two checkouts at the same instant each joined, because the membership was read before the lock:
  `teammove.Follow` now reads it again under the lock (`TestTwoFollowsOfOneTeamAtOnceJoinOnce`). Twenty-four
  one-line changes to these rules were each caught by a test, two of them also through the built binary and
  the built server.
- The same day a third session verified those fixes. Of 53 one-line changes of its own to their rules, thirteen
  passed the tests as they were: a start taking a map with no checkout for another checkout's, a dead process's
  map keeping a start from registering and giving the session its checkout, a kept start refreshing nothing
  but the conversation's id, an adapter that could not describe saying `not connected`, a retry announcing a
  follow that was not started, a pin that moved from another backend earning the follow's line, a machine that
  left while a follow waited being joined again, and a map with a relative checkout. Three of them had been
  hidden: two tests of the registration's deadline timed the whole prompt and failed on a busy machine
  whatever was changed, and now read the deadline where the backend is asked. The tests it added catch twelve;
  the one left is a registration that was made and whose map could not be written. It found, and fixed with a
  test that failed first, a start that kept a session leaving the doing line of the conversation that ended on
  the roster, and a follow that could not be started leaving its stamp, so that the next prompt said the old
  backend was being asked. Through the built binary and the built server
  (`TestAMoveAcrossAMembersCheckoutsThroughTheRealBinary`, `internal/harness/e2e/rest_checkouts_test.go`):
  three checkouts that followed in the same instant, beside a session that started in a fourth, joined once
  and left the team two members, where the follow without its second reading of the membership joined four
  times; a follow killed while it held the lock left it free; `/clear` from another checkout moved neither a
  session on the server nor one on the old backend; `/clear` with the server stopped kept the session and said
  that it could not be registered. It found five things more, which it did not change: a session that works
  outside its checkout never following, a Codex CLI thread resumed elsewhere staying where it had been, the
  starts that resolve no team, the second the registration leaves, and a member's join beside a follow.
- Those five were ruled on and built the same day, by the same session: points 3 and 8 as they stand, and the
  lock a member's join now takes. Each has a test that failed first. Through the built binary and the built
  server (`rest_checkouts_test.go`): a session whose checkout had followed moved at a prompt sent from a
  checkout still on the old backend; a `/clear` in a directory with no team file kept its session and said its
  context line; and a `brigade hook prompt` that was killed once the map named the new session, held there by
  a watcher log that was a pipe nobody read, was made good by the next prompt, which said the move, a later
  one starting the watcher that serves the session. Without the wait for a pidfile, and with the registration
  answered a quarter of a second before its deadline and no pidfile written, the prompt ended 0.11 to 0.38 s
  after the answer in four runs, in process. Of 44 one-line changes to the rules built that day, seven passed
  the tests as they were, each a rule of the notice or of a start that keeps a session: the notice going
  before its hook ended, the arrival line said whatever the conversation had heard, a notice over a map of the
  backend the move was from, a session's end and a kept start leaving the notice, and a watcher of another
  session taken for the session's own. The tests added since catch each. Four of the 44 were also made to the
  built binary, and the test above failed on each. The second of point 3 is held as the difference of two
  deadlines of one hook, the registration's and the poll's that follows it, and no longer against the clock
  of a machine that other agents were loading: 999 ms fails it.
- The same day, 35 one-line changes to the rules above, in the hook, the follow and the join's last line, were
  each caught by a test. One of them, the map of another backend treated as a session that continues, was also
  run through the built binary: the test failed, and the hook's log showed the server answering `not_found` to
  the heartbeat and to the resume hint of an id it never issued.
- What the Supabase adapter answers for a session it does not know, read from
  `internal/adapters/supabase/session.go` and `supabase/migrations/20260923022323_session_sync_peer.sql`, not
  run: `not_found`, for a heartbeat and for a resume hint alike. An id that is not a UUID is answered so by the
  adapter before any request. A UUID that names no session of the caller is answered so by the function
  (`PT404`): `session_heartbeat` looks for the caller's session before it checks the membership, and
  `register_session` checks the membership first, so a caller who is no longer an active member is answered
  `unauthorized` there. The rule above leans on neither answer.
- Not run: Windows. The child is started with the attributes the watcher is started with
  (`procutil.DetachAttr`: its own process group and a hidden console), and `make lint` passes for Windows.
- Not measured: a hosted project, and a team with many running sessions.

## Consequences

- A team's administrator, and whoever controls the old backend, can now move a session that is running, not
  only the next one. The member is told in the session, and `follow_team_move` turns it off as before.
- A team file that changed with no record behind it costs the old backend one request a minute for each
  checkout whose sessions are prompted, for as long as the file stays.
- A session's id changes under it when it moves. The context line says the new one; a reply that a teammate
  sends to the id it had does not reach it.
- A session with held messages stays behind until its member releases them, and a team may be on two backends
  for that long.
- The prompt that registers a session on the new backend waits for that backend, as the registration retry of
  a session attached to nothing always has: while it does not answer, one prompt a minute lasts until the
  registration's deadline, a second short of the hook's budget.
- What follows a registration is the map, the resume hint, the notice and the stamps, each a file synced to
  disk. Beside other work on the machine that took from a tenth of a second to 2.8 s (measured 2026-11-14, in
  process), against the 1.5 s between the registration's deadline and the hook's timeout: a hook can be stopped
  after it moved the session. The notice is what then tells the session, at its next prompt. Until that prompt
  the session has an id it was not told; and the watcher of the session it had runs for up to two seconds more
  before it sees the map and ends, so the watcher of the new one is started by a prompt after that.
- A move whose notice cannot be written is not made: the session stays where it is, and each prompt tries
  again.
- A session follows its own checkout's team file and no other. A member who works from another checkout of the
  team, with the session's own left behind, has a session that stays where its own checkout is. A session whose
  map predates the checkout it names (its plugin was updated under it) takes the first checkout a prompt comes
  from, which may not be the one it started in; it moves with that one, once at most.
- A session reads its own checkout's team file at every prompt, from wherever it works. A checkout that is
  gone (a worktree that was removed) is read as no file: the session stays where it is, and says nothing.
- A start from another checkout, or one that resolves no team, is a running session's only when that
  session's watcher is alive. A Codex CLI thread is keyed by its thread and not by its process, so its map
  outlives the process: a thread that is resumed in another checkout registers there and is that checkout's,
  as any start in another checkout over a map with no live watcher does, the one that names the same
  conversation included. Messages that waited for the session of the process before are left where they are.
- The once-a-minute bound has two edges. The stamp is read and then written, with nothing to keep out another
  hook, so prompts of several sessions of a checkout in the same instant each start a child; the children
  cannot both join, because the follow's lock lets one through at a time and the one that waited reads the pin
  and the membership again under it. And the stamp lives in Brigade's state directory, which a Claude Code
  session and a Muse Code or Codex CLI session can resolve differently: a checkout with sessions of both has a
  bound for each.
- A start keeps a session where it is only when its map is that process's beyond doubt: its watcher is alive,
  or it names the conversation the start names. A session with no watcher (a host with no inbox socket) that
  holds messages is still registered anew by `/clear`, which begins another conversation.
- A `/clear` of a running session that resolves no team where it comes from no longer ends with the line of a
  session that is not connected, or with none: the session is kept and says its context line. What the
  resolution said is dropped, but for a team file that was refused (one that does not match what the member
  joined, one that holds what looks like a secret), which the start says in the words of a session that stays
  where it is. A session with no live watcher, and one whose member left its team, end such a start as they
  did: `not connected: .brigade.json does not match`, or `not joined`.
- A start that keeps a session tells the backend the session is on of the conversation that begins, as a start
  that continues does: one heartbeat with the name the session has now and, when the conversation changed, an
  empty doing line. Nothing is sent to the backend the team went to.
- The hooks detach a second kind of child. `brigade hook follow` joins the watcher as a process a hook starts
  and does not wait for.
- A session's start, a prompt and a member's join can each write the same pin. The follow's lock and its check
  of the pin and the membership under it are what keep two of them from joining twice: a member's own first
  join takes the same lock once the person has answered, and finds under it a membership that a follow made
  meanwhile. A join that waits ten seconds for a follow whose backend hangs fails with the lock's error, and
  is run again.
- We would revisit this if a host began to show what a prompt hook prints (Grok Build), or if a follow were
  wanted without a prompt, for a session nobody is typing in.

## References

Work item 327; ADR 0003, which this supersedes in part; `docs/security.md` section 21;
`internal/harness/hook/move.go`; `internal/harness/teammove`; `docs/team-member/join/follow-a-move.md`.
