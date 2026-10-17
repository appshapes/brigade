# Muse Code participation: the live test (card 88)

Date: 2026-10-16 (evening, EDT; the log timestamps below are UTC of 2026-10-17)
Card: AppShapes Trello 88, "Research Muse support for Brigade"
Plan: `.context/plans/muse-team-participation.md` (sections 4 and 5; this report follows its arms and scoring rules)
Client: Muse Code **1.4.10-R6427.1**, the `aarch64_macos` artifact, sha256
`11cc1ae887b4975bbbe1de7358ee9260a612444df9ce4dbfffa1dd785826dbcf` = the channel manifest's `checksum` (verified
at the start of the run); run directly, never through the self-updating launcher
Host: macOS 26.6 (Darwin 25.6.0, arm64), `tmux` 3.7c, `python3` 3.14
Account: the operator's Power subscription, signed in through the browser at 01:21:28Z (never `META_API_KEY`, never
a copied `auth.json`: the runs used the real `XDG_CONFIG_HOME` and isolated `XDG_DATA_HOME`/`STATE`/`CACHE`)
Model: `muse-wonder-full-1.0` at `xhigh` (the account's default)
Driver: `scripts/experiments/muse-participation/` (README there)
Result: **route A (`session-message send`) is CLOSED for this account; route B (the `monitor` tool) WAKES an idle
session 0.2 s after an outside append, with no human and no approval, under `--yolo` and under Muse's default
profile, after 5 s, 120 s and 35 min idle, lines whole to 16 KiB, through `/compact` and `/clear` but not a resume;
settings-level hooks fire the three events and a detached hook child survives; UserPromptSubmit does not fire on a
woken turn; Brigade's plugin package is refused as it stands (D99).**

Earlier measurements (Linux, 1.4.9, echo provider only) are in the plan's section 10. Everything here ran on the
real provider unless the row says `echo`. Raw `session.jsonl` copies, pane captures and the auth state stayed outside
the repository; the numbers below were produced by `score.py` from those records.

## 1. The account's gates (plan 5.2 step 1)

Muse fetches a feature config only once a login is present (the operator's first run: credential unavailable at
01:21:08Z, no fetch; login present 01:21:28Z, `feature_config.fetch outcome=fresh` 01:21:30Z). A fresh data dir,
signed in, fetched the same file at 01:33:23Z: `feature-config/7075626c6963.json` ("public"), 13 gates —
`local_session_messaging` **true**, `monitor` **true**, `plugins` **true**, `pace_guardrail`, `voice`, `web_fetch`,
`workflow_tool`, `subscription_*`, `spark_1_3_contributor_max` true; `tag`, `workflow_api_v2_rollout` false — and
**no `external_agent_ingress` key**. A second launch with the cache present resolved `local_session_messaging` from
`source=remote` and `external_agent_ingress enabled=false source=default`: the account's remote config is consulted
and does not grant the ingress gate.

## 2. Arm 0 — `muse plugins validate plugin` (no model)

Exit 1, `invalid plugin diagnostics=32`: three errors "foreign hook handler field `args` is unsupported" (one per
hook; D99 holds on 1.4.10), twelve warnings "Claude manifest field `userConfig` declares unsupported behavior" (one
per option), warnings that the skills' `allowed-tools` grant nothing until plugin review, and the presentation-only
manifest fields ignored. Brigade's package cannot install on Muse as it stands.

## 3. Arm 1 — route A, the ingress gate (decisive for route A)

Each row: a fresh TUI session on the real provider, idle at the composer, one outside process (`run-muse.sh
session-message …`) targeting it by UUID; the pane and `session.jsonl` read afterwards.

| Row | Receiver | `list` | `send` | Card in the TUI | Admission record | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| A1-default | Auto-review profile (Muse's default) | `external_agent_ingress_closed` | exit 1, same code, `receipts: []` | none | none | A3 (closed) |
| A1-yolo | `--yolo` | same | same | none | none | A3 (closed) |

Both rows are the default launch environment. The gate is checked in the **sender** process before any target is
consulted, so the watcher's position cannot change it; the plan's step 2 (watcher-position sender) therefore has
the same answer as step 3 on this account, and the Codex-era conclusion for route A stands: **closed for this
account today**, on macOS as on Linux. Not worked around for the verdict (plan section 10's rule).

**Informational rows (CONDITIONAL by construction; not part of any verdict).** With Muse's undocumented
`MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=on` in both the TUI's and the sender's environment, the gate resolves
`enabled=true source=override`, `list` shows the live sessions, and `send` is refused at admission with
`sender_unverified` (exit 1, `receipts: []`) — no card in the TUI within 6 s, no admission record — both for a plain
process and for a process carrying a second live TUI session's `MUSE_SESSION_ID`/`MUSE_THREAD_ID`. On 1.4.3 the
third-party sighting was `containment_limited`/`causal_metadata_invalid`; on 1.4.10 it is `sender_unverified`. The
approval card of the card's open question 1 was never reached.

## 4. Arm 2 — route B, the `monitor` tool (decisive for outcome 2)

The TUI session is asked, in one prompt, to arm its `monitor` tool once (persistent, `wake_delay_ms 0`) on
`fixture/watch.sh <trigger-file> <facts-file>` (a `tail -n 0 -F` of the trigger file) and to answer each line with
`RECEIVED <the line>`. The driver then appends a line carrying a 12-hex-character nonce after each idle hold, never
types again, and the scorer reads Muse's own `session.jsonl`: `inbox_item_queued` (the monitor saw the line), a run
whose origin is `inbox_delivery.run_origin` with client `muse-runtime-background-terminal` (the wake: a turn nobody
typed), `inbox_item_drained`, and the nonce inside the model's own `assistant_message_committed` text. Offsets are
from the driver's append (Muse's clock reads about 20 ms behind the driver's).

### 4.1 Customer configuration (`--yolo`)

| Session | Monitor args (from the log) | Append | Idle before | Queued | Run start | Drained | Text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pilot `…6b2be94` | `persistent: true` (no `wake_delay_ms`) | MSG2 | 125 s | −0.021 s | **+119.997 s** | +120.065 s | +122.340 s | 0 |
| v2 `…c3f67236` | `persistent: true, wake_delay_ms: 0` | MSG1 | 5 s | −0.016 s | **+0.196 s** | +0.257 s | +2.331 s | 0 |
| v2 `…c3f67236` | same monitor | MSG2 | 120 s | −0.020 s | **+0.195 s** | +0.271 s | +3.851 s | 0 |

Null control (v2): 120 s with the monitor armed and nothing appended — 0 runs started. The pilot's MSG1 is VOID
(the driver appended it before the monitor's `tail` started; the arming detection matched the prompt's echo), and the
pilot's default batch delay measured **120.0 s**, which is why `wake_delay_ms: 0` is part of the arming text.
The only tool call in either session is `monitor`; no tool record names an evidence path (the `--yolo` evidence rule).
Arming needed no human decision under `--yolo`: the model armed it from one prompt, 10–13 s after submit.

The monitor's shell under `--yolo` (facts file written by `watch.sh`): environment names `HOME LANG MUSE_SESSION_ID
MUSE_THREAD_ID MUSE_TOOL_USE_ID CLAUDE_CODE_TOOL_USE_ID PATH PWD SHELL SHLVL TERM TMPDIR USER XDG_CACHE_HOME
XDG_DATA_HOME XDG_STATE_HOME` plus the two `MUSE_*` the TUI was launched with — the monitor inherits the TUI's
environment, unlike hooks; `ps` sees the host's processes; writes to `$HOME` and `/tmp` succeed; an HTTPS connection
to the Supabase host succeeds. Sandbox off, as expected.

### 4.2 Muse's default profile (Auto-review, sandbox on, approval judge on)

Same prompt, no `--yolo` (`--trust-workspace` only), and the trigger file **outside the workspace** (under the test
root's state directory), because that is where a Brigade sink would live. Session `…544722a7f`:

| Append | Idle before | Queued | Run start | Drained | Text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- | --- |
| MSG1 | 5 s | −0.018 s | **+0.194 s** | +0.275 s | +4.282 s | 0 |
| MSG2 | 120 s | −0.016 s | **+0.199 s** | +0.267 s | +3.106 s | 0 |

Null control: 120 s armed, nothing appended — 0 runs. Monitor args `persistent: true, wake_delay_ms: 0`. **No
approval record of any kind** in the session: the `monitor` call started 01:54:56.845Z and completed 36 ms later; the
76 s between the prompt and the call was model time at `xhigh`. So arming a monitor needs no human decision under
Muse's default profile either, at least for a command that reads a file outside the workspace.

The monitor's shell under the default profile is the sandbox: environment adds `ALL_PROXY FTP_PROXY HTTPS_PROXY
HTTP_PROXY NO_PROXY NODE_USE_ENV_PROXY` (proxy-only network), `ps` sees no process, a write under `$HOME` fails
("Operation not permitted"), a write under `/tmp` succeeds, a loopback connect is refused, and a direct HTTPS
connection fails at DNS. Reading the trigger file outside the workspace succeeded. **Design implication:** under the
default profile Brigade's watcher cannot live inside the monitor (no network without the proxy and its per-host
review, no writes to the state or credential directories); the shape that fits both profiles is a watcher started by
a settings-level SessionStart hook (which survives, §5) writing one line per frame to a sink file in Brigade's state
directory, and a monitor that only tails that file. That shape was not built here (it needs a `brigade watch`
stdout or sink-tail mode); every piece of it was measured separately.

### 4.3 Line sizes, compaction, clear, resume (`--yolo`, session `…b57f7dac`)

The same arming prompt, then lines of 64 B, 2 KiB, 8 KiB and 16 KiB, each `MSG<n> <nonce> <padding> END=<6 hex>`;
the model is asked to answer `RECEIVED MSG<n> <nonce> END=<the 6 characters>`, so the END marker proves the line
arrived whole. "Text" is the model's reply in the pane (model time included; the run-start offsets of §4.1 apply).

| Line | Size (with newline) | Whole line echoed | Text |
| --- | --- | --- | --- |
| MSG1 baseline | 64 B | yes | +4.4 s |
| MSG2 | 2,048 B | yes | +18.8 s |
| MSG3 | 8,192 B | yes | +10.5 s |
| MSG4 | 16,384 B | yes | +5.9 s |
| MSG5 after `/compact` (26 s later) | 64 B | yes | +3.1 s |
| MSG6 after `/clear` (11 s later) | 64 B | yes | +3.1 s |
| MSG7 after `/exit` + `muse resume <uuid>` | 64 B | **no** — never queued; no wake in 131 s | — |

Scored from the log, the run-start offsets were +1.85 s, +0.20 s, +1.48 s, +0.74 s, +0.20 s, +0.20 s (the larger
ones are Muse reading a long line before queuing it, not batching); the model's text is slower on the long lines
because it reached for `read_file` and `bash` to look at them, against the prompt's instruction — the only tool
calls in the run besides `monitor`, and none naming an evidence path. A monitor line is not subject to the 8 KiB
session-message cap, and the monitor survived both `/compact` and `/clear` without re-arming. It did **not** survive
the session's exit: the TUI's `/exit` was still compacting when the driver closed the pane (SIGKILL; "the previous run
of this session ended abnormally"), the pane on resume showed `Monitor "Watch inbox messages" — failed · signal: 9`,
the resumed session armed nothing by itself, and MSG7 was never queued. A resumed session needs the model to arm the
monitor again, which needs a prompt: the same first-prompt cost as a new session.

### 4.4 The ≥ 35-minute hold and its null control (`--yolo`)

Two sessions launched together, each with its own monitor and trigger file. The **hold** session (`…d8dcb09a2`,
launched with the §5 probe hooks) was armed at 01:59:54Z and left alone for **2,100 s**; the **null** session
(`…423a13ae08`) took the 5 s and 120 s appends first (both woke: +2.7 s to the model's text) and was then left armed
with nothing appended for 2,100 s.

| Session | Append | Idle before | Queued | Run start | Drained | Text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- | --- | --- |
| hold `…d8dcb09a2` | MSG1 | 2,100 s | −0.016 s | **+0.202 s** | +0.276 s | +3.416 s | 0 |
| null `…423a13ae08` | none for 2,100 s | — | — | **0 runs started** | — | — | 0 |

The session woke after 35 minutes idle exactly as after 5 s: the 30-minute "timed ceiling" in the tool's text does
not apply to a persistent monitor, and nothing in the hold session's log is a model call nobody sent (one user turn,
one background-origin run). **UserPromptSubmit did not fire on the woken turn**: the probe's store holds one
SessionStart and one UserPromptSubmit record, both from the first prompt at 01:59:40Z, and nothing at 02:34:58Z —
so Brigade's prompt hook (heartbeat, poll) would not run on monitor-woken turns, as on 1.4.3. The SessionEnd probe
left no record either (its python start-up is longer than the 0.1–0.4 s budget of §5).

## 5. Hooks at settings level (plan 5.6/5.7 rows that need no plugin; `echo` provider, `muse exec`)

Supplied per run with `-c 'hooks={…}'` (the operator's `settings.json` untouched):

- All three events fire: `SessionStart` (`source: startup`) 01:51:19.136Z, `UserPromptSubmit` 01:51:19.372Z,
  `SessionEnd` (`reason: other`) 01:51:20.030Z. In the TUI (the long-hold session of §4.4, launched with the same
  hooks) the TUI sat at its composer for 6.6 s before the first prompt, and `SessionStart` fired 0.28 s **after** that
  prompt was submitted (01:59:40.406Z vs 01:59:40.126Z), followed by `UserPromptSubmit` 0.18 s later: on 1.4.10 as
  on 1.4.9, SessionStart runs at the first turn, not at launch, so a Brigade session would register when its user
  first types — the same moment its monitor can first be armed. stdin fields: `cwd hook_event_name model permission_mode session_id
  transcript_path` (null in all three) plus `source` | `prompt turn_id` | `reason`. `$0` is the script path.
- Environment names: `HOME LANG LOGNAME MUSE_SESSION_ID MUSE_THREAD_ID PATH PWD SHELL SHLVL TERM TMPDIR USER _` — no
  `XDG_*`, no `CLAUDE_CONFIG_DIR`. `MUSE_SESSION_ID` equals the stdin `session_id` in all three.
- **Survival:** a child started from the SessionStart hook in Brigade's spawn shape (new session, stdio to a log,
  not waited for) was alive after the hook returned and after the whole run exited; killed by the driver. The 1.4.8
  helper cleanup did not touch a settings-level hook's child on macOS 1.4.10 (plugin hooks were not measured: the
  package does not install).
- **SessionEnd budget:** a hook ticking every 0.1 s wrote 2, 4 and 4 ticks (0.13 s, 0.39 s, 0.40 s) in three runs and
  never finished — about 0.1–0.4 s, as on Linux. Brigade's close must come from lease expiry.

## 6. Privacy pre-flight

With `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on` the TUI still printed "Found personal rules for Claude
Code — import into Muse Code with /rules import" (and set `tui.rules_import_offer_dismissed` in the operator's real
`settings.json`, the one change the runs made there), but the pilot session's `session.jsonl` carries none of the
operator's skill names or rule text: every `skills/*` mention is Muse's bundled `muse-core` set.

## 7. Capability table

Verdicts follow the plan's 5.9: PASS / FAIL / VOID / CONDITIONAL / NOT RUN, facts from `session.jsonl`, the probe's
private store and the driver's timestamped log; the model's own text scored, never an echo.

| Row | Verdict | Evidence |
| --- | --- | --- |
| Account gate `external_agent_ingress` | FAIL (absent, resolves `false`) | §1 |
| Arm 0: `plugins validate plugin/` | FAIL (D99 `args`; `userConfig` inert) | §2 |
| Arm 1 route A, default environment, both profiles | FAIL = A3 (closed before admission) | §3 |
| Arm 1 flag variants | CONDITIONAL, and still refused (`sender_unverified`) | §3 |
| Arm 2 WAKE after 5 s and 120 s idle, `--yolo` | PASS (+0.196 s, +0.195 s; CONTENT +2.3 s, +3.9 s) | §4.1 |
| Arm 2 WAKE after 5 s and 120 s idle, default profile, trigger outside the workspace | PASS (+0.194 s, +0.199 s; CONTENT +4.3 s, +3.1 s) | §4.2 |
| Arm 2 null controls (120 s, both profiles) | PASS (0 runs) | §4.1, §4.2 |
| Arm 2 arming without a human decision | PASS under `--yolo` and under the default profile (no approval record) | §4.1, §4.2 |
| Arm 2 default batch delay without `wake_delay_ms` | measured 120.0 s (the arming text must set `wake_delay_ms: 0`) | §4.1 |
| Arm 2 ≥ 35-minute hold | PASS (run start +0.202 s, text +3.4 s) | §4.4 |
| Arm 2 35-minute null control | PASS (0 runs in 2,100 s with the monitor armed) | §4.4 |
| Arm 2 lines of 2, 8, 16 KiB arrive whole | PASS | §4.3 |
| Arm 2 wake after `/compact` and after `/clear` without re-arming | PASS (+3.1 s, +3.1 s) | §4.3 |
| Arm 2 wake after `/exit` + `muse resume` | FAIL (the monitor dies with the process; re-arm needs a prompt) | §4.3 |
| Arm 2 UserPromptSubmit on a monitor-woken turn | FAIL — does not fire (the prompt hook cannot heartbeat there) | §4.4 |
| Monitor shell reach under the default profile | measured: no `ps`, no writes outside the workspace, no direct network, reads outside the workspace OK | §4.2 |
| `brigade watch` as the monitor's command (fs adapter / local stack) | NOT RUN — design implication recorded instead | §4.2 |
| Hooks: three events, stdin fields, `MUSE_SESSION_ID` = `session_id` | PASS (`echo`, settings level) | §5 |
| Survival of a detached SessionStart child (settings level) | PASS (alive after the run exited) | §5 |
| Survival under a plugin hook (the 1.4.8 cleanup) | NOT RUN (precondition FAIL: the package does not install) | §2 |
| SessionEnd budget | measured 0.1–0.4 s | §5 |
| Identity in the model's shell (E0-7 shape, two sessions, poison) | NOT RUN | — |
| Route D (Arm 6) | NOT RUN (plan 5.2 step 5: runs only if Arms 1 and 2 both fail; Arm 2 passed its short holds) | — |
| Privacy pre-flight | PASS | §6 |

## 8. What this does and does not prove

**Proved on this machine, this build, this account.** A Muse Code session whose model has armed one persistent
`monitor` with `wake_delay_ms: 0` starts a turn by itself about 0.2 s after an outside process appends a line to the
watched file, under `--yolo` and under Muse's default profile, after idle holds of 5 s, 120 s and 35 minutes, with the line's content reaching the model whole up to 16 KiB and surviving `/compact` and `/clear`. No
human decision was needed to arm it in either profile. That is the capability Codex lacked and P8-1 closed on: an
idle session woken from outside with no human in the loop. Settings-level hooks give Brigade its three lifecycle
events with the session id in both the payload and the hook's environment, and a child they spawn Brigade's way
outlives them. Route A is closed by a server-side gate the account does not hold, on macOS as on Linux.

**Not proved, and what each means for the build.**

- *The first prompt.* A monitor is armed by the model, so a session is a mailbox until its user types once (the
  SessionStart hook can put the arming instruction into context, but cannot start the turn). After `/compact` and
  `/clear` the monitor measured here kept running; after an exit and `muse resume` it did not (§4.3), so re-arming is a
  cost at every session start and resume, never mid-session.
- *The arming text.* Every session here was armed by one explicit prompt. Arming from Brigade's own skill text
  placed in context by a SessionStart hook (plan 5.5's "≥ 3 sessions per profile") was not run; it is the first row
  of the implementation card, not a capability question.
- *The watcher's home.* Under the default profile the monitor's shell has no network and no writes outside the
  workspace, so `brigade watch` cannot run inside it; the watcher must be the settings-level hook's detached child
  writing a sink file the monitor tails (every piece measured, the assembly not built). Under `--yolo` either shape
  works.
- *Plugin packaging.* Brigade's Claude package is refused (D99 `args`; `userConfig` inert; hooks get no `XDG_*`).
  A Muse package needs native argv-array hooks in `.muse-plugin/plugin.json`, a Brigade-owned options source, and
  `muse plugins approve` before any hook runs; the 1.4.8 helper cleanup for plugin hooks was not measured because
  no plugin hook ran — a settings-level hook is the measured exempt path.
- *Identity.* `MUSE_SESSION_ID` is a UUID present in hooks and in the monitor's shell; the two-session poison row
  (plan 5.7) was not run. The P8-1 ruling applies unchanged: the marker is parity with Claude, the host's approval
  policy is the gate.
- *Close and heartbeat.* SessionEnd gets 0.1–0.4 s, and UserPromptSubmit does not run on a monitor-woken turn, so
  neither the close nor the heartbeat can ride the hooks alone: the lease expires, and the watcher (or the monitor's
  own command) must heartbeat.
- *Cost.* Each wake is a model turn on the subscription; nothing here measured quota.

**Decision rule (plan 5.10).** Outcome 2 turns on route B: wake within seconds ✔, the ≥ 35-min hold ✔,
`/compact` ✔, armed without a human decision under `--yolo` ✔ (and under the default profile ✔), no Brigade
credential or state file under the workspace ✔ by construction of the sink shape, reaches the backend — through
the hook-spawned watcher, not from inside the monitor. The two facts the plan puts to Rjae for acceptance stand:
the route is reachable only after the first user prompt, and under the default profile any network from inside the
sandbox is a per-host review (avoided by the sink shape). Route D was not probed: the plan runs it only if Arms 1
and 2 both fail.
