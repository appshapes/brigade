# 11. Risks and mitigations; open questions

Live: Phases 4–6 and the testing strategy are still executed from this text; corrections below.

## Corrections recorded in the execution log

None recorded.

<!-- verbatim from the one-file plan -->
## 11. Risks and mitigations; open questions

### 11.1 Risks

| # | Risk | Likelihood / impact | Mitigation |
| --- | --- | --- | --- |
| R1 | Claude Code changes the socket protocol, the preamble, the registry file, the Bash tool's environment or `PATH` handling for plugin `bin/`, `permission_mode` semantics or the wrapper regex in a release (weekly releases; all but the socket frame and the `bin/` statement are undocumented). | medium / high | Pin the tested version in docs; the hook logs the registry `version`; the E0 driver scripts are promoted to `scripts/experiments/E0-<n>/` when each experiment closes and stay runnable as a regression kit (the probe plugin, the fake `brigade` and the Go lab they depend on are committed under `docs/research/` by P0-2); fallbacks already designed (name from `basename(cwd)`, socket path from env, `idle` state; `not_registered` with a clear message when the map is missing); U-03 validates the frame with Brigade's own parser. |
| R2 | The model replies through native `SendMessage`, ignores the reply instruction, or reaches the binary by an evasive form. | medium / medium | E0-3 gate on D19; the instruction is repeated in the frame and the skill; a deny rule on `SendMessage` is documented as an option; the evasive forms are a corpus item (the argv[0] tripwire was dropped as unworkable, 6.4). |
| R3 | Broadcast-from-DB policy, `realtime.topic()` or the Phoenix framing behaves differently hosted than locally, or a Realtime release changes the `vsn=1.0.0` wire format. | low / medium | E0-2 locally, P5-1 hosted; the drain timer keeps delivery working without Realtime; `postgres_changes` fallback verified live; the `2.0.0` binary decoder is kept in the tree behind a test-only switch. |
| R4 | Refresh-token reuse detection locks a profile out when two processes race, or the sandboxed in-memory refresh breaks the persisted family. | low (measured: concurrent refreshes get the same token) / high (rejoin) | E0-6 soak with the flock and the in-memory case; terminal error handling with one re-read-and-retry; rejoin is cheap. |
| R5 | `accept` everywhere lets a teammate's (or a secret holder's) message reach an unattended bypass, auto-mode or `-p` agent. | medium / high | D18 is a user decision under the empowering-by-default principle; the frame, the preamble and the skill are the controls; the corpus run each release; `refuse` is one setting away; `hold` with a terminal-only release ships in Phase 5 for cautious teams; the join secret is the boundary and is documented as such. |
| R6 | `-p` idle wake cannot be automated. | medium / low | E0-4; fallback is a recorded interactive run. |
| R7 | The first-use download does not finish inside the `SessionStart` hook budget on a slow link, so the first session starts without team messaging. | medium / low | E0-8 (a) measures with a throttled server; the script is idempotent and the next hook or Bash call retries; the background-download variant is already designed (6.2) and replaces the synchronous one if the measurement says so. |
| R8 | Free-tier hosted project pauses after a week idle. | high for hobby teams / low | `unavailable` with `project_paused`; docs recommend Pro or self-hosting for quiet teams. |
| R9 | Prompt injection succeeds despite framing. | medium / high | Section 10; small bodies; the P0-1 corpus run each release under the 9.6 pass rule; the opt-in gates; documentation. |
| R10 | `SessionEnd` budget (1.5 s) too short for `session close`. | high / low | Fire-and-forget with a 1 s cap; lease expiry is authoritative; the watcher also closes on PID death; a plugin `timeout: 5` may raise the budget (E0-5 (h)). |
| R11 | A `plugin/bin/checksums.txt` or `VERSION` that does not match the source or a published release is committed. | medium / medium | `checksums-check.sh` in the fast job on every commit; the release job verifies goreleaser's build against the committed file before publishing and deletes the draft otherwise; `make release` is the only sanctioned writer. |
| R12 | Time: Phase 2 and the conformance suite are the long poles. | medium / medium | The fs adapter lets Phase 3 start as soon as Phase 1 is done; Phases 2 and 3 overlap. |
| R13 | The static binary cannot verify TLS on a teammate's machine (no CA bundle in a minimal Linux container; the macOS sandbox blocks Security.framework). | medium / low | Embedded Mozilla roots with `x509usefallbackroots=1` (verified inside the sandbox); `SSL_CERT_FILE` honoured; the error maps to `unavailable` with the CA hint. |
| R14 | `refuse` sessions accumulate 60 unacked messages and every sender then sees `recipient_inbox_full`. | medium / low | `inbound` is visible in `brigade sessions` and the skill says not to message refusing sessions; retention drains after 7 days; the trade-off (honest signal vs silent drop) is deliberate. |
| R15 | Another `brigade` earlier on the user's `PATH` shadows the plugin's (the plugin `bin/` is appended last). | medium / low | The `SessionStart` hook warns; `docs/setup.md` recommends the symlink to the plugin's bootstrap rather than a separate install; a Homebrew formula, if ever added, would be the plugin's pinned version too. |
| R16 | The Bash sandbox breaks the session-bound commands (loopback refused for the local stack; writes denied under the home directory; TLS). | high with the sandbox on / low | Designed around (6.12): no writes outside `$TMPDIR`/cwd, in-memory refresh, embedded roots, proxy variables passed through; the hosted domain entry documented; loopback measured by E0-8 (c); the proof runs without the sandbox otherwise. |
| R17 | The hand-rolled clients drift from GoTrue/PostgREST/Realtime behaviour that supabase-js would have tracked (new error codes, a changed serializer default). | low / medium | The integration suite runs against the pinned local images and, from P5-1, the hosted project; every mapping is table-driven with an `internal` fallback; the realtime-js and auth-js sources are the reference and are re-read at each Supabase CLI bump. |

### 11.2 Open questions for the discussion (numbered; each with a recommendation)

1. **D18 inbound default.** Decided: `accept` everywhere. Remaining question: should `hold` with the terminal-only release (P5-9) ship before the first public release, or after? Recommendation: after, unless the Phase 4 corpus run produces an open finding on a config-edit or exfiltration item, in which case P5-9 moves before P5-10.
2. **D20 outbound gate.** Decided: `off` by default, the ask rule as `on`. The argv[0] tripwire an earlier draft attached here is deleted, not deferred: after the bootstrap's POSIX `exec "$target" "$@"`, argv[0] is the cache path on every legitimate call — byte-identical to the evasive full-path form — so no keying of the check can discriminate [verified today, 6.4]. The limitation is documented and the skill plus the corpus item are the control.
3. **D3 second adapter and conformance suite in Phase 1.** Decided: yes.
4. **D32 hosted project timing.** Decided: after the proof. Free vs Pro: Pro or self-hosting for any team that goes quiet for a week.
5. **D19 frame.** `<brigade-message>` (recommended, gated by E0-3) or the native wrapper with `from-name` only? Decided by E0-3; the `did:` variant is never shipped.
6. **Roster visibility (D22).** Decided: every active member sees the roster; `team members` ships in Phase 2.
7. **Join limiter (D6).** Decided: per-principal only as the hard layer, with the per-team failure count as an advisory log/notice; no project-wide hard limit.
8. **Join secret stored locally for automatic rejoin (D5).** Recommendation: no.
9. **`not_found` SQLSTATE.** Keep `P0002` (HTTP 500 from PostgREST, mapped correctly on the body) or switch to a custom `PT404` code so gateway logs do not show 5xx for routine not-found answers? Recommendation: keep `P0002` for v1 (the mapping is body-first and tested); revisit when hosted logs are looked at in P5-1.
10. **Who can rotate and revoke (Phase 5).** Decided (D22, and in the brief): creator only, including `transfer_team`; administrative roles stay unfrozen (section 12). The creator's profile directory is the team's only administrative credential and should be backed up (5.10).
11. **Anonymous-user cleanup (P5-3).** 7 days without a membership (recommended) or 24 h?
12. **JWT expiry.** 3600 s in v1 (recommended); measure revocation lag in P5-2 before shortening. Note that the `access_token` push on every refresh already re-runs the topic policy, so the lag for an open channel is bounded by the refresh margin, not only by the expiry.
13. **Release rehearsal unknown (P2-12).** Does `GORELEASER_CURRENT_TAG` with `--skip=validate` accept a tag that does not yet exist as a git object? Recommendation: settle it on a scratch tag before the first release; the design works either way. (The former second half — whether `checksums.txt` lists binary-format assets under the `name_template` names — is settled: the dry run showed it does [verified, A.7].)
14. **First-use download inside the hook budget (E0-8 (a)).** Synchronous with `timeout: 60` (recommended, simplest) or the background variant? Decided by the measurement.
15. **Bootstrap cache location.** The brief said `${CLAUDE_PLUGIN_DATA}/bin/`; the Bash tool never sees that variable, so the plan uses `${XDG_DATA_HOME:-~/.local/share}/brigade/bin/` (D35). Alternative: have the `SessionStart` hook export the data path through `CLAUDE_ENV_FILE` (verified to reach the Bash tool today) so the cache could live under `CLAUDE_PLUGIN_DATA` after all; rejected because a human's terminal and a disabled hook would then have no cache; `CLAUDE_ENV_FILE` itself is fine — documented for `SessionStart` hooks and observed to reach the Bash tool [verified today: https://code.claude.com/docs/en/hooks] — but those two reasons stand on their own.
16. **Harness receiver limits for socket posts.** Do Claude Code's per-sender rate limit and repeat suppression apply to posts with no native `from`, and on what key? Recommendation: assume no; the watcher's limits are primary; E0-3 (d) measures.
17. **Non-interactive and SDK hosts.** Injected messages do not appear as `stream-json` events; recommendation: out of scope for the plugin; note for a future Brigade-owned launcher using SDK `origin` framing.
18. **Coverage floor.** None in v1 (two numbers are reported: the unit profile and the `GOCOVERDIR` data of the process-boundary runs; merging them into one number has [uncertain] semantics for overlapping blocks). Recommendation: decide after Phase 3 whether to gate `internal/protocol` and `internal/harness/inbound` at 90 %.
19. **`macos` CI job scope.** Everything under `go test ./...` for now; trim to `internal/harness/...` and `cmd/brigade` if the job exceeds five minutes (arm64 minutes cost more).
20. **Homebrew tap and macOS amd64.** Add goreleaser `homebrew_casks` for the human install path in Phase 5? Ad-hoc sign the darwin/amd64 binary in CI (`codesign -s -`) although nothing quarantines a curl download? Recommendation: both deferred to P5-10; record the amd64 execution result on real hardware first.
21. **`encoding/json/v2` for the adapters' stdin reader.** Already the choice (7.3); the open half is whether to keep the explicit `utf8.Valid` check as belt and braces. Recommendation: keep it for one release.
22. **Should CI run the LLM proof?** Decided: no (cost, non-determinism, no login in CI); local only, results recorded in `.context/plans/brigade-proof-results.md`.

Researcher questions resolved by this plan without a user decision: `vsn=1.0.0` for the Phoenix client (all-JSON frames; the `2.0.0` binary decoder kept behind a test switch); no `toolchain` line in `go.mod`; `tools.mod` for dev tools; raw binaries rather than archives as release assets; `plugin/bin/VERSION` as the pin rather than a line inside the script; explicit per-function revokes instead of the no-op default-privileges statement; `enable_signup = true` required (GoTrue source); `realtime.send` argument order (verified); heartbeats and the Realtime quota (assume they do not count; irrelevant at proof scale); `CLAUDE_ENV_FILE` (observed to work, not used); `CLAUDE_CONFIG_DIR` injection (inherited, fallback `~/.claude`); Docker 29 vs CLI 2.116 (settled by the afternoon stack); pgTAP `auth.users` columns (E0-1 (d)); hook stderr (never shown to the model, per the docs).

### 11.3 Defects the judges found in the candidates, and how this plan avoids them

- Wrong cross-reference for the token-refresh experiment: here the refresh soak is E0-6 and D23 names it.
- Resume by name or by "most recent dead PID" could capture a live session of the same principal: dropped; resume only by Brigade id from the by-native map, ownership checked server-side, uniform `not_found` otherwise (D9).
- "Deliver on next prompt" is not a human gate: `hold` releases only through a terminal command the model cannot run (`brigade inbox release` refuses inside a session; D18, 3.6).
- `SessionEnd` timeouts of 2 or 5 s misleadingly suggest more than the 1.5 s budget: the hook assumes 1.5 s; the docs' rule that a longer per-hook timeout raises the budget is recorded with its [uncertain] plugin applicability (3.8, E0-5 (h)).
- `brigade@inline` vs `brigade-inline`: both spellings and their contexts are stated (3.2).
- Per-call random idempotency key plus Supabase-only body-hash dedupe: deterministic `brigade send` key, no server body-hash suppression (D11).
- E0-2 "async SessionStart" contradicting a synchronous hook: the hook is synchronous everywhere (6.3, E0-5).
- Native wrapper with `from="did:brigade:…"` as the default: never emitted (D19).
- Bash-run `inbox-release` gated only by skill text, and `CLAUDE_PLUGIN_ROOT` unverified in the Bash environment: the release path is now a terminal-only command with a mechanical refusal inside sessions, and the Bash environment was measured (no `CLAUDE_PLUGIN_*`, `CLAUDE_PID` present, plugin `bin/` on `PATH`), which is exactly why the CLI relies on `bin/` and the by-pid map (D34, 6.4).
- Acking held messages, and refusing while acking: `hold` and `refuse` never ack (D10, 6.8).
- `register_session` with a rowtype in a multi-item `INTO`: single `returning * into v_row` (5.4).
- `send_message` raising `23505` inside its own `unique_violation` handler: the conflict uses `P0001`, and the handler wraps only the insert (5.4).
- `rejoined` always true: captured from `FOUND` before the upsert (5.4).
- `envelope()` on a `record` alias: whole-row reference `x.m` of type `brigade.messages` (5.4).
- `adapter_path` as a single string cannot express a command with fixed arguments: `adapter_command` accepts a JSON array (D26).
- Pair-loop rule (8 hops per pair in 10 minutes) trips on ordinary exchanges: dropped; rate limits and the hop cap remain (D17).
- Makefile as an outline only: full recipes (7.4).
- `[api] schemas` dropping `graphql_public`: kept (5.9).
- Plain-text delimiter frame forgeable by a body: the tagged frame and a sanitiser that neutralises its own tag (6.7).
- Security lints, dependency audits and lockfile discipline deferred to Phase 5: in CI from Phase 1-2 (D30).
- Project-wide join limiter as a join-DoS vector: dropped (D6).
- `unauthorized` for unknown recipients, and distinguishable `unauthorized`/`not_found` on `session register`, `heartbeat`, `close`: uniform `not_found` on every verb (D15).
- Lease check on `send_message` turning a dead watcher into a misleading `unauthorized`: no lease check; a send touches the lease (D12).
- Address changes on `/clear` stranding accepted messages: the Brigade session is per process and survives `/clear` (D9).
- `describe` making a network call at startup: `describe` is strictly local (C-01).
- `team join` required by the protocol core: a capability (4.2).
- The fs adapter shipped in the production plugin with a "try it offline" path: never shipped; tests reference it through `adapter_command` (7.1).
- A held store that churns every drain with no dedupe: nothing is stored beyond a bounded pending index; the server is the single source (6.8).
- A 30 s heartbeat process spawn: stdin commands on `message watch` as a capability with the one-shot fallback (D14).
- New in this revision, from the afternoon digests: a per-schema `alter default privileges … revoke execute` that revokes nothing (replaced by explicit revokes, 5.3); a status-first error mapping that would have reported every `not_found` as `unavailable` (body-first, 5.11); a `bufio.Scanner` NDJSON reader that would stop for good at the first overlong line (drop-and-continue, 7.3); a `toolchain` line that breaks every build (omitted, 7.2); a `ModeCharDevice` TTY test that is true for `/dev/null` (termios, 5.11); `os.UserConfigDir()` on macOS (own resolver, 3.2); `slog.Any` leaking a JWT through the redactor (banned, 7.3); a cache directory the Bash tool cannot see (D35).

---

