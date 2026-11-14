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
already arrived there. The owner asked whether a running session can follow by itself.

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

An assessment of the same day recommended a follow in two steps at the session's next prompts. The design below
was handed to the build as decided; the choices the build made inside it are the building session's, under the
owner's standing rule that a session decides on its own recommendation.

## Decision

A session that is running follows its team at its next prompts, in two steps, on the same record a session's
start follows on.

1. **The follow, off the hook's budget.** At a prompt of a session whose team file names another backend for the
   team it joined, and under the gates a session start applies (the option `follow_team_move`, a session that
   does not name its adapter itself, an adapter that advertises `team.move`), the hook starts a detached child,
   `brigade hook follow`, and returns. The child does what a start's follow does and nothing else: it asks the
   old backend, compares the record with the file member by member, joins or reuses the membership, writes the
   binding and the pin. Its environment is built from scratch and carries neither the inbox socket nor the
   messaging token, and what it needs it takes as arguments, none of them a secret. Run inside a session it
   refuses.
2. **Once a minute.** At most one follow a minute is started for a checkout, whichever of its sessions is
   prompted, and the stamp that holds the bound is written before the child is started. A team file with no
   record behind it earns one question to the old backend a minute while its sessions are prompted, and nothing
   to the address it names.
3. **The re-attachment, in the hook.** A later prompt finds the checkout's pin matching the team file while the
   session's map still names the backend the team left. The hook registers the session anew on the new backend,
   under the name it has, with no resume hint and no heartbeat of the id it had; it writes the map and starts
   the watcher, which retires the old one, which closes the old session. A request for quiet made in the session
   is kept. The doing line is asked for again.
4. **What the member is told.** One line when the file changes, which echoes nothing from it. Then the line a
   session start says when it follows, at the first prompt that finds the checkout followed, whether or not the
   session can move at that prompt. A follow that had nobody to tell leaves a mark for the checkout before it
   writes the pin, so that a session which starts there after the asking one ended says the line instead.
5. **Held messages stay, so the session stays.** Messages that wait for the session on the backend it is leaving
   are not carried. While any is held, or released and not yet delivered, the session stays where it is, and
   says so once.
6. **A registration that fails leaves the map untouched.** The session goes on where it was, is told once, and
   the attempt is made again a minute later. A registration that was made and not recorded is left to its lease.
7. **The same for a session that is attached to nothing.** The prompt hook's registration retry, which ADR 0003
   kept from following, starts the same child, and registers at the first prompt after it.
8. **The member's own join moves the session too.** A pin that matches the team file is the member's consent
   however it was written: after `brigade team join` in the checkout, the running session moves at its next
   prompt, and says so in a line of its own.
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
- The hooks detach a second kind of child. `brigade hook follow` joins the watcher as a process a hook starts
  and does not wait for.
- A session's start, a prompt and a member's join can each write the same pin. The follow's lock and its check
  of the pin under it are what keep two of them from joining twice.
- We would revisit this if a host began to show what a prompt hook prints (Grok Build), or if a follow were
  wanted without a prompt, for a session nobody is typing in.

## References

Work item 327; ADR 0003, which this supersedes in part; `docs/security.md` section 21;
`internal/harness/hook/move.go`; `internal/harness/teammove`; `docs/team-member/join/follow-a-move.md`.
