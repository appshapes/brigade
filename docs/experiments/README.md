# Experiments: Phase 0 (2026-08-30 / 2026-08-31) and Phase 3 (2026-09-03)

Ticket 15. One writeup per Phase 0 experiment of the plan's section 8. Each file records what was **measured**, not
what was expected: environment and versions, the checks with their results, the plan text each result contradicts,
and a "limits" section naming what the run does *not* prove. Treat them as dated evidence.

The runnable drivers and harnesses are committed beside them under `scripts/experiments/` (`E0-1`, `E0-2`, `E0-3`,
`E0-4`, `E0-5`, `E0-6`, `E0-7`, `E0-8`, and `sitting/` for the three keyboard-dependent checks). The plan corrections
these experiments produced are collected in `.context/plans/brigade-execution-log.md`, which is the authority on what
must change before Phases 2 and 3; code written against the uncorrected plan text will be wrong.

Every experiment was run by an author and then re-checked by an adversarial verifier, and **the verifier found a
decisive flaw in every one of E0-1 through E0-8** after the author had reported fully green — 7 wrong-reason passes
in E0-1, 15 in E0-2, a missing causation control in E0-4, a missing null control in E0-5, a structurally guaranteed
all-zeros distribution in E0-6. Read a green result here as evidence only where the writeup names the control that
could have made it go red.

| File | What it settled |
| --- | --- |
| `E0-1.md` | The three `brigade` migrations apply to a minimal local Supabase stack and the RLS/privilege posture holds — 72 live assertions, 0 failed, green again after a `db reset`. `brigade:not_found` reaches the client as **HTTP 500 carrying SQLSTATE `P0002`**, so an adapter must key on SQLSTATE and not on status. pg_cron 1.6.4 is present, so the opportunistic-gc fallback is not needed locally. |
| `E0-2.md` | **D21 stays on broadcast-from-the-database** — neither flip condition fired. Exactly-once holds with concurrency actually measured (peak 50 in-flight RPCs), revocation takes effect on both the `leave_team` and the membership-only path, and a 30-minute soak passed 14/14 across a stack restart. The drain must **not** use a `seq` watermark: cross-page `seq` inversion was observed live. |
| `E0-3.md` | Inbound framing and reply behaviour. Variants A and C tie on every automated gate — 5/5 `-p` and 5/5 interactive replies each with the right ids, 0 native `SendMessage`, 0 evasive forms — and **26/26** of the P0-1 injection corpus pass. Records **D19 = C**, decided by check (b) at the interactive sitting. Caveat kept in the writeup: the receiving harness prepends its own preamble, so 26/26 does not isolate the contribution of the plan's own frame. |
| `E0-4.md` | The idle-wake criterion is **MET**: a frame posted to an idle session's inbox socket produces a new assistant turn with nothing written to stdin — 12/12 delivered runs, worst 6.7 s against a 10 s budget. The figure to budget against is the **harness reaction, 1–7 ms**; the seconds are model latency. An **open stdin** is the load-bearing condition for a `-p` session to stay wakeable. |
| `E0-5.md` | **Both of §6.6's watcher exit conditions are wrong.** `kill(pid,0)` does not detect a SIGKILLed session (it stays kill-alive as a zombie; detection took 27.6 s and 59.0 s), and socket-ENOENT is a **permanent false positive** — `claude` never re-creates the socket and the session keeps working. Also: a plugin cannot buy SessionEnd budget but a `--settings` file can; `SessionEnd` cannot be the only close path; native session ids recur, so D9's map must tolerate that. D9's hash-compare-and-respawn is safe. One `/clear` boundary anomaly is **unresolved**. |
| `E0-6.md` | All six token-refresh checks pass over a 30m28s soak with three real OS processes on one `session.json`. **§5.1's "a token two or more steps behind revokes the family" is wrong** — the stale token is refused and the family survives, so treating `refresh_token_already_used` as terminal would destroy a working credential. One-behind tolerance is **load-bearing for crash recovery**. The flock is sound; its 100 ms poll costs ~100 ms per contention against a 36 ms hold. |
| `E0-7.md` | Two concurrent sessions in **one** `CLAUDE_CONFIG_DIR` each select their own profile through their own `--settings` `pluginConfigs` block, with no cross-writes. Profile and team come from `state/by-pid/<pid>.json` **in preference to the environment** (proven by a poison control), which makes that file an unauthenticated trust boundary guarded by filesystem permissions alone. A fresh `CLAUDE_CONFIG_DIR` does **not** inherit the login. The environment strip list must be a **prefix rule**, not an enumeration. |
| `E0-8.md` | CLI-only plugin mechanics, run on Claude Code **2.1.252**. The headline: **D20's skill grant holds in interactive Manual mode** — the one arm never previously tested — so a Manual-mode user pays one dismissible Skill dialog per project, not a prompt per command. Also: make the bootstrap's **background download the default** (0.02 s vs 33.5 s blocking at 250 kB/s); the `SessionStart` context line is `stdout.strip()`, not byte-exact; `SessionStart` **re-fires on `/clear`**; the `--body-file` threshold must sit below **10,000 characters**; and `claude plugin validate --strict` does **not** detect a missing hook-command binary. |
| `E0-9.md` | `crossSessionInbound` `hold` and `refuse`. `hold` shows a notice, delivers nothing, raises no dialog, and **nothing expired in ~25 minutes** (the spec asked only for 5). `refuse` is **silent to both sides** — the poster's socket write succeeds and returns nothing — which is what makes §6.10's settings scan load-bearing rather than defensive. |

## Phase 3 — the plugin measured through real headless sessions (2026-09-03, Claude Code 2.1.259)

Same rules as above: measured, not expected; each file names what it does not prove. The drivers are the repository's
own targets (`make plugin-check plugin-validate`, `make harness-smoke`) and `claude -p` invocations quoted in full.

| File | What it settled |
| --- | --- |
| `E3-wiring.md` | P3-6. The fs-adapter onboarding under the plugin (`profile init --adapter …`, `team create --secret-file …`), then a real `claude -p` with the dev pointer: the `SessionStart` context line names the fs team, the by-pid and by-native maps and a LIVE detached watcher exist mid-run, and the session's own `SessionEnd` stops the watcher **0.36 s before `claude` exits**. Two profiles with mixed adapter registrations and no `adapter_command` in either `--settings` each see only their own team; an unresolvable `adapter_command` yields the D36 `config` line, and a resolvable override that cannot read the profile the adapter's own `config: profile_missing` line. Also: a silent, successful `UserPromptSubmit` hook is recorded **nowhere** (stream, stderr, transcript), and a symlink that resolves to the plugin's bootstrap is not reported as a shadow. |
| `E3-smoke.md` | P3-7. `scripts/harness-smoke.sh` end to end through a real `claude -p` with the fs adapter, five nested sessions, 0 flakes: the model lists the team, sends the second principal a message that lands in the fs store, receives that principal's message **mid-turn** (recorded in the transcript as a `queued_command` attachment removed with `absorbed_mid_turn`, never as a `user` record), which the watcher acknowledges, and replies with `brigade send … --reply-to`; zero native `SendMessage` calls; no full-path or shell invocation. Assertions are `jq` predicates over the stream and the store, each shown able to fail. |
| `E3-interactive.md` | P3-8, **pending**: the checklist for the keyboard-dependent checks (the skill grant and the `permissions.allow` rule in Manual mode, the ask rule dialog in bypass mode, `/rename`, `/clear` and `/compact`, the shadowing warning, the sandbox domain entry), prepared for Rjae with the exact commands; results are recorded there as they are run. |

## E0-10 — not run, blocked on D32

`E0-10.md` does not exist. E0-10 is the optional round of hosted checks (anonymous sign-in limits, the Before User
Created hook, asymmetric signing keys, the paused-project error body, `supabase config push`, gateway behaviour
without an `apikey`, the embedded-roots TLS path against a real `*.supabase.co` host). It is **blocked on decision
D32**, which defers creating the hosted Supabase project until after the proof (Phase 5). Per the plan's own Phase 0
table, nothing in Phases 1–4 depends on it.
