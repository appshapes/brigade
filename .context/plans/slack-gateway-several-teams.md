# Several teams on one Slack gateway

Status: **in progress, 2026-10-22**. **Trello card 128**
(<https://trello.com/c/WAB36Spn/128-several-teams-on-one-slack-gateway>, AppShapes › Brigade): its number goes on
the commits. Owner: Rjae. The brief this answers is `.ignored/slack-gateway-several-teams.md` (gitignored, written
2026-10-22 by an AAFP session); the assessment and the six rulings were made in the Brigade session `brigade-d7`
the same day.

## 1. The problem

A Supabase project runs one Slack gateway and, until this work, that gateway serves one team. `slack-in` and
`slack-out` read the team from the project-wide function secret `BRIGADE_TEAM_REF`, and the installer's second pass
overwrites it, so a second team's install takes the gateway from the first. On 2026-10-22 the second team of a
shared project ran the second pass with the first team's own Slack app token (the two team files carry the same
`gateway.slack` line), and the first team's `slack-gateway` session went offline: its gateways row, member and
session survived, since the table is keyed by `(team_id, kind)`, but nothing read them.

The mail gateway solved the same problem for mail in 0.22.0 (card 49): the per-team settings moved into the
gateways row, `mail-out` ticks every row in turn, `mail-in` routes by the inbox a mail reached. Mail has a routing
key per team (the inbox); Slack has none, which was the design question.

## 2. Rulings (Rjae, 2026-10-22, each discussed and agreed in turn)

1. **Shape A: one Slack app, one bot, several teams.** Not one app per team. One project therefore serves one
   Slack workspace; a team whose people are in another workspace gets its own project. A per-team app id could be
   added by a later migration without undoing this.
2. **An ambiguous `to:` value.** A full session id cannot collide (ids are unique across the project). An
   ambiguous five-character tail is refused with the full ids, as today. A member label on exactly one team goes
   through as today. A label on several teams (a credential is per team per machine, so one person is one
   principal per team) is answered with the candidates grouped by team and their five-character tails, up to the
   three best per team, so the second attempt always goes through. A label two different principals share inside
   one team is still refused with the principal prefixes. The gateway never picks a team for the person.
3. **One gateway principal per team**, as the installer makes today. The installer's existing-member branch, the
   remove page and the per-principal rate limits (60 a minute, 600 an hour) stay per team.
4. **No `team:` header line.** The grouped answer covers every ambiguous case; a header line would not work in
   the slash command, and would add a paragraph to four documents for a line almost nobody types.
5. **The roster** a person sees lists every served team, grouped by team name in alphabetical order, online
   sessions only (card 51's rule), "(none)" under a served team with nobody online. With one served team the
   output is what it was.
6. **Secrets in the gateways row** arise only under shape B and are moot: the bot token and signing secret stay
   project-wide function secrets, as the mail provider key does.

Standing value (Rjae, card 51): "our highest value is simplicity of use and simplicity of documentation".

## 3. The change

No schema migration: the gateways table already carries one Slack row per team.

- `supabase/functions/_shared/db.ts`: `listSlackGateways(sql)` returns every `kind = 'slack'` row with its team,
  oldest first, as `listMailGateways` does. Nothing per team is read from the environment any more.
- `supabase/functions/_shared/gateway.ts`: `resolveRecipientAcross(arg, teams)` applies ruling 2 over several
  teams' rosters and returns the recipient with its team; `resolveRecipient` (one team) is unchanged and is what it
  delegates to once the team is known.
- `supabase/functions/_shared/slack.ts`: `slackRoster(teams, gatewayIds, botHandle)` takes the served teams and
  groups by team name (ruling 5).
- `supabase/functions/slack-out/index.ts`: loops over every Slack gateway row, heartbeating and draining each in
  turn, one team's failure not stopping the next; the scope check and the @handle cache are per tick, not per team.
  The report gains `gateways: [...]` and `error`, mail-out's shape.
- `supabase/functions/slack-in/index.ts`: loads every row, reads every team's roster as that team's gateway
  member, resolves the target across them (a thread reply's target is a full session id, so it needs no special
  path), then sends and records through the matched team's gateway. The error text of a refusal is escaped for
  Slack, since it may now carry session and team names.
- `scripts/slack-gateway-install.sh`: no longer writes `BRIGADE_TEAM_REF` (five secrets, not six); runs once per
  team with that team's `team_file=`; its header and dry-run text say so. The secret stays in the project for the
  mail installer, which still reads it for a mail row installed before 0.22.0.
- Tests: Deno cases for `resolveRecipientAcross` (unique id and tail across teams, ambiguous tail, label on one
  team, label on two teams, label two principals share in one team, no match, empty) and for the grouped roster; a
  pgTAP case with two teams' Slack rows in one project.
- Docs: `docs/project-owner/configure/slack-gateway.md` (the one-per-project sentence goes; a "Several teams"
  section like mail's), `docs/project-owner/remove/remove-a-gateway.md` (the last paragraph names Slack),
  `docs/team-member/use/people-on-email-and-slack.md` (what a person on two teams sees), `docs/security.md` §14 (one
  sentence: a bot serving several teams reaches every served team's sessions from the one workspace), `CHANGELOG.md`
  `## [0.37.0]`, the execution log row.

## 4. Upgrade and the live check

Redeploying the two functions is the whole upgrade: every existing row is served from the first tick, the secrets
are unchanged, and the displaced team's session comes back online without anyone running the installer again. A
project whose two team files carry the same `gateway.slack` line has one app, which is what shape A needs.

The live check is on the shared project with both teams: both `slack-gateway` sessions online within a minute of
the deploy; `/brigade sessions` shows both teams grouped; a DM `to: <a session on each team>` reaches the right
team; a session on each team posts to a channel and a reply in that thread comes back to it. The human half of
that check is the teams' own. The appshapes project's gateway is redeployed the same way and checked from this
checkout's own session.

## 5. Interim

Until the functions are redeployed, the shared project serves the team whose ref the secret names. Re-running the
installer with the other team's file would take the gateway back and start the ping-pong the brief describes; the
redeploy ends it for both.
