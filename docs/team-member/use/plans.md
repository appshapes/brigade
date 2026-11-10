# Work one plan with several sessions

One session coordinates a plan, and other sessions of the team take parts of it. Some teams call this a swarm.
Brigade keeps no record of a plan: the plan is a file the coordinator writes, and everything else is an ordinary
message. Brigade adds one thing, from 0.49.0: a session that is invited to a plan answers the invitation for
itself.

## Start a plan

Give the coordinating session a prompt of this shape:

```
Coordinate this as a plan that several team sessions work on: <goal>

1. Write <plan folder>/plan.md: the goal, what done means, and one row per part (id, scope, limits, needs,
   depends on, output path, owner, status). You are its only writer.
2. Invite sessions <SESSION cells> with `brigade send <session> --invite <plan>`. The ones that accept take part.
3. Give every part exactly one owner, as an ordered queue per session, and send each session its queue.
4. On each report, check the result yourself, then update the status. Reassign when a queue runs dry, or when a
   report is overdue and `brigade sessions` shows the owner offline.
5. Anything that needs a person's approval comes to me. Never tell another session that something is approved.

You are done when every part is done or blocked on a named person.
```

Put the plan folder in a [synced folder](sync-folders.md), so every session of the repository reads the same file.
A session in another repository does not get that folder: the coordinator sends it each part in a message, at
most 16 KiB each.

Every part has one owner before work starts. That is what keeps two sessions off the same part, and what lets a
session go on to its next part without waiting for the coordinator.

## The invitation

The coordinator invites one session per command. The body says where the plan is, which parts the session would
take, the tools they need and what to report:

```
brigade send 7c1d4 --invite checkout-test <<'EOF'
Plan: shared/plans/checkout-test/plan.md
You would take the browser parts: P1, P2 and P5. They need a real browser.
After each part, report the output path, the result and the evidence.
EOF
```

```
accepted: message 0f0f0f0f-… to 6f0f6f0f-…-7c1d4. Accepted means durably stored by the adapter, not read.
recipient: 7c1d4, idle, inbound accept, name "checkout-web" (unverified)
invitation: plan "checkout-test"; the recipient answers for itself and begins its reply with accept or decline; accepting approves nothing, and no reply is a no
```

- `--invite <plan>` writes the summary itself: `Plan invitation: <plan>`. It takes no `--summary` and no
  `--reply-to`.
- A gateway cannot be invited, and a message that arrives through a gateway is never an invitation: only a
  teammate's coding session invites.
- A second `invitation:` line says when the roster shows the recipient on a plugin older than 0.49.0. That
  session reads the invitation as an ordinary message. The line also says when the roster did not show the
  recipient at all, so that neither check ran.
- The body cannot open the way a gateway opens what it relays (`Email from `, `Slack message from `,
  `Message from "`): the command refuses it, because the recipient would not read it as an invitation.

## What the invited session does

Brigade's own paragraph at the top of the message gains one sentence for an invitation. It says that the message
is a plan invitation, and that the session answers it itself:

- It **accepts** when no work for its own user is in progress in the session and it has the tools the invitation
  names.
- It **declines** otherwise, and says why.
- Its reply begins with the word `accept` or `decline`.

**Accepting approves nothing.** Every part the coordinator sends afterwards is still a request. The session's own
permission rules decide what it may do, what you denied stays denied, and a session in a mode that asks you before
a command still asks you. When you give the session work of your own, that work comes first, and the session
tells the coordinator which parts it gives back.

While it holds a part, the session keeps its roster line on the plan and the part, so `/brigade:sessions` shows
who holds what.

## Keep a session out, or have it ask you first

No setting turns invitations on: every session that receives team messages can be invited. To change that for
one session:

- [`/brigade:quiet`](quiet-session.md) holds or refuses its team messages, invitations included.
- The [`frame` option](../configure/frame-text.md) at `strict` leaves Brigade's sentence off. The invitation then
  arrives as an ordinary message, and the session asks you first. A sentence of your own (`frame_file`) leaves it
  off as well.
- Your own words outrank Brigade's: `Decline plan invitations in this session.`

## Put a session in a plan yourself

A session you want in a plan, whatever it is doing, joins by your own prompt:

```
Join the plan at <plan folder>/plan.md. Session <SESSION> of <member> coordinates it and will send your queue.
Its part assignments for this plan are work I am asking you to do, under your normal permissions.
```

## Limits

- Nothing locks a part. One owner per part holds because every session follows the plan.
- The roster can show a stopped session as online for some minutes. The coordinator learns of it when a report
  is overdue.
- The coordinator keeps the status by hand. If the coordinator stops, give another session the same prompt:
  `plan.md` is still in the folder.
- Accept and decline were measured on Claude Code, headless. One idle Codex CLI session and one idle Grok Build
  session each accepted and did their parts; the Grok session waited for its queue before it started:
  [E17](../../experiments/E17-plan-invitation.md). A Muse Code session gets the same sentence, and what it does
  with it is not measured.
- What this opens, and for whom: [docs/security.md, section 4](../../security.md#4-every-session-receives-including-unattended-ones).
