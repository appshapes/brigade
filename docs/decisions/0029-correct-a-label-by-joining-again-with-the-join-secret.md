# 0029. Correct a label by joining again with the join secret

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

A team holds one label for each member, sent by the join that made them a member. No command changed it. In the
thinktech team's move of 2026-11-14 (work item 320) the team's administrator joined from a terminal that named
no Claude profile, so the join read the default profile's account, and every session of his on that team showed
under an address he does not use there (work item 326). People address a member by label through the gateways,
and the digest and the decisions are keyed by it.

`brigade team join` in a checkout that was already joined answered `already joined` and dropped a `--label`
without a word. The backends, though, already take a new label: a join whose secret names the team the profile is
bound to is a rejoin (protocol 4.4.10, case C-08), and on a rejoin that carries a label the bundled server, the
Supabase function `join_team` and the local adapter each keep the membership and its principal and replace the
label; with no label they keep the one they hold. The protocol and the REST contract are frozen and have no
request that changes a label.

## Decision

A member corrects the label a team holds for them with the join's own flag: `brigade team join --label <label>`
in a checkout that is joined joins again, with the team's join secret and the label, and says what the team
holds now and what the machine had joined with. It is sent only for a label the command names: without `--label` the command does what it did, and a
re-consent sends no label whatever the `label` option resolves to. A label equal to the one the machine joined
with sends nothing. The checkout's consent is not asked for again, and its pin is not touched.

Nothing changes in the protocol, the REST contract, a migration or a function: the command uses what a rejoin
already does on each backend.

## Alternatives considered

- **A request of its own that changes a label, with no secret.** The plainest for a member who no longer has the
  secret's file. It is a new verb in the frozen protocol and the frozen REST contract, a capability each adapter
  announces, a migration every Supabase project's owner applies and a server release, for a thing a member does
  once.
- **The pages say that the scripted form is the way** (a join document on stdin, outside a session), as the work
  item allowed. Nothing to build, and few would find it; it also runs outside the checkout's team file, so the
  member names the team's profile by hand.
- **A verb of the harness's own, `team label <label>`**, that sends the same rejoin. One more name for what
  `team join --label` already says, with the secret's handling written twice.
- **Send the label the option resolves to on every re-consent.** A second checkout would then quietly rewrite a
  label a member chose, from whichever profile's account that session reads.

## Evidence

- `internal/harness/commands`: `TestTeamJoinWithALabelCorrectsTheLabelOfAMember`,
  `TestTeamJoinOfAMemberSendsNothingWithoutANewLabel`, `TestTeamJoinWithALabelRefusesAnotherTeamsSecret`,
  `TestTeamJoinWithALabelSaysWhenTheBackendMadeAMember`, and in a session
  `TestTeamJoinInSessionWithALabelCorrectsTheLabelOfAMember` and `TestTeamJoinInSessionSecondCheckoutWithALabel`.
  Run against the code before, the first, the second and the last were seen to fail.
- `TestATeamOnAServerThroughTheRealBinary`, step 5b, with the built binary: against the stand-in in `make test`
  and against the built server over TLS in `make server-test` (2026-11-23, server 0.10.0 of the tree): the label
  changed and changed back, one membership, the same principal, the member's binding holding the label.
- `TestIntegrationAJoinAgainWithALabelReplacesIt` on the local Supabase stack, 2026-11-23: a label sent replaces
  the one held, none sent keeps it, and the other member's is untouched.
- The built binary at a terminal of a throwaway home on the local Supabase stack, 2026-11-23: the secret read
  without echo, `team members` and `team status` showing the new label under the same principal, then
  `unchanged:` and `already joined`.
- None measured on a hosted project or a running server: nothing is applied to either.

## Consequences

- Correcting a label needs the join secret. A member who no longer has its file asks the administrator, as for a
  first join.
- A rejoin also does what a rejoin does: on Supabase it sets the membership active and stamps the secret's
  version. A member who is active and presents the current secret loses nothing by it.
- What a machine keeps under a label does not move with it: a digest subscription stays under the earlier label
  until a session that was running subscribes again (work item 414's rule), and a work item assigned to the
  earlier label keeps that assignee. The label's page says so.
- An adapter that kept the earlier label on a rejoin would make the command's line untrue; none of the three
  bundled ones does, and the protocol does not say either way. Revisit if a third-party adapter does.

## References

- Work item 326 on the Brigade Board; work items 320 (where it was seen), 379 and 414 (where a label comes from,
  and what is kept under it).
- `internal/harness/commands/teamsetup.go` (`relabel`, `sendJoin`); `docs/team-member/join/your-label.md`,
  "Correct your label" and "Join from a terminal"; `docs/security.md` section 2.
- `docs/protocol-v1.md` 4.4.10; `supabase/migrations/20260830120000_brigade_schema.sql` (`join_team`).
