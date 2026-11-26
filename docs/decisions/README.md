# Decisions

Brigade records a decision that a newcomer would ask "why" about as an ADR, an architecture decision record: one
file per decision, numbered, with the alternatives that lost and the evidence, and never rewritten once it is on
`master`. [ADR 0001](0001-record-decisions-as-adrs.md) is the decision to work this way and says why. The rules an
agent follows are in [documentation.md](../agent/repository/documentation.md) and
[conventions.md](../agent/repository/conventions.md); `make docs-check` holds them.

## The decisions

- [0001. Record decisions as ADRs in the repository](0001-record-decisions-as-adrs.md)
- [0002. Close a decision's ask from the asker's side, with the ruling as its outcome](0002-close-a-decisions-ask-from-the-askers-side.md)
- [0003. Follow a team that moved, on the old backend's record and a move ticket](0003-follow-a-team-that-moved-on-the-old-backends-record.md)
- [0004. Bound one principal on a server with the reference backend's numbers](0004-bound-one-principal-on-a-server-with-the-reference-backends-numbers.md)
- [0005. Ask for a note only for a commit made in the checkout during the turn, and say what git shows](0005-ask-for-a-note-only-for-a-commit-of-the-turn.md)
- [0006. Take a team in and open its move by request, and let whoever takes it in own it](0006-take-a-team-in-and-open-its-move-by-request.md)
- [0007. Let a session move a team, behind an option that is on by default](0007-let-a-session-move-a-team.md)
- [0008. Follow a move from a running session at its next prompts](0008-follow-a-move-from-a-running-session-at-its-next-prompts.md)
- [0009. Name every ADR that supersedes a part in the Status line](0009-name-every-adr-that-supersedes-a-part-in-the-status-line.md)
- [0010. Keep the record of a team's move on the server it leaves](0010-keep-the-record-of-a-teams-move-on-the-server-it-leaves.md)
- [0011. Remove a team from a server by the operator's command](0011-remove-a-team-from-a-server-by-the-operators-command.md)
- [0012. Show the board in a browser from a page on the member's own machine](0012-show-the-board-in-a-browser-from-a-page-on-the-members-own-machine.md)
- [0013. Derive a send's key from the reply and the summary too](0013-derive-a-sends-key-from-the-reply-and-the-summary-too.md)
- [0014. Read the Claude account's email as the default label on every harness](0014-read-the-claude-accounts-email-as-the-default-label-on-every-harness.md)
- [0015. Stop a release over a red or missing CI run, with an override](0015-stop-a-release-over-a-red-or-missing-ci-run-with-an-override.md)
- [0016. Strike the promise of a new log started from a snapshot](0016-strike-the-promise-of-a-new-log-started-from-a-snapshot.md)
- [0017. Do nothing when brigade runs with no arguments in a hook's place](0017-do-nothing-when-brigade-runs-with-no-arguments-in-a-hooks-place.md)
- [0018. Say in the webhook gateway's 202 why an accepted message waits](0018-say-in-the-webhook-gateways-202-why-an-accepted-message-waits.md)
- [0019. Keep what differs between the harnesses in one host table](0019-keep-what-differs-between-the-harnesses-in-one-host-table.md)
- [0020. Put delivery, local facts and the hook's dialect behind three interfaces](0020-put-delivery-local-facts-and-the-hooks-dialect-behind-three-interfaces.md)
- [0021. Open Grok Build's unmeasured refusals behind one option](0021-open-grok-builds-unmeasured-refusals-behind-one-option.md)
- [0022. Give each harness a status in the registry](0022-give-each-harness-a-status-in-the-registry.md)
- [0023. Start a new team on a Brigade server](0023-start-a-new-team-on-a-brigade-server.md)
- [0024. Write each skill once and generate the four copies](0024-write-each-skill-once-and-generate-the-four-copies.md)
- [0025. Hold a skill's description to the length a loader accepts](0025-hold-a-skills-description-to-the-length-a-loader-accepts.md)
- [0026. Keep Grok Build's subagent check and its refusal of another harness's marker](0026-keep-grok-builds-subagent-check-and-its-refusal-of-another-harnesss-marker.md)
- [0027. Keep the roadmap as work items of another kind, in one order](0027-keep-the-roadmap-as-work-items-of-another-kind-in-one-order.md)
- [0028. Ask the CI check again in the release workflows, with the override in the tag's message](0028-ask-the-ci-check-again-in-the-release-workflows-with-the-override-in-the-tags-message.md)
- [0029. Correct a label by joining again with the join secret](0029-correct-a-label-by-joining-again-with-the-join-secret.md)
- [0030. Pin the Codex CLI and Grok Build packages to the last release in their marketplace files](0030-pin-the-codex-cli-and-grok-build-packages-to-the-last-release-in-their-marketplace-files.md)
- [0031. Name the command by the package's path in the lines a session reads on a host with no brigade on PATH](0031-name-the-command-by-the-packages-path-on-a-host-with-no-brigade-on-path.md)
- [0032. Run each scheduled job as one workflow, named by a standing work item](0032-run-each-scheduled-job-as-one-workflow-named-by-a-standing-work-item.md)
- [0033. Hand an agent the job's own token, and name the jobs that hold the Claude App's](0033-hand-an-agent-the-jobs-own-token-and-name-the-jobs-that-hold-the-claude-apps.md)
- [0034. Say the roster-line reminder from Grok Build's Stop hook](0034-say-the-roster-line-reminder-from-grok-builds-stop-hook.md)
- [0035. Record which work item a session works on in the board folder](0035-record-which-work-item-a-session-works-on-in-the-board-folder.md)
- [0036. Read each session's usage from its harness's own records](0036-read-each-sessions-usage-from-its-harnesss-own-records.md)
- [0037. Estimate a work item's cost from a shipped price table and a member's file](0037-estimate-a-work-items-cost-from-a-shipped-price-table-and-a-members-file.md)

## Record a decision

1. **While it waits on someone**, it is not a file. A decision that waits on the owner's own hand, on another
   session or on evidence not yet measured is a work item on the Brigade Board tagged `decision` beside `brigade`:
   the question, the options and the evidence in the sections of [the template](0000-template.md), and the ruling
   as a comment. Ask for the owner's ruling with
   `brigade decision --to <the owner's roster label> --card <work item>`, the question on stdin. The session that
   receives the answer records it with `brigade work-item comment <work item>`, the owner's words on stdin. The
   asking session, or another session of the member who asked, closes the ask with
   `brigade decision --close <the ask's id>`, the ruling as its outcome on stdin; a session of another member
   that receives the answer records it and tells the asker (ADR 0002).
2. **Once it is decided**, copy the template to `NNNN-short-title.md` with the next number, the title in the
   imperative, and write it in the same change as the code, the rule or the page it explains. Until it is on
   `master` it can still be revised, on a pull request's branch too. Its Status is
   `Accepted` (or `Rejected`, for a decision ruled against that a newcomer would still ask about); a file is never
   Proposed. Its References name the work item it was carried by or made in.
3. **List it** under "The decisions" above, by its number and title.
4. **Land it** with the change. The work item's landing comment names the ADR.

A decision a session makes on its own inside a work item needs no work item of its own: its ADR lands in that work
item's change.

## Change one that is on master

An ADR keeps its text. What can still change:

- **Its Status line**, when a later ADR supersedes it: `Superseded by NNNN`, or `Accepted; superseded in part by
  NNNN` when only part of it is replaced. When more than one ADR has replaced a part, the line names each once,
  the lowest number first: `by 0002 and 0009`, `by 0006, 0007 and 0008` (ADR 0009). A change of mind is always a
  new ADR.
- **A fact that was wrong when it was written** (a date, a count, a citation, who decided something): correct it
  in place and add one entry to the header, after the Supersedes line and any earlier entry:
  `- **Corrected:** YYYY-MM-DD: <what was wrong, and what the ADR says now>`, continued on lines indented two
  spaces when it is long. Earlier entries stay as they are.

The title, the other header lines and the Decision section never change, and an ADR is never removed or renamed.
Two things are not a change. A link is repointed when the page it names moves: a link's target is not part of an
ADR's text, its words are. And when two ADRs have landed under one number, one of them takes the next free number,
in its file name and its title line, with nothing else changed.

## What the gate holds

`make docs-check` (the `decisions` rule) fails when an ADR is not named `NNNN-short-title.md`, reuses a number,
does not start `# NNNN. `, has no valid Status or no `## Decision` section, names a superseding ADR that does not
exist or names the ADRs that supersede a part out of order, is not listed under "The decisions" above, or carries a Corrected entry that is misspelt or outside the
header. Where the tree is a git repository it also compares each ADR with its text on `master` as the tree has
it, and fails when a landed ADR has changed beyond what the section above allows, or is gone from the tree: in
the working tree, in a commit that is not on `master` yet, or in a pull request. Putting the landed text back
always passes. The gate does not look back at what `master` already did.
