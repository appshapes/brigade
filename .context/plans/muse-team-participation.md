# Muse Code participation in Brigade: card 88 review and the live-test plan

Date: 2026-10-15 (revised the same day after an adversarial review, section 9)
Card: AppShapes Trello 88, "Research Muse support for Brigade" (list Brigade)
Status: proposed — not scheduled. Scheduling a workstream in `.context/plans/brigade-execution-log.md` is an owner
decision (P8-1 precedent); this plan adds no row. Nothing in section 6 is to be implemented before the live test
of section 5 has run and an independent verifier has signed its evidence.
Scope: one repository, one shared Brigade Go runtime, a Muse package separate from `plugin/` if the test passes.

## 1. Read this first

The card's answer (2026-10-14, desk research) is "Yes, on paper". After a second desk pass on 2026-10-15 — Meta's
docs read again, the changelog downloaded raw, the SDK reference site, the strings of the current Linux binary
(never run), and the measurements of eight third-party projects that have driven Muse hooks and session
messaging — the honest answer is **"Not as the card describes, and the decisive route was measured closed on
1.4.3."** Four things change:

1. **An outside process could not send a session message at all.** The card's open question 1 ("does a person
   have to approve?") sits behind a question 0 it did not ask. On Muse 1.4.3 (Linux), `muse session-message
   send --target <name>` from a plain shell answered `external_agent_ingress_closed`, exit 1, and the idle
   receiving session did nothing — no approval card, no turn. With the undocumented
   `MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=1` set on both ends the send got further and was refused at
   admission as `containment_limited` / `causal_metadata_invalid`, the **target's** receipt unverified
   (`unverified_target_receipt`, `target_verification: unverified_kernel_peer`); still no card, no turn. A sender
   inside the target's own process tree (an MCP server Muse spawned, `MUSE_SESSION_ID` set) hit the closed gate
   in the default build — which says nothing about admission for that position. `external_agent_ingress` is a
   **server-fetched, per-account feature gate** (`$XDG_DATA_HOME/muse/feature-config/`), and that account's
   gates held `local_session_messaging: true` and no `external_agent_ingress`. (aiur-team/khala spikes on
   1.4.3-R5018.1; agent-fleet saw "external agent ingress is unavailable" on 1.3.0.) No measurement exists for
   1.4.4–1.4.9. The native Muse-to-Muse control in the same rig *did* wake an idle session with no prompt
   (`exact_profile_match`), so the receiving side works; the sender's admission is the whole question. Two
   facts in the binary narrow it further: the only *verified* sender identity is a macOS code-signature check on
   the Muse CLI itself (`verified_macos_cli`; Linux senders are "Unverified Linux process"), and an admitted peer
   is remembered as a **generation** (uid, pid, boot instance, code identity), so an "Allow for this session"
   grant may be per sender process — for a one-shot CLI sender, per message.
2. **Brigade's plugin cannot run on Muse as it stands.** All three hooks in `plugin/hooks/hooks.json` are
   exec-form (`command` + `args`), which Muse's own diagnostics call "D99: Claude `command` plus `args` argv
   execution is not implemented yet (#10133)"; a Claude-format hook command "must be a string" and is run
   through a shell. `userConfig` is a recognised declaration that "installs but stays inactive" — the string
   `CLAUDE_PLUGIN_OPTION` occurs nowhere in the binary — so none of Brigade's fifteen options reaches a hook.
   Nothing documented or observed puts a plugin's `bin/` on PATH. The card's "Plugin: Muse installs Claude Code
   plugins" is true only of a package with string or native-argv hooks and no options.
3. **The session identity is a UUID, and hooks get a cleared environment.** `MUSE_SESSION_ID` is a UUID (the
   stdin `session_id`), not a process id, so `config.ClaudePID`, the by-pid map and the watcher's pidfile names
   need the host-neutral key the Codex plan designed (its §4). Hooks start from a cleared environment: `HOME
   PATH USER LOGNAME TMPDIR TEMP TMP SHELL LANG LC_ALL TERM`, plus for plugin hooks `MUSE_PLUGIN_ID
   MUSE_PLUGIN_ROOT MUSE_PLUGIN_DATA_DIR` and the aliases `CLAUDE_PLUGIN_ROOT PLUGIN_ROOT CLAUDE_PLUGIN_DATA
   PLUGIN_DATA`, plus (1.4.4 on) `MUSE_SESSION_ID MUSE_THREAD_ID` "applied last so no other source can set
   them". No `XDG_*`, no `CLAUDE_CONFIG_DIR`, no proxy variables — so a Brigade hook under Muse resolves its
   state and config directories from `HOME` alone.
4. **Two routes the card did not know about soften the first point.** (a) Muse's `monitor` tool: the model arms a
   command, every stdout line it prints becomes an inbox item, and an idle TUI started a turn **0.2 s after the
   monitor's line (about 0.9 s after the outside write, behind a 1 s poll)**, with no keypress (khala, 1.4.3,
   `persistent: true`, `wake_delay_ms: 0`; the default batch delay is 120 s, the non-persistent timeout 5 min,
   the tool text names a 30-minute ceiling). That is an officially shipped, model-armed way for an outside
   process to wake a session — reachable only after the session's first user prompt, since arming needs a model
   turn (section 4). (b) The 1.4.8 helper cleanup the card worried about applies to **plugin** hooks only: "hooks
   configured in settings keep their existing background-helper behavior".

Smaller corrections: both release channels now serve **1.4.9-R6297.1**, the 1.4.8 build cannot be fetched, and
the launcher self-updates hourly, so a test runs the pinned binary directly and records its build. The 8,192-byte
message cap is in the binary (the model tool's schema: "at most 8 KiB"; a validator: "exceeds the 8,192-byte
plain-text limit"); that the CLI enforces the same number is inferred. Hook **stdout is capped at 16,384 bytes:
over it "the process tree is terminated" and no stream is parsed**. Brigade's prompt hook caps its own output at
`OutputCap` (10,000 code points, `hook/hook.go`) and skips a frame that would exceed it (`ErrNotInjected`),
so the exposure is multibyte text: 10,000 code points can be 40 KB. On Muse that output would be dropped whole,
and the seen-before-ack window P8-1 recorded would lose the message.

So the next step is still a live test, but a different one from the card's list, run in this order: the account's
gate, the ingress arm with the sender in the watcher's own position, the monitor arm, and only if one of them
can start a turn in an idle session without a human, the hooks, identity, survival and size arms. If neither
route can, the Codex ruling applies word for word (P8-1: "a mailbox, not a peer") and the card closes as won't do.

**Added 2026-10-16.** Caleb ruled that the human's front end need not be Meta's TUI: a session fully shared between
the human and Brigade but reached through another client is acceptable. That puts a fourth route in scope, **route
D** (section 4): a Brigade-controlled client that owns a `muse serve` host and submits Brigade's frames as turns. Arm
6 (5.11) probes it only after Arms 1 and 2 fail, and it changes the closing rule: the card closes as won't do only if
routes A, B **and** D all fail. A second sighting of the gate also arrived: a send from outside Muse aimed at a `muse
exec` run answered `external_agent_ingress_closed` on 1.3.0 (rtravellin/muse-code-plugin-cc README, 2026-09); that
plugin drives Muse as a headless worker from Claude Code and has no inbound path, so it is evidence, not a route.

## 2. The card, claim by claim

"Source" names where the 2026-10-15 pass read it: the dev.meta.ai pages were read through summarising fetches
(not downloaded), the changelog was downloaded raw, the SDK site and the third-party repositories were fetched
raw, the binary's strings were extracted locally.

| Card says | What the evidence says (2026-10-15) | Source |
| --- | --- | --- |
| SessionStart fires on startup, resume, clear, fork and (1.4.8) compact | Confirmed: `source` is one of `startup resume clear compact fork`. 1.4.8 made `clear`/`compact` *report* their source; a hook matching only `startup` no longer runs there. Two older measurements disagree on timing — no re-fire on resume (ftown, 1.3.0), fired at the first turn rather than at launch (amux, 1.0.3) — and nothing measures 1.4.8/1.4.9; the launch-time question decides whether a fresh session is registered before anyone types | SDK hook-events page; changelog 1.4.8; ftown, amux |
| UserPromptSubmit can return `additionalContext` | In JSON output only the nested `hookSpecificOutput.additionalContext` counts (a top-level key rejects the whole output; measured, khala 1.4.3). The SDK page — "checked against Muse Code 1.3.0" — says plain stdout not starting with `{`/`[` is added to context on SessionStart and UserPromptSubmit; that plain-text path, the one Brigade's hooks use, is **unmeasured on any version**, and ftown's 1.3.0 contract says only a JSON object acts | SDK hook-events; khala, Signet, ftown |
| SessionEnd only observes; unfinished hooks cancelled at shutdown | Confirmed and worse: on 1.4.3 unfinished SessionEnd hooks were cancelled within **0.25–0.5 s** (Signet). Brigade's `hook session-end` closes the session through the adapter; if 1.4.9 measures the same, the lease is the close path | changelog 1.4.0; Signet PR 2075 |
| `MUSE_SESSION_ID` / `MUSE_THREAD_ID` documented, as strong as `CLAUDE_PID` | Confirmed as documented (1.4.4). The trust statement is right (P8-1 ruling: the host's approval policy is the gate, the marker is parity with Claude). Missing from the card: it is a UUID, not a pid; before 1.4.4 hooks got no `MUSE_SESSION_ID`, and 1.4.4 also gave it to background monitors | changelog 1.4.4; khala #1143 |
| `muse session-message send --target <uuid-or-name> < body` is in the binary | Confirmed verbatim in 1.4.9: `send --target <session-uuid-or-name> [--in-reply-to <reply-token>] [--display-context <json>] [--json] < body`; no `--from`, `--deliver`, `--wake` or intent flag, so the CLI's delivery and wake policies and its "solicitation" accounting are unknown (the model tool defaults are `steer_active_turn` and `wake_when_idle`) | binary strings |
| Muse writes that command into messages it sends to Claude Code sessions | Confirmed in part: 1.4.0 "Messages sent to Claude Code include the command for replying to the source Muse session"; the binary holds a frame with `<reply>Reply with muse session-message send --target "" --in-reply-to "</reply>`. Which frame reaches Claude Code, and whether that reply route admits a non-Muse sender, is unmeasured | changelog 1.4.0; binary strings |
| Since 1.4.2 a message wakes an idle recipient by default | Confirmed ("Approved peer notifications wake idle agents by default") — for **admitted** messages; an admitted message can also be parked for the next turn | changelog 1.4.2, 1.4.0 |
| Unverified senders get an Allow once / Allow for this session card | The card's choices exist (`allow_once reject_once allow_connected block_connected`, session-scoped ones "until either session restarts"). An outside sender never reached the card on 1.4.3: refused by the gate first, then at admission. The vocabulary also has `authenticated_controller` and `authenticated_external_agent` — a no-click path whose authentication is nowhere documented. `--display-context '{}'` and identity-shaped JSON were already tried on 1.4.3: same refusal, or `invalid_display_context` | binary strings; khala |
| "Muse installs Claude Code plugins and fills in `${CLAUDE_PLUGIN_ROOT}`" | Muse substitutes nothing in hook strings; the **shell** expands `${CLAUDE_PLUGIN_ROOT}` from the environment Muse sets (under fish a `${VAR}` failed with exit 127, agentchute 1.4.7). Exec-form `args` unsupported (D99). Hooks stay inert until `muse plugins approve <id>` (non-interactive with `--json`, stdin `/dev/null`), which on 1.4.3 rewrote the user's `settings.json`. Muse refuses a plugin that contains symlinks or that binds two hooks to one source file. Only one project has even validated a Claude-format plugin under Muse (khala: `manifest_family claude-compatible`, hooks `supported`, `asyncRewake` unsupported); none installed and ran one | SDK compatibility page; binary strings; khala, ftown, amux |
| "Hook events it doesn't know are skipped with a warning" | Confirmed (1.4.5), and so are unknown keys (`asyncRewake`, `once`) | changelog |
| Open question 2: does the watcher survive the 1.4.8 cleanup? | Still open, narrowed: plugin hooks only; settings-level hooks exempt; the stated purpose is to release the plugin's installed files; the mechanism (process group, session, cgroup, descendant tracking) is undocumented, so whether Brigade's `Setsid` child escapes is unknown. 1.4.6 also kills a running hook's processes when the terminal closes | changelog 1.4.8, 1.4.6 |
| Open question 3: 8 KiB vs Brigade's 16 KiB | Confirmed 8,192 bytes for the tool and the validator; CLI enforcement inferred. Add: hook stdout 16,384-byte hard cap, process tree killed | binary strings; SDK hook-events |
| Open question 4: `args`, `userConfig`, `bin/` on PATH | Answered from the binary and the SDK site: `args` not implemented; `userConfig` inert; `bin/` on PATH unobserved anywhere | binary strings; SDK compatibility page |
| Open question 5: the sandbox | The permissions page (summary read): writes only to the workspace and a temp dir, `proxy-only` network prompting per new host; the extending page: hooks run outside the sandbox. Add, measured: on Linux the **monitor's** command ran in bwrap with a private PID namespace (host pids invisible; khala #1143, 1.4.3) — not yet measured for the Bash tool's children; since 0.2.1 sandboxed commands cannot reach host Unix-domain sockets | docs; khala #1143; changelog 0.2.1 |
| Open question 6: interactive sessions only; headless excluded | The session-messaging page says so; the 1.4.0 changelog says `receiver_limits` "bound the peer-message receiver in the TUI and `muse exec`", and the binary has a `target_surface_excluded` code. Whether a `muse exec` run is a receiver is unresolved | docs; changelog 1.4.0; binary strings |
| "Run a live test … in an isolated home folder" | `XDG_CONFIG_HOME` moves settings, trust and `auth.json`; `XDG_DATA_HOME` moves sessions, plugins and the feature-config. The session-name registry (`session-name-authority/session-names.db`) is written under the **real** home regardless (khala), probably from the passwd entry — so names are shared with the owner's own sessions and are not a safe target. By default interactive Muse loads `~/.claude` skills and personal rules and sends them to Meta with every prompt; `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on` (interactive) or `--no-foreign-personal-context` (exec only) stops it, and where that loader resolves `~/.claude` from is unmeasured | SDK host-operations page; launcher script; khala; rtravellin, firstmate |
| "No free tier was found" | Confirmed from the subscriptions and auth pages (summary read): tiers Everyday/High/Power/Maximum with no prices shown, or a pay-as-you-go `META_API_KEY`; the session-messaging page: availability "depends on the session-messaging rollout gate" | docs |

## 3. What Brigade needs from a host, and where Muse stands before any run

The four capabilities the Claude Code host supplies (plan 6.5–6.8), with the verdict the evidence supports today.
"Measured" means a third party ran it on the stated version; nothing here was run by us.

| Capability | Claude Code supplies | Muse status | Verdict |
| --- | --- | --- | --- |
| Lifecycle hooks: register, heartbeat, close | SessionStart / UserPromptSubmit / SessionEnd, exec-form, options as `CLAUDE_PLUGIN_OPTION_*` | The three events exist; exec-form `args` unsupported; options inert; SessionEnd budget ≈ 0.3 s (1.4.3); 600 s default timeout; 16,384-byte stdout cap | Needs a Muse package (string or native-argv hooks) and a Brigade-owned options source |
| Session identity in hooks and in the model's shell | `CLAUDE_PID` (int) in both; by-pid map | `session_id` on stdin; `MUSE_SESSION_ID` (uuid) in hooks, shell commands and monitors from 1.4.4; the model's shell sandboxed | Needs the host-neutral key; trust parity with Claude (P8-1) |
| Context delivery into a turn | hook stdout / `additionalContext` | Documented for plain stdout (1.3.0 page), measured only for the JSON form (developer-role block, khala) | **Undecided — Arm 3** |
| **Wake an idle session from outside** | inbox socket (`socketpost`) | Route A `session-message send` from a non-Muse process: **refused on 1.4.3 by a per-account gate**, unmeasured since, never measured from the watcher's position. Route B `monitor`: measured once on 1.4.3, model-armed, sandboxed, 120 s default batching, 5-min/30-min limits. Route C hooks: cannot start a turn while idle (same as Codex). Route D `turn/start` on a Brigade-owned `muse serve` host: documented (`ifBusy` defaults to `queue`; one client per host; writer lease per session), unmeasured, and the human's front end is the client, not Meta's TUI | **Undecided — Arms 1, 2 and 6** |

Supporting facts that shape the design: session messaging is same-user, same-machine, interactive only, macOS and
Linux (Brigade's watcher is on the same machine as its session, so that is enough); `--no-session-log` disables
it; session logs are `$XDG_DATA_HOME/muse/sessions/YYYY/MM/DD/<id>/session.jsonl` (the scorer's input, since hook
`transcript_path` is `null` in every observation); `/export trajectory` and `muse export --session <uuid>` dump
the same log; the settings file needs `schema_version: 1` or Muse refuses to start; an idle session makes model
calls nobody sent (away recaps after two minutes away, prompt hints, automatic titles — settings keys
`away_recap_enabled`, `prompt_hint_enabled`, `auto_session_title_enabled` exist in the binary), which a scorer
must not count as a wake.

## 4. The wake routes, honestly

**Route A — `muse session-message send` from the watcher.** The product-grade route if it works: the watcher spawns
`muse` with an argv array (a third exec seam beside `hook/spawn.go` and `adapterclient/spawn.go`, so a forbidigo
rule change), body on stdin, ≤ 8,192 bytes, `--json` for the receipt. Its gate is the account's
`external_agent_ingress` feature flag plus admission. The sender that matters is **the watcher's own position**: a
detached child of the receiving session's SessionStart hook, inheriting that session's `MUSE_SESSION_ID`, sending
to that session — a self-send in Muse's terms (`self_hop_limit` is a receiver-limits key). A plain outside shell is
a supporting row only. Three sub-outcomes to tell apart: (A1) admitted and woken with no card — the only outcome
that satisfies "no human in the loop" as Brigade defines it; (A2) admitted after one "Allow for this session"
click that then covers **later sends from fresh sender processes** — if the grant is per sender generation, a
one-shot CLI sender gets a card per message, which is not A2 but A3; (A3) refused, or a card per message. Two
more conditions: a pass counts only in the receiving session's **default launch environment** — a result that
needs `MUSE_EXPERIMENTAL_*` or an invented `--display-context` is CONDITIONAL and opens nothing, because Brigade
cannot set the environment a user launches `muse` with; and the CLI has no intent flag, so if every send counts
as a "solicitation" the route goes silent after three unanswered messages to one peer (a Brigade receiver answers
through `brigade`, never through `muse session-message`) — the fourth and fifth sends must wake too. The platform
is a variable: Linux cannot produce the `verified_macos_cli` identity, so a Linux A3 does not close the card until
steps 1–3 of Arm 1 have run on macOS.

**Route B — the monitor tool.** The model arms `monitor` with a command Brigade supplies (`brigade watch
--stdout-frames`, a sink that prints one line per frame); Muse queues each stdout line as an inbox item and wakes the
idle session. What makes it weaker than the socket: the model must arm it, which needs a model turn, which only a
user prompt starts — so every session is a mailbox until its user first types, and perhaps again after `/clear`,
`/compact` or resume; arming may raise a tool-approval decision (the approval judge, on by default, or the user) —
not under `--yolo`, the configuration Brigade's customers run, where the only question is whether the model arms it
from the skill text; the monitor runs **inside the sandbox** (bwrap, private pid namespace measured for it, no host
Unix sockets, network approval on first connect to the backend host — a human action the decision rule must count as
A2 does); a non-persistent monitor dies after 5 min and the tool text gives a 30-min ceiling unless persistent;
batching is 120 s unless `wake_delay_ms: 0`; monitor-woken turns fired Stop but not UserPromptSubmit on 1.4.3 (1.4.9
unmeasured — Brigade's heartbeat hook would not run on them); and it inverts Brigade's process model: the watcher
becomes a sandboxed child of the session, so the adapter child, credential refresh and state writes must work under
bwrap's write scope without putting any Brigade credential or state file under the workspace.


**Route C — hooks.** No hook starts a turn while a session is idle. Same as Codex; it is why P8-1 closed.

**Route D — a client that owns the host (added 2026-10-16, after Caleb ruled that the human's front end need not be
Meta's TUI).** Muse ships its whole harness — model, tools, sandbox, approvals, hooks, plugins, skills, session store
— behind `muse serve`: JSON-RPC 2.0 over stdio ("MSP"), spawned by its one client, "the stdio wire is the only public
protocol surface", on by default since the SDK tier's variable became an off-switch. A client Brigade controls owns
the host; the human prompts through it; Brigade's watcher hands it frames over a local inlet; the client submits each
as `turn/start` (`ifBusy: "queue"`, the wire default: queued input launches "when the current turn reaches its
terminal state" and survives host restarts; `steer` injects into the running turn against an `expectedTurnId`;
`replace` exists, undescribed). Approvals follow the user's choice, as everywhere in Brigade (allow-all by default,
options to restrict): under the customer configuration the host runs `--yolo` and no approval frame ever appears;
under a restricted profile `approval/requested` reaches the client, `approval/decide` answers with a rendered
`choiceId`, "approvals do not expire in v1" (a pending one "parks its turn indefinitely, blocks idle unload, and
survives restarts"), and a denial ends the turn `completed`. Identity needs no hook: the session id is the
`session/start` result and `MUSE_SESSION_ID` reaches the model's shell (1.4.4); registration, heartbeat and close are
the client's own calls — so D99, the inert `userConfig` and the cleared hook environment stop mattering. What it
costs, honestly: (1) one live client per host and one writer lease per session, so Meta's TUI cannot be open on the
same session (`sessionInUse`; the SDK reports exit 4 as "another live client holds the lease"), and whatever the
human wants from the TUI — rewind, voice, side chats — the client must provide or forgo; (2) served sessions start in
the Auto-review profile with the host's own reviewer (changelog 1.2.1, 1.4.6), so the client has to pass the user's
chosen posture through — `--yolo` on the host for the default, or `:ask-me` ("select, never create") plus an approval
mode under the sealed ceilings for a restricted team — which Arm 6 row 2 proves both ways; (3) the sandbox applies to
every session the host loads and "no protocol method can change" it, so the client sets it at spawn from the user's
options (off under `--yolo`), and under the default profile the model's shell has only the reach route B's monitor
measured; (4) stability: `turn/start`, `turn/steer`, `approval/*` and `userInput/*` carry no experimental label,
`peer/*` and `admission/*` do, "the wire evolves by addition", a fingerprint mismatch "is a warning condition, not an
error", and an older client against a newer host is the supported direction; (5) whether `serve` draws on the
subscription or needs API balance is not stated. Against P8-1 it is not a mailbox: the model is woken on delivery
with no human. What it is not is a wake of a session the human runs in Meta's product, which is the trade Caleb
accepted. Existing front ends and the build choice are in section 6; the probe is Arm 6 (5.11).


## 5. The live test (P0)

Modelled on `scripts/experiments/codex-participation/` and `scripts/proof-idle-wake.sh`, written against the
five "highs" the P8-1 verifier found in the Codex run (5.9) and the review of section 9. It spends a real Muse
account; it never runs in CI (D31). Everything below is a design; no arm has been run.

### 5.1 Prerequisites and isolation

- **Account.** A Meta account with a subscription or API balance. The run signs in **inside the test root** —
  a device login, or `muse auth set --api-key-stdin` from a key file outside the repository — and signs out at
  teardown (`muse logout`). Never a copy of the real `~/.config/muse/auth.json`: it is an OAuth store the CLI
  writes back to, and a refresh inside a copy can rotate the token and log the real install out.
- **The binary, pinned by hand.** Fetch `https://api.meta.ai/muse-code/channels/muse-stable` → `manifest_url` →
  the `x86_linux` or `aarch64_macos` artifact; verify `size` and `sha256` against the manifest; keep manifest and
  checksum in the evidence; run the binary directly (never the self-updating launcher); record its sha256 at
  start and at teardown and the version each `session.jsonl` reports (expect `1.4.9-R6297.1` today).
- **Machines.** Linux (this machine: bwrap present; `tmux` absent — install it; `script`, `jq`, `pgrep`, `python3`
  present) **and** macOS for Arm 1 if Linux refuses (section 4). The sandbox needs a working bubblewrap on
  Linux; Muse bundles one.
- **The environment: an allowlist, not a strip.** A third-party binary that sends context to a remote model gets
  `env -i` plus exactly `HOME PATH USER LOGNAME SHELL TERM LANG TMPDIR`, the four `XDG_*_HOME`, and — in the one
  arm that tests it — a single `MUSE_EXPERIMENTAL_*` variable. The driver diffs the launch environment's names
  against the allowlist and refuses to start on any extra (an inherited `META_API_KEY` would silently replace
  the account whose gates the run reads). This is stricter than the Claude proofs' strip-by-prefix, for the
  reason stated.
- **The root**, outside the repository (`$XDG_RUNTIME_DIR/brigade-muse-<stamp>/` or `~/.cache/`): `home/`, with
  `XDG_CONFIG_HOME=home/.config`, `XDG_DATA_HOME=home/.local/share`, `XDG_STATE_HOME=home/.local/state` set to
  exactly the `HOME`-derived defaults, so a hook (which gets no `XDG_*`), the model's shell and the driver all
  resolve the same directories; `home/.config/muse/settings.json` with `schema_version: 1`, the three idle
  model-call settings off, and nothing else; one empty workspace per session (`--trust-workspace` for them).
  The real `~/.config/muse/settings.json`, if any, is hashed before and after the run and must be unchanged.
- **Privacy pre-flight.** Before the first prompt that reaches Meta's model, a session on the echo provider in
  the test environment runs `muse skills list --source all --json`, and its first `session.jsonl` is read: no
  skill or rule name from the owner's real `~/.claude` may appear (names compared hashed). The kill switch
  `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on` is set and required, not belt-and-braces: Muse resolves at
  least one path from the passwd entry rather than `HOME`, so an empty test home proves nothing by itself.
- **Session targeting and the wrong-session gate.** Sessions are addressed by **UUID**, read from the probe hook's
  stdin `session_id` and matched to a `session.jsonl` under the test root's data dir — never by name (names live
  in the shared registry under the real home, can be shared, quarantined or ambiguous). Before any send, the
  driver stops unless `pgrep -x muse` lists only processes it started and `muse session-message list --json`
  returns a subset of its own sessions; otherwise the arm is void.
- **Keys.** Every keystroke to any pane goes through one function that logs pane, millisecond timestamp, purpose
  and byte count; the submit method is pinned (tmux `paste-buffer -p`, then the enhanced-Enter sequence that
  agentchute measured; a bare Enter does not submit); the priming user record with its readiness nonce must
  appear in `session.jsonl` before a hold starts; no tmux client is attached during a hold; the scorer voids any
  wake whose window contains a key event to that pane, with a mutation row for it.
- **Two configurations per arm (owner ruling 2026-10-16).** Brigade is allow-all by default with options to restrict,
  and its customers run their harnesses with `--yolo`; so every arm that reaches a model runs first under the
  **customer configuration** (`--yolo`: approvals off, sandbox off, workspace trusted) and then under **Muse's
  default** (sandbox on, approval judge on, no `--yolo`), and the report labels every row with its configuration. The
  evidence rule changes with it: under `--yolo` the model's shell can reach every file, so a private-store or
  `session.jsonl` fact counts only if the scorer finds no tool-call record in that session whose command names an
  evidence path (the driver hashes `session.jsonl` and the private store right after each wake and the scorer
  compares); under the default profile the old rule holds — a session that granted an outside-sandbox or file-write
  approval is void for every private-store fact.

- Nothing joins a Brigade team, contacts a teammate or touches the hosted backend. Arms that need a Brigade
  session use the fs adapter or the local Supabase stack, as the proofs do.

### 5.2 Order

1. Sign in; record the feature-config: every gate **name → boolean**, the files' sha256 at start and teardown.
2. `muse session-message list --json` with no sessions of the run open.
3. Arm 0 (5.3), on the echo provider where the plugin commands allow it.
4. Install and approve the probe package (5.6's fixture) — Arm 1 needs its SessionStart hook.
5. Arm 1 (5.4), then Arm 2 (5.5). **If both fail, run Arm 6 (5.11) before anything else**; if it fails too, stop and
   report: the remaining arms only matter if outcome 2 or outcome 3 is reachable.
6. Arms 3 and 4 (5.6, 5.7), **on the TUI routes only** — route D needs no hooks, its client does registration,
   heartbeat and close itself, and 5.11 carries its own identity, survival and size rows: hook mechanics, timeouts,
   the SessionEnd budget, the 16 KiB cap, survival and identity run on `--provider echo` where the row needs no real
   model; delivery and CONTENT rows need the real provider, and the scorer refuses to score them from an echo run.
7. Arm 5 (5.8), optional, in its own isolation.

### 5.3 Arm 0 — no model turns

`muse plugins validate --json` on a `git archive` copy of `plugin/` inside the test root, and on each of the three
probe packages of 5.6. Records how Muse classifies Brigade's package (manifest family, each hook's status — expect
`unsupported` for all three, D99 — the `userConfig` row, the skills) and catches a fixture defect (a shared hook
source file, a symlink) before any session is spent. On 1.3.0 the plugin commands needed a fetched feature-config
("not available in this build" with an empty data home); if so this runs after sign-in.

### 5.4 Arm 1 — the ingress gate (decisive)

Each variant gets a **fresh receiver** (`b1`, `b2`, …: a named tmux pane, one priming prompt whose answer carries
a readiness nonce, then idle), because the solicitation limit and a dedupe window would otherwise silence later
sends. Every body is unique and carries a split token (two halves; the joined form appears in no prompt, hook
output or environment, and the driver refuses to launch if it does). Each receiver's priming prompt, in the
user's own words, tells the model that test messages from other sessions carry two halves and that the answer is
the halves joined — except one receiver per flag set left unprimed, whose row is "a woken model acts on a peer
request without prior user authorization" (khala's woken model declined the request).

Two things are scored for every send, separately: **WAKE** — the receipt says delivered; the receiver's
`session.jsonl` has a `peer_message.admission.settled` record whose inbox item's body hash equals this send's;
a main-conversation turn (never a recap, hint or title call) starts after it within 90 s; no key event reached
that pane in the window; and **CONTENT** — the joined token in that turn's assistant text. The card boolean comes
from an admission-requested or pending record in the log plus a pane-text match on "Allow once", recorded at run
time, not from a hashed capture. A send with WAKE false is a mailbox at best, whatever the receipt says.

1. **Gate read.** `muse session-message list --json` from a plain shell. Error code, or peer rows with
   `canReceive`, is the first finding.
2. **Watcher-position send.** The probe's SessionStart hook in `b1` starts a helper in Brigade's spawn shape
   (`Setsid`, stdio to a log, released, the hook's environment including `MUSE_SESSION_ID`/`MUSE_THREAD_ID`);
   on the driver's signal it runs `muse session-message send --target <b1 uuid> --json` with a unique body. The
   receipt's code (`external_agent_ingress_closed`, `self_hop_limit_reached`, `causal_metadata_invalid`,
   `containment_limited`, admitted…), WAKE and CONTENT are the finding. Repeat from a helper started by a
   settings-level hook (`b2`).
3. **Plain-shell send** to `b3`: the supporting row (khala's 1.4.3 shape, re-measured on this build).
4. **Flag variants**, each with its own receiver, its own positive control and its own null control under
   identical flags: `MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=1` on the sender only; on the receiver's launch
   environment and the sender. `--display-context '{}'` and the two identity shapes khala saw rejected are
   regression rows. These are CONDITIONAL rows by construction (section 4).
5. **Positive control** per flag set: `a`'s model sends the native message. Scored from **`a`'s own log** — the
   `send_session_message` tool call, its delivered receipt, and the receiver's admission record for that body
   hash. A control whose sender record is missing is VOID, never "did not wake". The control's validity gate is
   WAKE, not CONTENT. Run on its own receiver, so no reply chain overlaps a cold-send window.
6. **Null control** per flag set and per hold length: a fresh receiver, primed, 30 s settle, then an observation
   at least as long as the longest hold it vouches for (≥ 120 s beside the 120 s hold), nothing sent, no
   main-conversation turn.
7. **If any variant shows a card**: click "Allow for this session" once (a logged human action), then send
   **twice more from fresh sender processes** of the same shape. A2 holds only if both later sends are admitted
   with no card; record each admission cause and the grant evidence. Then a long-lived stand-in for the watcher:
   one parent process sends, is killed, is restarted in a new `setsid` session, sends again.
8. **Sustained sends** whenever any variant is admitted: one sender identity sends ≥ 5 distinct bodies to one
   idle receiver that never replies through Muse, each spaced ≥ 90 s apart; each must meet WAKE; the receipt code
   of any failure is recorded. The 4th and 5th sends waking is part of route A's pass.
9. **Platform.** If every default-environment variant on Linux is A3, steps 1–3, 5 and 6 run again on macOS with
   the `aarch64_macos` artifact before 5.10 may close the card; otherwise the verdict is stated as Linux-only.

Pass for route A = step 2 (the watcher's position) reaches A1, or A2 as defined in step 7, in the default launch
environment, with step 8 green — on the platform the verdict names.

### 5.5 Arm 2 — the monitor route

The arming instruction is the **actual Brigade skill text** (a draft `plugin/skills/team-messaging` paragraph for
Muse), in ≥ 3 fresh sessions under `--yolo` (the customer configuration, where arming needs no decision and the row
measures only whether the model arms it from the skill text) and ≥ 3 under the default profile and approval judge,
each recording whether arming was auto-allowed, judged, or asked of the user. The monitor's command is
`<fixture>/watch.sh` (`persistent: true, wake_delay_ms: 0, show_lines: true`), which tails a trigger file; from
outside, the driver appends a line with a split token. Per session: wake latency from the append to the first
main-conversation record; whether UserPromptSubmit fired on the woken turn (the probe's events file); holds of 5 s,
120 s and **≥ 35 min** (past the 30-min ceiling the tool text names; a 2-hour point if budget allows), each with a
null control of the same length with a monitor armed and nothing appended; `/compact`, `/clear` and `muse resume`
between wakes, recording whether the monitor needed re-arming; lines of 2, 8 and 16 KiB, recording whether each
arrives whole. Inside the monitor's shell the fixture records environment names (`MUSE_SESSION_ID` expected present
since 1.4.4), `/proc/1/comm`, the pid namespace, and whether the local stack's port is reachable. Then the real
thing: `brigade watch` (fs adapter, then the local stack) as the monitor command, reading the credential store at its
normal path — do reads and refresh writes outside the workspace succeed, and does a network card appear?


### 5.6 Arm 3 — hooks, plugin and delivery (the Codex probe, ported)

Three fixture packages under `scripts/experiments/muse-participation/fixture/`: (i) a **native**
`.muse-plugin/plugin.json` — argv-array hooks for `SessionStart`, `UserPromptSubmit`, `PreCompact`,
`PostCompact`, `SubagentStart`, `SubagentStop`, `SessionEnd`, each with explicit `timeoutMs` and **its own
wrapper file** importing one shared probe module (Muse refuses two hooks on one source file); (ii) a
Claude-format manifest with **string** hook commands (`"${CLAUDE_PLUGIN_ROOT}/scripts/probe-<event>.py"`), the
path a Brigade compatibility package would use; (iii) Brigade's exec-form shape, to record what Muse does with
`args` (skip, run bare, refuse). Installed with `muse plugins install <dir> --scope user --json`, approved with
`muse plugins approve <id> --json </dev/null`; the test root's `settings.json` is hashed before and after approve
(it was rewritten on 1.4.3), and the settings-level hooks block of 5.7 is written after approve.

The probe records to `MUSE_PLUGIN_DATA_DIR/events.jsonl` (the private store): hashed `session_id`,
`source`/`reason`, the field-presence map of stdin, environment **names** (never values), whether
`MUSE_SESSION_ID` equals the stdin `session_id` and whether `MUSE_THREAD_ID` differs in a subagent, the plugin
variables' presence, `$0` (which shell runs string hooks). Measured, one fresh session each unless stated: a
session launched and **left untouched for 30 s before any key**, to see whether SessionStart fires at launch or
at the first turn; the sources on `/clear`, `/compact`, `/fork`, `muse resume`; the SessionEnd `reason` values
and budget (probe sleeps of 0.2, 0.5, 2 s on separate runs); hook timeouts (a 3 s probe under `timeoutMs:
1000`); stdout handling for **Brigade's actual shapes** — a `Brigade: …` line, a poll preamble plus a
`<brigade-message` frame, the JSON form — and the caps: 16,384 bytes of ASCII accepted, 16,385 bytes
`output_too_large`, and ~10,000 code points of three-byte characters (Brigade's own cap, over Muse's).
**Delivery**: UserPromptSubmit prints the integration-owned instruction with the anchor and the two halves; the
row passes only on the **joined token** in the model's own assistant records; a null runs in a fresh session with
the same prompt and the probe installed but not approved (hooks inert), where the token must be absent; mutation
rows for halves-only text and for the token appearing only in a hook record. The halves are minted by the probe
into its private store per run and per session and handed to the scorer from there — never via the launch
environment, which every child inherits (P8-1 high 3).

### 5.7 Arm 4 — survival, identity, sizes

- **Survival.** From the plugin SessionStart hook: a **positive control** first — a plain `helper &` with no
  detachment, which must stop when the hook finishes; if it survives, every survival row is VOID, not PASS.
  Then Brigade's way — the brigade binary from the plugin cache through the shipped spawn path
  (`internal/harness/hook/spawn.go`), and again from a copy outside the cache — `setsid -f`, and a double fork;
  the same from a settings-level hook in the test root's `settings.json` (the exempt path); and a native
  `async: true` hook that never exits under a large `timeoutMs`. Checkpoints: after the hook returns, after the
  next prompt, after `/clear`, after `muse plugins update` and a reinstall, after the TUI exits (1.4.6).
- **Identity, the E0-7 way.** The hook writes a per-session nonce keyed by sha256(`session_id`) into the private
  store. In **two concurrent sessions**, a fixture command run from each Bash tool looks the nonce up by its own
  `MUSE_SESSION_ID` and prints a count and a digest, never `env`; each session's tool-result record must carry its
  own nonce and never the other's; a poison run with the other session's id must return the other's. The Codex
  four with all four labels, "nested" defined as a grandchild through a second shell and a `setsid` hop —
  capability rows, not a gate (P8-1). Also recorded: whether the sandboxed shell can read the store,
  `/proc/1/comm`, `kill -0` on the helper's pid, a write to the resolved state dir, a connect to the local stack's
  port and to one HTTPS host (does a network card appear?).
- **Sizes.** If route A is open: an 8,192-byte body delivered intact (the at-limit control), 8,193 refused with
  the limit's code; a 2,000-byte body with Brigade's frame overhead delivered intact.
- **`muse exec` as receiver** (cheap, last): a `muse exec` run holding a long tool call — listed by
  `session-message list`? `canReceive`? A send → `target_surface_excluded` or a wake. `muse exec` may drive
  hook-mechanics proofs later; wake, idle, null and delivery proofs stay on the TUI unless an equivalence arm
  runs the same wake on both surfaces and they agree (P8-1 high 5).

### 5.8 Arm 5 — the reply route (optional; cannot change route A's verdict)

Muse reaches Claude Code through a transport that reads Claude Code's session registry and key file under
`CLAUDE_CONFIG_DIR`. So this arm runs **only** with a dedicated, otherwise empty `CLAUDE_CONFIG_DIR` inside the
test root, with its own login, passed to both the `claude -p` session (stdin held open as in the idle-wake proof,
hooks off) and Muse's environment, behind a pre-flight that `session-message list` shows exactly that one Claude
session. It knowingly lets Muse read that throwaway session's key file; never the real `~/.claude`. `a`'s model
messages the Claude session; the frame Claude receives (its `isMeta` user record) is hashed and its reply form
recorded; the reply command is run from a plain shell; whether it is admitted, and with which cause, is the
finding.

### 5.9 Scoring and the checklist from P8-1

A scorer like `analyze.py`, with `analyze_test.py` mutation rows, and these rules — each one a thing the Codex
verifier caught:

1. No fail-open: an arm whose label is absent scores FAIL. NOT RUN is allowed only when a named precondition row
   recorded FAIL; anything else absent is FAIL, with a mutation row for it.
2. Facts come from the probe's private store and from `session.jsonl`, never from command output the model can
   write; the private store counts only in sessions that granted no outside-sandbox or file-write approval.
3. The delivery and wake tokens never ride the launch environment; they are minted per run and per session and
   kept in the private store; only the joined form scores.
4. Every number in the report is produced by the scorer from per-run records committed as evidence — including
   relative millisecond timestamps for each send, append, key event and first record, so every latency can be
   recomputed offline; no hand-written aggregates.
5. Every flag and environment variable **each session** ran with is in the report: profile, approval mode,
   sandbox flags, `MUSE_EXPERIMENTAL_*`, the three idle-call settings, the account's gate names and booleans.
6. The scorer counts only main-conversation records and drops purpose-labelled side calls (recaps, hints,
   titles); a mutation row plants a recap record after a send and must score FAIL.

Plus the house rules: null and positive controls under the same flags as the arm they vouch for; the model's own
text scored, never an echo; raw `session.jsonl`, pane captures and the auth state stay outside the repository;
the committed evidence is hashes, field presence, event names, booleans and relative timestamps.

### 5.10 Deliverables and the decision

- `scripts/experiments/muse-participation/` (fixture packages, `run.sh` driver, `analyze.py`, tests, README),
  `docs/experiments/muse-participation.md` and `-evidence.json`, a row in `docs/experiments/README.md`.
- A capability table with PASS / FAIL / VOID / CONDITIONAL / NOT RUN (rule 1) per row above, the Muse build, the
  OS per row, and the account's gates — re-checked by an independent adversarial verifier before any
  implementation row opens, as P8-1 required and did not get in time.
- **Outcome 2 (idle wake without a human)** is PASS if: route A reaches A1, or A2 as 5.4 step 7 defines it with
  Rjae's acceptance, in the default launch environment, from the watcher's position, with the sustained-sends row
  green; **or** route B wakes within a few seconds, survives the ≥ 35-min hold and `/compact`, is armed by the skill
  text without a human decision under `--yolo` (the default-profile judge's decision is a capability row), puts no
  Brigade credential or state file under the workspace, and reaches the backend — with the facts that it is reachable
  only after the first user prompt and that a network card is a one-time human action put to Rjae for acceptance, as
  A2 is. Otherwise card 88 closes as the Codex card did. **Outcome 1** (prompt-poll, next-prompt delivery) is
  possible on Muse with a string-hook package and is recorded, but P8-1 already ruled that a mailbox is not a peer.


**Outcome 3 (route D, added 2026-10-16)** is PASS if, in Arm 6: rows 1–4 and 7 pass — under `--yolo` the turn runs
with no approval frame, and under the restricted profile the gate is accepted at `session/start` and exercised on a
real tool call (both halves of row 2); idle wake through the inlet within a few seconds at 5 s, 120 s and ≥ 35 min
with no human action and the next turn on the stored login; queued injection during a busy turn running after it,
with the parked-approval behaviour recorded either way; a clean close that lets the TUI open the session and a held
lease that refuses a second host. Row 6 decides the identity design, not the outcome; rows 5, 8, 9 and 10 are
capability rows. Outcome 3 is CONDITIONAL, not PASS, if the restricted profile cannot be selected at all (then
Brigade's restrict options have nothing to bind to), or if the ≥ 35-min hold needs a keepalive turn (a model call
nobody sent). A one-time human action at the client's start (workspace trust, a network card) is an install step of
Brigade's own client, which is a different thing from A2's approval card; whether it still needs the acceptance A2
needs is Rjae's call and is flagged in the report. **The decision:** outcome 2 PASS → routes A/B and section 6's TUI
shape. Outcome 2 FAIL and outcome 3 PASS → route D and section 6's owned-host shape, as a new card for the client
(the build is a client, not a plugin), with card 88 closing on that card. Both FAIL → the card closes as the Codex
card did.


### 5.11 Arm 6 — route D, the owned-host probe (added 2026-10-16; runs only after Arms 1 and 2 fail)

One day, no plugin package, no TUI on the session. A throwaway driver — python3 standard library speaking raw
JSON-RPC 2.0 over the host's stdio, since Meta says the wire guide plus the schema export "are your implementation
surface" and raw is what a Go client would speak; the official `@muse-code/sdk` 1.4.10 is the comparison if raw
stalls — spawns the pinned binary as `muse serve` from the 5.1 root with the 5.1 environment, logs every frame both
ways with millisecond timestamps, and exposes one Unix socket (mode 0600, under `XDG_RUNTIME_DIR`, never under the
workspace) as the inlet. Every row that reaches a model runs under `--yolo` first and Muse's default second (5.1);
`--disable-request-user-input` is never passed. Rows, each PASS/FAIL under 5.9's rules (facts from `session.jsonl`
and the driver's frame log; the model's own text scored for CONTENT; null controls of the same length):


1. **Handshake.** `initialize` with `clientInfo.name` matching `[a-z0-9_]+` (the quickstart's `my-app` does not —
   record whether a hyphen is refused), `userInputDialogs: true`, `experimentalApi` off except in row 10; record
   `serverInfo.version`, `schema.fingerprint`, `museHome` (must be under the test root), `sandboxAvailability`,
   `grantedCapabilities`; then `initialized`.
2. **Approval posture, both ways.** First the customer configuration: a host spawned with `--yolo`, a shell tool
   call, and the record that no `approval/requested` frame appeared and the call ran. Then the restricted profile:
   `session/start` with `permissionProfile: ":ask-me"` and each approval mode in turn (`promptUnmatched`,
   `onRequest`); record which the host accepts under default ceilings. Served sessions otherwise start in Auto-review
   with the host's own reviewer (changelog 1.2.1, 1.4.6), which is not a human gate. A shell tool call must then
   produce `approval/requested` at the client; `approval/decide` with a rendered `choiceId` (never hardcoded) must
   run it, a denial must end the turn `completed`, and a protected write must ask. Record `hook/list`: do
   settings-level and project hooks appear, and do they run as `hookRun` items in a served session?

3. **WAKE, idle.** The priming prompt, then holds of 5 s, 120 s and ≥ 35 min with the client subscribed and no turn
   running; at the end of each, the driver writes a frame with the minted token to the inlet, which submits
   `turn/start` (`ifBusy: "queue"`). Latency from the inlet write to `turn/started` and to the first
   main-conversation model record; CONTENT from the joined token in the model's text; a null hold of each length with
   no inlet write. The ≥ 35-min hold doubles as the survival row: the next turn must run on the stored login with no
   keepalive (nothing documented keeps a host alive or kills it when idle; a subscribed session must not unload —
   `session/closed` with reason `idle` is a FAIL).
4. **WAKE, busy.** During a long shell call (`sleep 90`, approved), an inlet frame with `queue` must run after the
   turn (`disposition: "queued"`, a pre-minted `turnId`); one with `steer` (`expectedTurnId` read from
   `turn/started`) must reach the model mid-turn; `replace` recorded. Then the same three while the turn is **parked
   on an approval** nobody has answered: whether queued input is absorbed, blocked, or cancels the approval is
   undocumented and decides the injector's behaviour. `turn/unqueue` on a queued frame must yield `unqueued` and no
   `turn/completed` for it.
5. **The question flow.** A prompt that makes the model ask (`userInput/requested`) answered through the client;
   `userInput/cancel` resolves the tool call as cancelled; `autoResolutionMs` recorded if present.
6. **Identity and reach in the model's shell.** A shell tool call records environment names (`MUSE_SESSION_ID`
   expected; whether the host's own environment leaks into the sandbox), `/proc/1/comm`, whether a file under the
   test root's state dir outside the workspace is readable and writable (bwrap scope), whether the fs-adapter
   `brigade` binary runs there and what it resolves as its session with `CLAUDE_PID` absent — the facts the
   host-aware `config.InSession` (section 6) needs, measured here instead of in 5.7.
7. **Lease and hand-off.** With the host holding the session, `muse resume <uuid>` in a tmux pane must refuse
   ("session already open", naming the holder's pid); after a clean close (stdin EOF, exit 0, the 30 s drain) the TUI
   must open the same session and show the injected turns (serve → TUI is undocumented; TUI → serve is implied by a
   1.4.4 fix); then `muse serve` plus `session/resume` while the TUI holds it must exit 4 / `sessionInUse` and leave
   the TUI's session untouched. `session/resume` after a host crash (SIGKILL mid-turn) must return `pendingRequests`
   and re-issue a parked approval under a new `approvalId`.
8. **Sizes.** Text parts of 2,000, 8,192, 8,193 and 16,384 bytes through the inlet: no cap is documented for turn
   input, so each is recorded as delivered whole or refused with its error.
9. **Privacy.** The 5.1 pre-flight repeated on a served session: no skill or rule name from the real `~/.claude` in
   `session.jsonl`; whether the kill-switch variable in the host's environment is honoured by `serve` (the
   `--no-foreign-personal-context` flag is documented for `exec` only).
10. **Peer receiver (cheap, last, informational).** A second handshake with `experimentalApi: true` and
    `peerMessaging` requested; from a TUI session in the same root, `muse session-message send --target <served
    uuid>`; whether `admission/requested` reaches the client, and after `allow_once` whether the idle served session
    starts a turn. Served sessions are not documented as eligible receivers; a FAIL changes nothing for route D, a
    PASS means TUI users could message a Brigade-owned session natively.

Rows 3, 4 and 7 run on the real provider; rows 1, 2, 5, 6 and 8 run on the echo provider first where the host accepts
it, then once on the real provider. The scorer's "no key event in the window" rule reads the driver's frame log
instead of pane captures: any `turn/start` not originated by the inlet inside a wake window voids that wake.

## 6. If outcome 2 or 3 passes: the implementation shape (not to be started before the verifier signs 5.10)

Kept short and conditional on the rows named; the Codex plan's §4–§5 is the detailed template.

- **Package.** `muse/brigade/` with a native `.muse-plugin/plugin.json` (argv-array hooks — this satisfies the
  "never spawn with a shell" rule better than a Claude-format string would), one wrapper per event, its own
  `bin/brigade` bootstrap copy, VERSION and checksums from the one canonical source (release-prep copies them,
  checksums-check compares them), skills ported with Muse's commands (`muse plugins update`, not
  `/reload-plugins`), and a `muse-plugin-check` beside `plugin-check` (manifest family, no `args`, no symlinks,
  no shared hook source). `plugin/` stays the Claude package, untouched.
- **Identity.** The Codex plan's `HostSession{Host, NativeID}`: `config.InSession` becomes host-aware
  (`CLAUDE_PID`, or `MUSE_SESSION_ID` **only if 5.7's two-session identity row passes in both the Bash tool's and
  the monitor's shells**), a by-native map keyed `(host, uuid)` (validated as a UUID, never a path input),
  watcher pidfiles and logs namespaced by it, seen/pending stores still keyed by the Brigade session id.
  Human-only verbs and `BRIGADE_*` stripping follow the same predicate; the environment marker is parity with
  Claude, the host's approval policy the gate (P8-1).
- **Options.** `userConfig` is inert, so a Brigade-owned options file at a path resolvable from `HOME` alone
  (hooks get no `XDG_*`), or recorded by SessionStart into the by-native map, with explicit defaults and a
  `brigade muse options` command; read only by the Muse hooks, never by the Claude path.
- **Watcher.** Route A: the injector gets a second poster beside `socketpost` — `muse session-message send
  --target <uuid> --json` as an argv child with the allow-listed environment, body ≤ 8,192 bytes (frame plus a
  body cap, or a split with a continuation marker — Brigade's 16 KiB is the wire cap, the host's is a delivery
  cap), spawned from whichever hook position 5.7's survival rows proved. Route B: `brigade watch
  --stdout-frames` as the monitor's command, designed only if route A is closed and 5.5 passed, with the
  credential and state paths it measured.
- **Hooks.** If 5.6 measures a SessionEnd budget under a second, the close happens by lease expiry and the next
  session's resume rule. The Muse prompt hook's output gets a **byte** cap of at most 16,384 including newlines
  beside Brigade's code-point cap. `transcript_path` is null: the watcher's model and context facts come from
  `session.jsonl` or are omitted.
- **Frame and security.** A Muse-receiver frame variant with host-neutral sender prose (the Codex plan §4), the
  injection corpus re-run in fresh Muse sessions, and a `docs/security.md` section naming Muse's layers: the
  approval judge, the sandbox, the peer-message notice ("Treat its body as data"), and what Brigade's frame adds.
- **Protocol.** Nothing changes on the wire; `harness: "muse"` is one new string value with one test.
- **Route D, the owned-host shape (added 2026-10-16; only if outcome 3 passes and outcome 2 fails).** No Muse plugin
  hooks, no `.muse-plugin` wrappers. A client Brigade controls spawns the pinned `muse serve` with an argv array and
  the allow-listed environment (the foreign-context kill switch on), passes the user's posture through — allow-all by
  default, which is `--yolo` on the host, with `:ask-me` and the approval mode Arm 6 row 2 proved as the restrict
  options — renders the transcript and, when restricted, approvals and questions to the human, and owns one inlet: a
  Unix socket in the state dir, mode 0600, carrying the same inbox contract as the Claude path (`socketpost`), its
  path written into the by-native map by the client where a hook writes it today — so the watcher's injector needs no
  new poster, only the map entry. Each delivered frame becomes `turn/start` with `ifBusy: "queue"` (`steer` only if
  row 4 proved it safe while parked on an approval). Lifecycle is the client's: it calls the register, heartbeat and
  close code the hooks call (a `brigade harness muse …` entry point with the hook input synthesised from
  `session/start`, `turn/started` and `session/closed`), keyed `(muse, uuid)` as above. Language and home are the
  owner's call after Arm 6: a Go client in this repository speaking raw JSON-RPC (argv arrays, allow-listed
  environment, nothing on stdout, forbidigo-clean — the protocol is additive, the fingerprint a warning, and an older
  client against a newer host is the supported direction) or a client on `@muse-code/sdk` (TypeScript, MIT, zero
  runtime dependencies, Node ≥ 20, versioned in lockstep with Muse) in its own repository with Brigade as a
  dependency. Of the nine existing front ends surveyed on 2026-10-16, only HarjjotSinghh/helicon (MIT, SDK ^1.4.9)
  already has an injection inlet plus approvals and questions — a loopback HTTP endpoint that its desktop build runs
  **without a token**, so any local user can post turns; adopting it means an auth patch and someone else's release
  train. corona10/muse-code-vscode (MIT) is small and its turn function already takes `ifBusy`, but it is VS
  Code-bound; MTG-Thomas/Mousagetes has a 0600 Unix-socket inlet but is AGPL-3.0 and hardcodes the sandbox off. The
  survey's verdict is the recommendation: a minimal client (transcript, approvals, questions, inlet; one to two
  thousand lines) beats a fork. The skill text reaches the model through a skills-only Muse plugin in the host's
  plugin store (served sessions read it at startup; "hooks run in the host process") or a `skill` input part;
  `userConfig` stays irrelevant because the client owns the options. `docs/security.md` gains the inlet's threat row
  (local-only, 0600, a token or peer-uid check) with helicon's tokenless endpoint as the counter-example.


## 7. What Caleb's account setup should check before the run

1. Sign in once **inside the test root** (device login, or an API key read from a file outside the repository),
   then record the feature-config names and booleans. If `external_agent_ingress` is absent or false, route A is
   closed for this account today; the run still answers the rest, and the finding goes on the card.
2. Leave the real `~/.config/muse/` alone; the run never copies `auth.json`. If you have already signed in there
   for your own use, that is fine — hash its `settings.json` before and after.
3. Expect the first `muse` in a directory to ask to trust the workspace; `--trust-workspace` is passed for the
   empty test workspaces and the dialog is still pre-flighted (answered once, logged, before any hold).
4. Privacy: an interactive `muse` in a directory under your real home sends your `~/.claude` skills and rules
   to Meta with every prompt unless `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on` is set. The run sets
   it and checks it (5.1); set it in your own shell too.
5. Budget: two or three prompts per session across a few dozen short sessions per full run; the monitor arm
   holds sessions idle for 35 minutes (one for 2 hours if affordable) with no prompts during the hold; a macOS
   rerun of Arm 1's core steps if Linux refuses.
6. For Arm 6 nothing more from the account: the SDK tier is on by default (`MUSE_EXPERIMENTAL_SDK_ENABLED` is an
   off-switch — make sure nothing in your shell sets it to `off`). Whether a served host's turns count against the
   subscription is not stated: note the usage page before and after the probe day.
7. Decide before Arm 6, not after: whether a one-time human action at the client's start (workspace trust, a network
   card) counts as "no human in the loop". It is an install step of Brigade's own client, which is not the same thing
   as A2's approval card per message.

## 8. Sources (checked 2026-10-15)

- Meta docs, read through summarising fetches: https://dev.meta.ai/docs/muse-code, /session-messaging,
  /extending, /configuration, /permissions, /auth, /subscriptions; the changelog
  (https://dev.meta.ai/docs/muse-code/changelog) downloaded raw and parsed — the summarising fetches of it
  misattributed several entries to the wrong version
- SDK reference site (1.4): https://meta-models.github.io/muse-code-sdk/1.4/ — guides/plugins/reference/hook-events,
  guides/plugins/reference/manifest, guides/plugins/concepts/compatibility, guides/plugins/concepts/hooks,
  guides/plugins/examples/import-claude-code-or-codex-plugin, guides/host-operations, generated/msp (peer/list,
  admission/*); github.com/meta-models/muse-code-sdk (CHANGELOG.md, schema/msp, issue #84)
- Binary: `muse` 1.4.9-R6297.1 x86_linux from the stable channel manifest
  (`https://api.meta.ai/muse-code/channels/muse-stable`), sha256 verified against the manifest, inspected with
  `strings` only, never executed. Installer: https://dev.meta.ai/install.sh → https://api.meta.ai/muse-launcher.sh
- Third-party measurements: aiur-team/khala (`docs/build/multi-harness/spikes/muse.md`, `qwen-muse-no-gap.md`,
  issues #1117, #1126, #1143; Muse 1.4.3-R5018.1, Linux), agentchute v1.7.2 and PR #235 (1.4.7), Signet-AI/signetai
  PR #2075 (1.4.3), fmktech/ftown PR #105 (1.3.0), Guilduo (Windows 1.4.3), amux and traceary (1.0.3), agent-fleet
  and oh-my-musecode (1.3.0), rtravellin/muse-code-plugin-cc, kunchenguid/firstmate
- This repository: `.context/plans/codex-team-participation.md`, `docs/experiments/codex-participation.md`,
  `.context/plans/brigade-execution-log-archive.md` row P8-1, `docs/experiments/E4-idle-wake.md`,
  `docs/experiments/E0-4.md`, `docs/experiments/E0-7.md`, `scripts/proof-idle-wake.sh`, `plugin/hooks/hooks.json`,
  `plugin/.claude-plugin/plugin.json`, `internal/harness/hook/{spawn,prompt}.go`,
  `internal/harness/config/environ.go`, `internal/harness/sessionmap/bypid.go`, `internal/protocol/limits.go`
- Added 2026-10-16 for route D (read through summarising fetches): SDK site guides/msp-concepts (sessions-and-turns,
  approvals, durability-profiles, exit-classification), guides/msp-wire (and backpressure), guides/host-operations,
  guides/quickstart, compatibility, generated/changelog, generated/msp/methods (initialize, session-start,
  session-resume, turn-start, turn-steer, turn-unqueue, approval-decide, userInput/*, peer-list, admission-*,
  hook-list, session-setpermissionprofile), generated/msp/types (clientinfo, initializeresult, ifbusy, turninputpart,
  admissionchoice), generated/msp/capabilities, cookbook (approve-or-deny-a-tool-call, queue-steer-and-reclaim-turns,
  resume-a-session, survive-the-host-dying, answer-the-agents-question, classify-every-serve-exit);
  registry.npmjs.org/@muse-code/sdk and PyPI muse-code-sdk (both 1.4.10, 2026-10-16);
  dev.meta.ai/docs/muse-code/{auth,permissions,session-messaging,extending} re-read for `serve`.
- Front ends surveyed 2026-10-16 (cloned read-only into the session scratchpad, never executed):
  bex-co/muse-code-acp, RandyNorthrup/muse-spark-code, ChrisjanWust/paseo-muse-plugin, codegod100/muse-code,
  braedonsaunders/bb-plugin-provider-muse, corona10/muse-code-vscode, HarjjotSinghh/helicon, MTG-Thomas/Mousagetes,
  ibchouti9/openmuse; rtravellin/muse-code-plugin-cc read in full the same day (a Claude Code plugin that drives
  `muse exec`; no inbound path).

## 9. Review disposition (2026-10-15)

The first draft was reviewed the same day by two independent Opus reviewers (evidence fidelity against the
research record; experiment design against the P8-1 verifier's standards). Of their 44 findings, 43 are folded
in above — the watcher-position sender, the macOS verified-sender identity, the per-generation grant (A2
redefined), WAKE scored separately from CONTENT, the idle model-call settings and same-length null controls, the
solicitation limit as a product property, fresh receivers per variant, flag-conditional passes, `env -i`
allowlisting, no auth copy, the survival positive control, UUID targeting and the wrong-session gate, the reply
arm isolated with its own config dir, per-event wrapper files, the ≥ 35-min hold, the E0-7 identity shape, the
joined-token-only delivery rule with an inert-hooks null, at-limit size controls, the 16 KiB byte cap against
Brigade's code-point cap, `XDG_*` aligned with `HOME`, the settings-level hook in the test root, the privacy
pre-flight, and section 6 made conditional on its rows. One finding was corrected rather than applied: the
dev.meta.ai pages had been read on 2026-10-15 (through summarising fetches), so their rows stay, labelled as
such in section 2's preamble and section 8. The revision has not had a second review pass.

**2026-10-16.** Section 1's closing, section 3's wake row, route D in section 4, steps 5–6 of 5.2, outcome 3 in 5.10,
Arm 6 (5.11), the owned-host shape in section 6 and items 6–7 of section 7 were added after Caleb ruled that the
human's front end need not be Meta's TUI. Their facts come from two Opus research passes the same day (the SDK
reference, concept guides, cookbook and changelog; a nine-repository survey of MSP front ends, cloned read-only) and
from a full read of rtravellin/muse-code-plugin-cc. The SDK's generated method pages are thin ("No description is
available" on many), so the rows lean on the concept guides, the cookbook and the changelog; nothing was run. The
additions have had no adversarial review.

**2026-10-16, later.** Caleb's ruling that Brigade is allow-all by default with options to restrict, and that
customers run their harnesses with `--yolo`, replaced the 5.1 no-`--yolo` rule with a two-configuration rule
(customer configuration first, Muse's default second) and reworded route B's arming note, route D's approval cost,
Arm 2, outcome 2's arming clause, outcome 3, Arm 6's preamble and row 2, and the route D build shape. The evidence
rule for `--yolo` sessions (no tool-call record may name an evidence path) is new and unreviewed.

## 10. Live results, 2026-10-16 (echo provider only)

Two arms ran on this machine (Linux) on the pinned `muse` 1.4.9-R6297.1 (`x86_linux`, sha256
`a228010021276e112aa8422eb56ae82c0375405701950f6529bed4c9fe8abe05`, equal to the manifest's `checksum` field), each
in its own test root under `$XDG_RUNTIME_DIR` with the 5.1 allowlist environment and `--provider echo` (no sign-in,
no model calls, no billing), each re-checked by an independent Opus verifier against the raw records. Nothing ran on
the real provider and nothing ran under `--yolo`. The two arms ran **at the same time**, so 5.1's wrong-session gate
failed for the TUI rows: A1–A3 are VOID by the plan's own rule, and their facts are recorded as mechanics, not
verdicts. The raw evidence (frame logs, `session.jsonl` copies, receipts) is in the session scratchpad on this
machine only, not in the repository.

**Facts the verifiers confirmed:**

- **Arm 0.** `muse plugins validate --json` on a `git archive` copy of `plugin/` exits 1 with three errors, "foreign
  hook handler field `args` is unsupported" (one per hook), plus warnings that the skills' `allowed-tools`
  declarations grant nothing until plugin review. D99 confirmed on 1.4.9.
- **Gate, unauthenticated.** With no feature-config, and again with the machine's only cached one (file name
  `7075626c6963`, which is hex for "public": probably the anonymous config, fetched before the owner's sign-in
  refreshed, so **the signed-in account's gates are still unmeasured**), `muse session-message list --json` and `send
  --target <uuid>` answer `{"status":"unavailable","error_code":"external_agent_ingress_closed"}`, exit 1, from a
  plain shell and from the watcher position (a detached `setsid` child of a settings-level SessionStart hook carrying
  the session's `MUSE_SESSION_ID`). The cached config lists `local_session_messaging`, `monitor` and `plugins` true
  and has no `external_agent_ingress` key; Muse did not rewrite the file. An account gate that is closed is treated
  as closed: Brigade does not work around a server-side gate the account was not granted.
- **Hooks (settings level).** The working format is `settings.json` `{"schema_version": 1, "hooks": {"<Event>":
  [{"hooks": [{"type": "command", "command": "<string>", "timeout": <s>}]}]}}`. Hooks run for TUI and served sessions
  alike and appear in `hook/list`. Their environment has `MUSE_SESSION_ID` and `MUSE_THREAD_ID` and no `XDG_*`.
  SessionStart ran at the first prompt in every observation, but no run held an untouched session long enough to show
  it never fires at launch. A SessionEnd hook is killed somewhere between 0.235 s and 1.235 s after it starts. The
  stdout cap is 16,384 bytes: 16,000 bytes passes and a `setsid` background child survives; 17,000 bytes fails with
  "output_too_large … the process tree was terminated" and the background child dies with it.
- **Driving the TUI.** No tmux on this machine; a python3 pty driver works. `\r` submits a typed prompt (so does
  `\x1b[13u`); `\n`, `\x1b[13;5u`, `\x1b\r` and `\x1b[27;5;13~` do not. On 1.4.9 a user turn is recorded as
  `runtime.user_intent.accepted` (no `turn.input.user`). `/exit` then CR exits 0 with a clean `session.end`.
- **`muse serve` (route D mechanics).** Newline-delimited JSON-RPC 2.0. `clientInfo.name` must match `^[a-z0-9_]+$`
  (a hyphen is refused, -32602). Every `commandId` must be a UUIDv7. The stable schema export lists 69 methods, the
  experimental one 95 (`permissionProfile/list`, `peer/list`, `session/setPermissionProfile` are experimental only).
  A new session defaults to approval mode `onRequest` under `:auto-review`; `:ask-me` is accepted with all four
  approval modes. Idle holds of 5 s, 120 s and 1,500 s produced zero frames and no unload, and a `turn/start` at the
  end reached `turn/started` in 6–9 ms. Queued turns run in order and survive a host crash; admission holds at most 4
  queued turns (-32031). `ifBusy: "replace"` cancels the running turn. Text input up to 1,000,000 bytes is stored
  whole in `session.jsonl` (wire items are cut at 65,534 characters, `truncated: true`). The lease is a `flock` on
  `.session.lock`: a second host gets -32021 `sessionInUse`, `muse exec --session-id` gets "already in use", exit 1;
  after a clean close `exec` continues the same session; after SIGKILL the lock is free within about 27 ms.
  `peer/list` returns `[]`, and a session-message to a served session gets the same gate refusal.
- **Sandbox on this machine.** Every sandboxed shell command fails before it runs: `bwrap: loopback: Failed
  RTM_NEWADDR: Operation not permitted` (the host sets `apparmor_restrict_unprivileged_userns=1`; that this is the
  cause is unverified). The default profile cannot exercise any shell row here; the `--yolo` configuration, sandbox
  off, has not been tried.

**Not measured yet** (each is a 5.x row above):

- Everything on the real provider: WAKE and CONTENT rows, Arm 2 (monitor), Arm 6's model rows, the question flow,
  approvals on a real tool call.
- The signed-in account's feature-config gates (5.2 step 1), read after a browser sign-in under the card's rules
  (Power subscription, `META_API_KEY` never set, the credential never copied).
- The customer configuration (`--yolo`) for every arm; null controls; a hold of at least 35 minutes; the 0600
  Unix-socket inlet; TUI ↔ `serve` hand-off of one session; the scorer with mutation rows; the privacy pre-flight;
  arms run one at a time so the wrong-session gate holds.

## 11. Live results, 2026-10-16 (macOS, real provider) — see `docs/experiments/muse-participation.md`

Run by Rjae's session after the hand-off, on the operator's Power subscription (browser sign-in, never
`META_API_KEY`), Muse **1.4.10-R6427.1** `aarch64_macos`, in the plan's order. Headline: the signed-in account's
feature config has no `external_agent_ingress` (route A **closed**, both profiles; behind the undocumented switch a
cold send is refused `sender_unverified` before any card); Arm 0 confirms D99 on 1.4.10; **route B passes its short
holds** — a persistent `monitor` with `wake_delay_ms: 0` wakes the idle session 0.2 s after an outside append, under
`--yolo` and under the default profile, with no approval record, lines whole to 16 KiB, surviving `/compact` and
`/clear` (not `/exit` + resume); settings-level hooks fire all three events with `MUSE_SESSION_ID = session_id`, a
detached SessionStart child survives, SessionEnd gets 0.1–0.4 s. The ≥ 35-minute hold, its null control and the
UserPromptSubmit-on-wake row are in the report. Route D was not probed (5.2 step 5). The decisions 5.10 leaves to
Rjae are listed in the report's §8.

