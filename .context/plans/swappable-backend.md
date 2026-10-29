# A swappable backend: the gateways, folder sync and the harness over the adapter protocol (Trello card 156)

Owner: Rjae. Session: 155-review-adapter-swap. Written 2026-10-29 from card 155's review (its findings are the
first comment on that card). Status: proposal, awaiting the rulings of §10; nothing in §9 starts before them.
Rows P63-* in brigade-execution-log.md once work begins. Revised the same day after Rjae's clarification: the
green field (an organisation's first installation, not on Supabase) is the main focus; moving an existing team is
wanted but second (§1).

## 1. The goal, restated

Brigade's original design goal: a team runs Brigade on a backend of its choosing, through an adapter that speaks
BAP/1 (`docs/protocol-v1.md`), with no loss of features. Rjae's criteria for the move off Supabase, stated
2026-10-27 after card 141 (`.context/plans/syncthing-adapter.md`, head): no loss of functionality; every gateway
remains; members do nothing beyond `/brigade:update`; nobody rejoins; no device ids in the team file.

Two scenarios hide in "swap the backend", and Rjae ranked them on 2026-10-29: the green field is the main focus,
the move is wanted but second.

- **S1, the green field: an organisation's first installation, choosing not to use the Supabase adapter.** It
  creates its team on backend X and gets everything a Supabase team gets. Phases 0 to 4 deliver S1, and they are
  ordered by that organisation's install sequence.
- **S2, an existing team moves from Supabase to X.** Every member's credential is a principal of the old backend
  and means nothing to the new one; "nobody rejoins" needs the old backend to vouch for each member to the new
  one, a cross-backend trust primitive the protocol does not have. Phase 5 designs it and needs a ruling; until
  then S2 is one `/brigade:join <path>` per member, which is today's path for a changed team file.

**The yardstick: the green-field experience.** The plan is judged by what each person does on day one once it has
shipped:

- The Project Owner picks a backend: the Supabase project (today's path, unchanged); the reference server Brigade
  ships, run on a machine of theirs; or their own server implementing the REST contract in their own stack. Only
  the last one writes code, and it is a server, not an adapter. They run the gateway process beside it for email
  and Slack.
- The Team Admin runs `brigade team create --adapter rest --backend '<json>' --name <team> --secret-file <path>`
  and commits the team file: as on Supabase but for the two flags.
- Each member installs the plugin and runs `/brigade:join <path>`, nothing else, because the `rest` adapter is
  bundled in the binary and needs no registration. Only a third-party adapter asks a member to register it once.
- Each session has every feature: sessions, messages, doing, quiet, folder sync, email and Slack, the admin verbs.

Today that organisation writes a Go adapter against `docs/adapter-authors.md`, registers it on every machine,
puts a placeholder URL and key in the team file, and has no email, no Slack and, unless the adapter stores
`sync_peer`, no folder sync that says why.

## 2. What card 155 found, in one paragraph

The messaging core is adapter-neutral and proven on the fs adapter. What breaks is in four groups. (A) The mail and
Slack gateways, several teams per project and the whole Project Owner tree live inside the Supabase project (Edge
Functions, the `brigade_gateway` schema, `pg_cron` ticks) and are lost outright. (B) `team.admin`,
`session.inbound`, `session.sync_peer` and the roster columns survive only if the adapter advertises them, and
folder sync fails silently without `session.sync_peer`: no error, 0 peers for ever. (C) The harness itself is
Supabase-shaped: `.brigade.json` requires `url` and `publishable_key` from every adapter, `team create` requires
`--url` and `--key`, every join sends a Supabase `backend` object and needs a `brg1.` secret, and the harness never
checks `team.join`, so an adapter whose membership is managed elsewhere cannot attach a session. (D) The security
document argues the Supabase adapter; the protocol's safety rules are conformance-checked, but whether they form a
boundary is each adapter's.

## 3. The principle: a backend holds four nouns

A backend holds **teams, members, sessions and messages**, reached only through BAP/1. Everything else Brigade does
is a Brigade process over the protocol, or state on the machine that runs that process: the harness (injection,
hold, notifications, doing, folder sync), the gateways (threads, dedupe, allowed senders, provider glue), the
conformance suite. The Supabase project today holds three things outside the four nouns, and §4 moves all three
out: the gateway functions, the gateway schema (`gateways`, `threads`, `received`, settings, allowed senders) and
the per-minute ticks. What stays in a backend beyond the nouns is the adapter's own business: credentials,
retention sweeps, rate limits, the realtime channel.

The test of the principle is mechanical: `make e2e` (the P4 proof scripts) runs green against any conforming adapter
given on its command line, and the gateways and folder sync run in that proof.

## 4. The gateways: one Go process over the protocol

**Design.** The webhook gateway (`internal/gateway/webhook`, card 137) is already the shape: an ordinary member whose
process polls its inbox through `adapterclient`, keeps its own state, and never reads a store. The hosted mail and
Slack gateways become two more kinds of the same process.

- **One command, three kinds.** `cmd/brigade-gateway` (the webhook gateway renamed and generalised; the old name
  stays as an alias for one release) with `--kind webhook|mail|slack`. `internal/gateway/webhook` becomes
  `internal/gateway/core` (session, poll loop, receive and ack with retry, `--member` resolution, framing, the
  `to:` directives) plus one package per kind. The core is what `supabase/functions/_shared/db.ts` does with the
  protocol verbs (`sendMessage`, `fetchInbox`, `ackMessages`, `heartbeat`, `registerSession`, `listSessions`),
  and the `Backend` interface of `gateway.go` already names exactly those.
- **State on the gateway's machine.** The thread table, the received-id dedupe, the settings and the allowed
  senders move to `<state dir>/gateway/<name>/`: JSON files under a flock, as the fs adapter's store, with the
  90-day thread sweep in the process. No database, no new dependency.
- **The tick is the loop.** The process polls every 5 s (the webhook gateway's default) instead of the minute
  `pg_cron` gave; latency drops.
- **Several teams.** One process holds one credential directory per team it serves, exactly as the Edge Functions
  serve every team of the project. The 2026-10-22 rulings of `.context/plans/slack-gateway-several-teams.md`
  carry over unchanged: one Slack app and bot per workspace, one gateway principal per team, the grouped roster,
  the grouped answer for an ambiguous `to:`.
- **Mail: the connector contract is unchanged.** `docs/mail-connectors.md` already splits the core from the
  provider: a connector POSTs the inbound shape to the core's inbound URL with the bearer secret, and the core
  POSTs the outbound shape to the connector's send URL. The shipped Resend and Postmark connectors can keep running
  as Edge Functions (they hold no data) or run in the gateway process (`--connector resend|postmark`). The one new
  requirement: the core's inbound URL must be reachable by the connector, so the gateway runs where it has HTTPS,
  or an IMAP connector polls a mailbox and no inbound URL is needed (§10, ruling 3).
- **Slack: Socket Mode for the self-hosted case.** A websocket from the gateway to Slack with an app-level token;
  no public URL, and the slash command works over it too. The Events API path stays for a gateway that has a
  public URL. The signing-secret verification, the dedupe by event id and the thread rules move as they are.
- **Deployment.** The binary and the systemd unit as `docs/project-owner/configure/webhook-gateway.md` has them,
  plus a container image the release publishes (§10, ruling 4). For the AppShapes-hosted default project of card
  149, AppShapes runs the processes: the same code, the same recipe.
- **Transition for Supabase teams.** The Edge Function gateways keep working while the Go gateway reaches parity;
  the bar is `scripts/mail-connector-check.sh` green through the Go core and the live Slack exchange card 106
  measured. Then ruling 2 decides whether the Edge Functions are retired (one implementation of the routing rules,
  which today exist twice: `resolveMember` in Go and `resolveRecipient` in `gateway.ts`) or kept as the zero-ops
  deployment for Supabase teams. Recommendation: retire them one release after parity.

**The experience.** The admin installs the Go gateway as a new member of the team (`brigade-gateway install`
joins with a secret file, writes the config, and for mail configures the provider's webhook; the Edge Function's
gateway member is revoked by the same command, never copied: card 150's lesson). The `gateway` member of
`.brigade.json` is unchanged, so the address and the bot a person writes to stay the same. Members do nothing. On
a backend other than Supabase the same install runs, which is the whole point.

## 5. Folder sync

Folder sync is not lost; it needs one session fact, `sync_peer`, that the adapter stores and lists. Two steps:

1. **Say so at session start (phase 0).** The hook knows the adapter's capabilities; a project with a `sync`
   member and an adapter without `session.sync_peer` gets one line, `Brigade sync: off for this session; the
   adapter does not carry sync peers`, instead of 0 peers for ever. The same line pattern names a quiet session the
   team cannot see (`session.inbound`) and the admin verbs the adapter lacks (`team.admin`).
2. **A generic session fact (phase 2).** `session.facts`: an opaque JSON object of string members on the
   registration and the heartbeat, stored verbatim under a size cap and listed back, under one capability. Today
   every new per-session fact (`model`, `context_used_tokens`, `brigade_version`, `sync_peer` each had a
   migration) costs every backend a change; with `facts`, a backend written once carries the facts a later Brigade
   adds. `sync_peer` keeps its own member for the backends that have it and moves into `facts` for the rest.

The peer exchange stays in the backend on purpose: the roster is the one directory a joiner can trust (card 141's
lesson), and no device id goes into the team file.

## 6. The harness: nothing Supabase-shaped from any adapter

- **Team file version 2.** `backend` is one JSON object, adapter-defined, that the harness never interprets: it
  passes it to `profile init` and `team join` verbatim and shows its `display` member (or its `url`'s host) at the
  consent gate. The Supabase adapter's is `{url, publishable_key}`; the fs adapter's is `{}`. The secret scans
  over the whole document stay. A version-1 file is read as today. Members update before an admin commits a
  version-2 file, as the `sync` member precedent (`docs/team-admin/configure/team-file.md`): the VERSION column
  shows who is behind, and a plugin that cannot read it says `names a version this build does not read`.
- **`team create`.** `--adapter <name> --backend '<json>'`; `--url` and `--key` stay as the Supabase adapter's
  spelling of the same thing. `profile init` receives the object; the harness stops composing adapter flags.
- **Join without a secret.** When `describe` omits `team.join`, `brigade team join` binds from
  `describe.profile` (team, principal) after the consent gate and sends nothing: the adapter's own `profile init`
  established the identity (a login, a token the organisation issued). The `brg1.` secret shape stays the
  convention for adapters that have secrets.
- **Registering an adapter.** `brigade adapter register <name> <path>` writes the `adapters.json` entry (0600),
  and `brigade adapter check <name>` runs `describe`, the `brigade-full` profile of §7 and the conformance
  suite's fast cases, printing which Brigade features that adapter carries. Distribution stays manual by rule: the
  team file names a dialect and never an executable.
- **Hosts.** `describe.profile` gains an optional `hosts` list so the sandbox line at session start names the
  backend's host instead of `<ref>.supabase.co`.

**The experience.** A Supabase team changes nothing: `team create --url --key` still works and writes a file the
members already read. A team on another backend writes a `backend` object instead of a placeholder URL and key.

## 7. The protocol: additive, under capabilities

All of this is additive and lands by the invariant: `internal/protocol`, `docs/protocol-v1.schema.json`, the
conformance suite and both adapters in one commit each.

- **The `brigade-full` profile** in 4.7: the set of capabilities the Claude Code harness needs for every feature
  (`team.roster`, `team.admin`, `session.description`, `session.inbound`, `session.human_label`,
  `session.brigade_version`, `session.sync_peer` or `session.facts`, `message.watch.stdin_commands`). Not a
  requirement on adapters, a name the harness and `adapter check` can print against.
- **`session.facts`** (§5).
- **The three admin verbs specified.** `team rotate-secret`, `team revoke-member` and `team transfer` get request
  and result shapes and conformance cases, so they mean one thing on every backend; today they are
  "adapter-defined, not specified further".
- **A backend without `team.join`** gets its binding rule written down (§6) and a conformance case.
- **A REST backend contract** (phase 3): `docs/backend-rest-v1.md`, the four nouns as HTTP, so an organisation
  implements a server in its own stack and reuses Brigade's one adapter for it.

## 8. The proof: a second backend that is not a test fixture

Rjae's example, "an opaque REST API", is the strongest form of swappable: the organisation writes a **server**, not
an adapter. Phase 3 builds `brigade adapter rest` (bundled beside `supabase`, conformance-tested once) against
`docs/backend-rest-v1.md`, and `cmd/brigade-server`, a reference server in Go with a file store, so that a team can
run Brigade with no Supabase and no cloud account at all, and so that the contract has one implementation anyone
can read. Storage for the reference server is a ruling (§10, ruling 7): a Postgres driver was refused as a
supply-chain decision in `.context/plans/backend-upgrade.md` §2, and `docs/allowed-deps.txt` is the gate.

Proof, in this order: the conformance suite green on the rest adapter against the reference server; `make e2e`
green with `BRIGADE_ADAPTER_COMMAND` naming it; then a green-field team made by the yardstick of §1 (a fresh team,
two members, folder sync, and the gateways once phase 4 ships), run for a week with its messages as the
measurement. The `brigade` team's own move is phase 5's measurement, not this one.

## 9. The phases, in order

Each phase is its own release, changes no default (owner ruling 2026-10-06), carries its pages and changelog
section through `make docs-check`, and adds its rows to the execution log.

| Phase | What | Size | Needs |
| --- | --- | --- | --- |
| 0 | The contract page (`docs/developer/backend-contract.md`: §3 in two hundred words), the `brigade-full` profile, the session-start lines of §5 step 1, `adapter check`'s first form (describe plus the profile), a Project Owner page that says honestly what running Brigade without Supabase takes today (the last paragraph of §1), and the two documentation nits card 155 noted (`adapter-authors.md` still says darwin and linux only; `team-file.md`'s example uses `key`). | small, days | nothing |
| 1 | The harness of §6: team file version 2, `team create --backend`, join without a secret, `adapter register`, `hosts`. | medium | ruling 5 |
| 2 | The protocol additions of §7 except the REST contract: `session.facts`, the admin verbs, the no-`team.join` binding, each with conformance cases and both adapters. Phase 3's contract is written against these. | medium | ruling 6 |
| 3 | The REST contract, the bundled `rest` adapter, the reference server, the proof of §8, the Project Owner pages for a backend that is not Supabase (choose a backend, run the reference server, implement the contract) and the setup skill's branch on `adapter`. | large | phases 1 and 2; ruling 7 |
| 4 | The Go gateway process of §4: core, mail kind, Slack kind, install verb, systemd and container recipes, parity measured on Supabase and on the reference server, the Edge Functions kept for one release. | large, weeks | rulings 2, 3, 4; phase 3 for the second backend to measure on |
| 5 | S2: moving an existing team (§1). Design the vouching primitive, or settle on one `/brigade:join` per member and write the migrate page. | medium, or a page | ruling 8; phase 3 |

Why this order. It is the green-field organisation's install sequence: a backend to run, a team file that carries
its configuration, members who join, then the gateways beside it. Phase 0 is cheap and ends the one silent
failure today. Phase 1 is what a third-party adapter hits on day one (the placeholders). Phase 2 settles what the
contract must say about facts and the admin verbs before it is written. Phase 3 is the piece that makes "not
Supabase" practical: a server in the organisation's own stack, or the reference server, and no Go. Phase 4
completes the feature set and is the loss Rjae named first; it depends on nothing in phases 1 to 3 (the webhook
gateway already runs over any adapter) and could run in a parallel session from phase 0 on, which is the one
reordering worth considering; it also serves card 149, since AppShapes would run the same processes for the
default project. Phase 5 waits for a second backend to exist.

## 10. Rulings needed (Rjae)

1. **The goal's shape.** S1 (the green field: a first installation on any backend, full features) is the main
   focus and the goal of phases 0 to 4; S2 (moving an existing team) is phase 5 and may end as one command per
   member. Recommendation: yes.
2. **One gateway implementation.** The Go gateway process replaces the Edge Function gateways one release after
   parity, for Supabase teams too. Recommendation: yes; two copies of the routing rules already drift.
3. **Mail reachability.** The gateway's inbound URL is reached by (a) running the gateway where it has HTTPS (a
   container, a droplet), (b) a tunnel, or (c) an IMAP connector that needs no inbound URL. Recommendation: (a)
   as the documented path, (c) as the second connector, (b) undocumented.
4. **A container image** published by the release for the gateway (and in phase 3 the reference server).
   Recommendation: yes, one image, built by `release.yml` from the same tag.
5. **Team file version 2** with the `backend` object, members updating first. Recommendation: yes, in phase 1; a
   green-field team starts on version 2 and never sees the change.
6. **Protocol additions** `session.facts`, the admin verb shapes, the no-`team.join` binding, the `brigade-full`
   profile: all additive under 4.7. Recommendation: yes, one commit each by the invariant.
7. **The reference server's store.** A file store as the fs adapter's (small teams, one process), or a pure-Go
   embedded database added to `docs/allowed-deps.txt`. Recommendation: the file store first; the contract is the
   deliverable, not the server's scale.
8. **S2's vouching primitive**, or one `/brigade:join` per member. Note 4.5.15: a message never approves anything,
   so an invite that arrives as a message cannot join by itself; a primitive would have to be the old backend
   answering the new one directly. Recommendation: one command per member for the first version, measured on the
   `brigade` team's own move once phase 3's backend exists.

## 11. What this plan does not do

- It does not change the Supabase path for the teams on it: `team create --url --key`, the installers and the
  Project Owner pages keep working, and card 149's default project is one more Supabase project.
- It does not revive the backend-free transport (card 148): every shape here keeps a hosted directory.
- It does not change a wire shape of BAP/1; every addition is under a capability.
- It does not change a default or add a member option.
- It does not make the harness verify a backend's security: §D of card 155 stays true, and the contract page says
  which guarantees are the adapter's and which are the harness's.
