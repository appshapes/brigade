# One team, many repositories — feasibility study

Date: 2026-09-10
Status: **chartered by Rjae 2026-09-10 as a study — measure and report, no code change.** Owner: Frank's session
(`frank-brigade-vscode`). Row P11-1 in the execution log (added when the log is free of the P10 edit in flight).
Tier: Opus (evidence collection).

## 1. The question, in Rjae's words

A real team often owns more than one repository — ThinkTech is the same people on `thinktech` (front end) and
`thinktech-api` (back end). Can one Brigade team span both, so a session in either repository is a member of the
same team, sees the same teammates and messages them directly? Rjae "really wants this" and also "does not want to
risk messing up something that is working so well". The fallback if it cannot be done safely is Claude Code's own
local cross-session messaging as a bridge between a Brigade session and a session in the adjacent repository.

## 2. What the code says today (measured 2026-09-10 — the reason this is a study, not a build)

- A team's identity carries no repository. `internal/harness/teamstore/teamstore.go:39` —
  `Key(adapter, url, teamRef) = sha256(adapter + "\n" + url + "\n" + teamRef)[:32]`. Two repositories that commit
  the same `.brigade.json` (adapter, url, publishable key, `team_ref`, `team_name`) compute the same key.
- The credential store is per team, per machine: `${configDir}/teams/<key>/team.json`. A second checkout on a
  machine that already holds the credential needs no secret — `docs/security.md` §4 states the concession outright:
  "a session can now pin a second checkout to a team this machine already holds a credential for".
- Pins are per checkout: `${configDir}/projects.json`, keyed by the checkout's real path
  (`teamstore.go:93-102`). Nothing limits a team to one pin.
- The team file is discovered at the git toplevel only (`internal/harness/teamfile/discover.go:9-44`). A session in
  `thinktech-api` reads `thinktech-api/.brigade.json`; a session in `thinktech` reads its own. If both files name
  the same team, both sessions register to it.
- The wire needs nothing. `SessionRegistration` already carries an optional `workspace_label`
  (`internal/harness/hook/start.go:151-153`, opt-in through the plugin options `share_workspace_label` and
  `workspace_label`), and `brigade sessions` prints it (`internal/harness/commands/format.go:113-115`). BAP/1 is
  frozen and must stay so; this is the existing member that tells repositories apart.
- The documentation assumes one project: `docs/setup.md` "The project owns the team" (line 99 on) is written for a
  single checkout; `plugin/README.md` and `docs/security.md` §4 follow it.

So the expectation going in is: **it already works; what is missing is proof, a join affordance, and words.**
The study exists to find where that expectation is wrong.

## 3. What to measure — nothing here changes a line of Go

1. **Two repositories, one team, on the fs-adapter rig.** Extend nothing that ships: in scratch, create a second
   throwaway git repository, copy the first repository's committed `.brigade.json` into it unchanged, `brigade team
   join` there (expected: secret-free, since the machine already holds the credential), open a session in each,
   and send both ways. Record: both sessions on one `brigade sessions` listing, delivery both directions, the
   by-pid maps' `team_key` equal, `projects.json` holding two pins for one team.
2. **The same on the live team**, once (1) is green: a throwaway second repository on this machine joined to the
   `brigade` team the same way — never ThinkTech's real repositories in this study; that is Rjae's own step after
   the report. Record the same four things plus `brigade whoami` in each.
3. **Telling repositories apart.** With `share_workspace_label` on and `workspace_label` set to the repository name
   in each checkout's plugin options, what a teammate sees in `brigade sessions`, in the frame's attributes
   (`from-label` — check whether the label is the human label or the workspace label; frame attributes are U-04
   fixed, so this is a reading, not a change) and in `whoami`. Is that enough for a sender to pick "the back-end
   session"? If not, say what is missing and whether it is a docs matter or a wire matter (a wire matter is out of
   scope and is reported, not built).
4. **The join affordance.** Today the second repository gets its team file by hand-copying `.brigade.json`. Is that
   acceptable as documented practice ("commit the same file in every repository the team owns"), or does a verb
   (`brigade team link <path>`, or `team create --into <other checkout>`) earn its place? Answer with the measured
   friction of step 1, not with preference. Note `team create` writes the file at the toplevel of the *current*
   checkout only (`internal/harness/commands/teamsetup.go`).
5. **What breaks.** Look for code paths that assume one checkout per team: the hook's drift line (a checkout whose
   file names a different team than the pin), `team reset`/`revoke-credentials` semantics when two checkouts share a
   credential (revoking in one revokes for both — is that said anywhere?), the proof scripts' single-checkout
   assumptions (`scripts/proof*.sh`, `scripts/experiments/`), and the retention/prune of pins. Each finding is a
   sentence with a file:line, not a fix.
6. **The workaround, for comparison.** One paragraph on Rjae's fallback — Claude Code's native cross-session
   messaging bridging a Brigade session to a session in the adjacent repository — stating what it does not give
   (cross-machine, durable, addressed by team) so the report's recommendation is a comparison, not an assertion.

## 4. Deliverable

`docs/experiments/E6-multi-repo.md`: the measurements of §3 with run transcripts sanitised as the E-series does
(ids hashed, no paths under a home directory), a one-line answer per §3 item, and a recommendation in one of three
forms — **works as-is, docs only** (list the sentences to change in `docs/setup.md`, `plugin/README.md`,
`docs/security.md` §4); **works with one small verb** (name it, its refusals, and why hand-copying is not enough);
or **does not work because of X** (file:line). Plus the row's evidence cell. No code lands from this study; if the
recommendation is a verb or a docs change, that is a separate row Rjae charters.

## 5. Constraints

- No change to `internal/`, `plugin/`, `supabase/` or the protocol in this study. BAP/1 is frozen; `workspace_label`
  is the identification member and must suffice or be reported as insufficient.
- Never ThinkTech's real repositories, never a real teammate's session as the receiver of a test message, never a
  second `.brigade.json` in this repository. Scratch under `.ignored/` or outside the tree.
- The join secret never touches argv, the chat, or a file under a project directory (`CLAUDE.md`); the live-team
  step must not need it — if it does, that is a finding about the second-checkout path.
- Commit the report and the row in one commit, `15: …` or Frank's ticket prefix as agreed; merges only.

## 6. Why this is low-risk to what works

Nothing ships. The one live measurement joins a throwaway checkout to the existing team through the same path any
member already uses for a second checkout. **Cleanup is local only — never `brigade team reset`,
`revoke-credentials` or `leave` from the throwaway checkout:** all three act at the backend on the credential or
membership that every checkout on that machine shares (`docs/setup.md` "Two commands end the credential"), so
any of them would end the *real* member's access, not the throwaway's. (Found by `frank-brigade-vscode` on
2026-09-10 before running it; the first draft of this section said `team reset`, which was wrong.) Undo the
throwaway by deleting its directory and removing its entry from `${configDir}/projects.json` by hand — a local
0600 file, edited while no `brigade` writer runs — or simply delete the directory and leave the orphaned pin,
which nothing ever consults. Note this as measurement 5's first finding: there is no checkout-scoped undo verb.
If step 1 fails on the fs rig, the study stops there and reports.
