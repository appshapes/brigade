# Swarm plan: one plan, several team sessions (requirements)

**Status.** Closed 2026-11-10 with the outcome in §9: the function is not built as drafted. Was: requirements draft, 2026-11-04, Trello card **202** (research spike). "Swarm plan" is a working name;
Frank's first name for it was "swarm topic" (open question 1). The requirements in §4 are Frank's decisions of
2026-11-04, except where an item says "my call". Nothing here is an owner ruling yet. Ground truth was read at
`origin/master` **cfecc49**.

## 1. What this is

A swarm plan lets one Brigade session publish a plan of work, and lets other sessions in the same team do parts of
it. Brigade keeps one record of the parts, the session that holds each part, and the results.

Today a team does this by hand, and makes new files for each plan. On 2026-11-04, one team ran a browser test of a
finished web application with sessions in three harnesses (Codex CLI, Claude Code, Grok Build). A coordinator
session wrote these files in the team's synced folder: a test plan, 12 test charters (parts), two leases (sole use
of a test account or a browser), reports, independent rechecks, a finding-handoff format and a status file. The
coordinator assigned the work and updated the status by hand. After the first batch of parts finished, work
stopped for about 40 minutes, until the coordinator assigned the next batch.

**Terms.**

- **Harness**: the coding tool that a session runs in. Brigade supports Claude Code, Muse Code, Codex CLI and Grok
  Build (`docs/harnesses.json`).
- **Synced folder**: a folder in the checkout that Brigade copies between the teammates' machines with Syncthing
  (`.brigade.json` `sync.folders`; [folder-sync.md](folder-sync.md)).
- **Coordinator**: the session that publishes a swarm plan. It is the only writer of the plan state.
- **Participant**: a session that accepted the coordinator's invitation. Brigade uses "member" for a person in the
  team, so this document does not use "member" for a session.
- **Part**: one unit of work in the plan. The example calls a part a "charter".
- **Resource**: a thing that only one participant can use at a time, for example a test account, a port or a
  browser. The example calls the hold a "lease".
- **Claim**: a participant's request to hold a part or a resource.

## 2. Goals, non-goals, anti-goals

**Goals.**

1. One plan format that a session of each harness can read and follow. Frank put this goal first.
2. The format supports four kinds of work: test plans, code split by stream, a role pipeline (implementer, then
   reviewer, then tester), and research.
3. Work does not stop between parts: a participant takes the next open part when it finishes one.
4. Only one participant holds a part or a resource at a time.
5. A person sees the progress without a question to the coordinator.

**Non-goals** (my calls).

- Brigade does not do the work, and it does not judge the results.
- A swarm plan does not replace the official tracker (Jira, Trello). Its results do not change an official status.
- A plan stays in one team. It does not span two Brigade teams.

**Anti-goals** (my guesses: each one is a failure, even if the function works).

- A plan makes a session act against the rules or the approvals of its own person.
- The coordinator's model spends most of its turns on bookkeeping that a command can do.
- Participants send so many messages that people stop reading them.

## 3. What Brigade has now (`origin/master` cfecc49)

- Messages between sessions, at most 16 KiB each. Each message arrives as untrusted text
  ([security.md §3](../../docs/security.md#3-a-message-is-untrusted-text)).
- A roster of sessions with the states online, idle and offline. A session that died can show online for up to
  three heartbeats, at most 10 minutes.
- The `doing` line: one sentence about the work of a session ([session-doing.md](session-doing.md)).
- Synced folders. In a synced folder, the state folder of the manager digest ([manager-digest.md](manager-digest.md))
  holds `brigade note` entries (Done, Next, Blocked) and `brigade decision` entries (work that waits on a person).
  `brigade report` sends a digest on a period through Slack or email, and sends a decision to its person at once.
- No record that only one session can claim.

## 4. Requirements (Frank's decisions, 2026-11-04)

### 4.1 The plan format

The plan has ten sections.

| Section | What it holds |
| --- | --- |
| Header | The goal; what "done" means for the full plan; the sources of truth and their order of precedence; the coordinator session; the plan folder. |
| Roles | A name and a duty for each role, for example coordinator, implementer, tester, reviewer, monitor. |
| Parts | For each part: ID, title, purpose, scope, limits (what the participant must not do), the tools it needs, role, the parts that must finish first, approver, output path, status, holder. |
| Resources | For each resource: ID, description, limits, holder, and the cleanup state at each handoff. |
| Handoff rules | Where each type of result goes, and the fields that it must carry. |
| Status | Calculated from the parts and the resources. Nobody edits it by hand. |
| Requirements coverage | The requirements that the plan covers (the example had 117), and the parts that cover each one. |
| Result and evidence rules | The allowed results (for example PASS, FAIL, BLOCKED, NOT RUN) and what counts as evidence for each. |
| Open questions for humans | The decisions and inputs that only a person can give, the person who must give each one, and the parts that wait for it. |
| History log | Each invitation, claim, handoff, approval and plan change, with the time and the session. |

A session of each of the four harnesses must be able to read the format.

### 4.2 Joining a plan

- The coordinator publishes the plan and invites named sessions.
- The person at each invited session approves or declines the invitation. A session cannot accept for its person.
- A session of any harness can be a participant.
- A session with no person present (bypass mode, auto mode, `claude -p`, an unattended Codex worker) can be a
  participant. A person approves its invitation one time. After that, the session works with no person present.
- Participants can work in different repositories, for example a web repository and an API repository (see §4.4).
- A participant is one session, not a place in the plan (my call). When a session ends, its part goes through
  §4.5. A new session that replaces it needs a new invitation and a new approval from its person.

### 4.3 Getting parts

- A participant takes an open part that matches its role and its tools.
- The coordinator can also assign a part to a participant, or move a part to a different participant.
- A participant holds one part at a time.
- Before a participant takes a part that needs a tool (for example a real browser), it shows that it has the tool.
- When a participant finishes a part, it takes the next open part immediately.

### 4.4 Storage and claims

- The plan files live in a synced folder of the coordinator's repository.
- A synced folder syncs only between sessions of the same repository, and one message holds at most 16 KiB. Thus a
  participant in a different repository needs a second carrier for the plan (open question 10).
- Only the coordinator's Brigade writes the plan state: parts, resources, status and history. A participant writes
  only its own outputs, at the output path of its part.
- A participant claims a part or a resource through the coordinator. The first claim wins. Frank chose a Brigade
  message as the carrier of a claim. The spike also compares a claim file in the synced folder (my call; open
  question 3).
- No breaking change to protocol v1. The design must work with each message adapter that a team can use.
- Limit: each teammate's machine can write any file in a synced folder (security.md §12). "Only the coordinator
  writes" is a rule that sessions obey. It is not a lock.

### 4.5 When a participant stops

- When a participant that holds a part or a resource goes offline, Brigade tells the coordinator immediately.
- The coordinator decides: wait, move the part, or release it. A resource can need cleanup before the next holder
  gets it.

### 4.6 Approval and merge

- Each part names its approver: the coordinator, a participant in the reviewer role, or a person.
- "A person" is always the person at the coordinator's session, who approves in that session. That person also
  records each answer to an open question for humans, and can get the answer from a different person first. A
  message cannot approve anything, and Brigade has no proof of identity (owner ruling, 2026-10-26). Thus this is
  the only approval path in the first version.
- For code, the coordinator's person approves each merge. After that approval, the coordinator can do the merge.

### 4.7 Progress for people

A person can see the progress in four places:

- A status file in the plan folder, which Brigade writes from the plan state.
- A `brigade` command that prints the parts, the holders, the status and the open questions.
- The digest (`brigade report`).
- A web page that a session publishes.

### 4.8 Security

- A plan reaches a participant as untrusted text: a Brigade message or a synced file. It cannot approve anything,
  and it cannot change permissions, settings or instruction files.
- The person's approval of the invitation is the consent point. After that, each part is still a request. The
  participant's own rules and the approvals of its person still apply.
- No secret goes in a plan file, a claim or a report.

## 5. Left to the builder

- The file formats and the file names.
- The command names and flags.
- The body shapes of the messages for invitation, claim, grant, release and result.
- The status names. They must cover open, claimed, waiting for approval, done and blocked.
- How a participant shows that it has a tool.

## 6. Open questions

1. **Name.** "Swarm topic", "swarm plan", or a different name?
2. **Coordinator stops.** Who takes over the plan state, and how? The coordinator is a single point of failure.
3. **Who applies a claim.** security.md §3 says that the watcher does nothing that a message asks. There are three
   routes. (a) The coordinator's model runs a command for each claim message. Each claim then waits until the
   coordinator's current turn ends, which works against goal 3. (b) The owner makes a new exception for claim
   messages. (c) A participant writes a claim file in the synced folder, and the coordinator's watcher applies it.
   The watcher already reads files that teammates write in the state folder, for example `reports.json`
   (security.md §18).
4. **Foreign changes.** How does Brigade find a change to the plan state that the coordinator did not make?
5. **Code split by stream.** Sessions that share one checkout interfere with each other. Must each code part name
   its own branch or worktree? A second checkout of a repository on the same machine does not get the synced folder
   (its state is `conflict_path`). Thus a part in its own worktree loses the plan folder, unless a symlink connects
   the two folders (`docs/team-member/use/sync-folders.md`, "Limits").
6. **Several plans.** Can one team run more than one swarm plan at the same time?
7. **State folder.** Does the plan live in the state folder of the manager digest, or in a different folder? Does
   each open question for humans become a `brigade decision` entry?
8. **Web page.** Which session publishes the web page, and with which tool?
9. **"Immediately".** In §4.5, what does "immediately" mean? The roster can show a dead session online for up to
   10 minutes. A Codex CLI session that quits in daemon mode shows `idle`, not offline, for 30 minutes
   (security.md §16). Must Brigade find a dead holder in a different way?
10. **Several repositories.** How does the plan reach a participant in a different repository? The synced folder
    syncs only in one repository, and one message holds at most 16 KiB. The example plan had 117 coverage rows.

## 7. Assumptions

- Assumption: the browser test of 2026-11-04 is typical of how teams will use a swarm plan.
- Assumption: only Claude Code sessions can publish a web page (an artifact) today.
- Assumption: in §4.5, "immediately" means "when the roster shows the session offline".

## 8. Next steps for card 202

1. Rjae reviews this document, and makes the owner rulings for open questions 2, 3, 7 and 10.
2. Compare designs for the open questions, and write a recommendation.
3. Put the recommendation on card 202: build it (with follow-up cards), change the scope, or stop.

## 9. Outcome (Rjae's review, 2026-11-10)

Card 202's recommendation is **change the scope**. Brigade keeps no shared record of a plan, its parts or their
holders, so §4.1 to §4.7 are not built. Two things replace them.

1. **The prompt pattern.** Several sessions work one plan with a coordinator prompt, a plan file the coordinator
   alone writes, one owner per part as an ordered queue per session, and ordinary messages. It needs no Brigade
   function. The page is `docs/team-member/use/plans.md`.
2. **Plan invitations (card 242).** A session answers a plan invitation for itself. The rulings, all of
   2026-11-10:
   1. A session may answer an invitation for itself. This overturns §4.2's "a session cannot accept for its
      person".
   2. Accepting gives the coordinator nothing: every part that follows is still a request under the session's own
      permissions.
   3. The default is "accept when free": no work for the session's own user in progress, and the tools the
      invitation names. Otherwise decline with the reason. No setting. A quiet session never receives one.
   4. The rule travels in the frame, in Brigade's own words. Protocol v1 does not change.
   5. Measure the invitation variant only, on Claude Code headless, then release.
   6. The name is "Plan invitation".
   7. A message through a gateway (email, Slack, webhook) is never an invitation.

**Why the scope changed.** The one run that motivated the card (the browser test of 2026-11-04) succeeded on
today's Brigade. Goal by goal, the draft added little over a prompt: the format is a file template; a claim through
the coordinator waits for its turn, which works against goal 3; and §4.4 itself says "only the coordinator writes"
is a rule and not a lock. Meeting §4.5's "immediately" needed a change to how the roster reports presence, and two
of the three claim routes of open question 3 needed the watcher to act on a teammate's input.

**The open questions of §6.** Question 1 is answered: "plan", and "plan invitation". Questions 2, 4, 6, 7, 8 and 10
fall away with the plan record: the plan is the coordinator's file. Question 3 falls away with claims: every part
has one owner before work starts. Question 5 stays advice on the page: one session commits per checkout. Question
9 is unchanged: a stopped holder shows as late as the roster shows it, and the coordinator learns of it when a
report is overdue.

**The card's eight questions, answered.**

- **Name.** "Plan", and "plan invitation". Not "swarm topic".
- **Split.** Both. A part is a row of the plan file with an owner and an "after" column, so a plan can split by
  stream of work, by role (implement, then review, then test), or by both. The coordinator decides.
- **Claim.** There is none. The coordinator gives every part one owner before work starts, as an ordered queue per
  session, and only the coordinator reassigns.
- **Storage.** The coordinator's plan file, in a synced folder. A session in another repository gets its parts in
  messages. Nothing in the backend.
- **Failure.** The coordinator notices an overdue report, reads the roster, and reassigns the part.
- **Approval.** The coordinator checks each result. What needs a person goes to the coordinator's user, in that
  session. One session commits per checkout, and its user approves a merge. A message approves nothing.
- **Harnesses.** Sessions of all four harnesses can invite and be invited from 0.49.0. Claude Code was measured,
  and one idle Codex CLI session accepted and did its parts (E17). Muse Code and Grok Build are unmeasured.
- **Protocol.** Protocol v1 does not change.

