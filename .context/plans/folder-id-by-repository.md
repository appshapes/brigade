# Folder ids keyed by repository as well as path

Date: 2026-09-28. Status: **built and released as 0.16.0** (rows P24-1..P24-4 of the execution log). Owner:
Rjae. Trello card **42**. Written as a hand-off by a session of another repository's team, from the
measurements in §1; **verified against this tree and corrected on 2026-09-28** by the session that built it —
§10 lists what was wrong or missing, and the sections above it now read as corrected. Anchors are file:line at
**98cff90** (master, 0.15.0), and every one of them was found where the hand-off said.

The team and its two repositories are a customer's. This copy calls the repositories `<web>` and `<api>`; the
card holds the hand-off as it was written.

## 1. The problem, as it happened

One team spans two repositories, `<web>` and `<api>`, both carrying a byte-identical `.brigade.json`:

```json
{"team_ref":"f68f272b-…","team_name":"…", …, "sync":{"folders":[".brigade"]}}
```

Each repository has its own `.brigade/` folder with its own contents (team config imported into the sessions'
context, a README, a Makefile, a private skill, patches). On a machine that holds both checkouts, only one of the
two folders syncs. Measured on Rjae's Mac, 2026-09-27, Brigade 0.13.0:

- `brigade sync status` in the `<web>` checkout lists one folder, `<web>/.brigade`, state `idle`.
- In the `<api>` checkout it lists `<api>/.brigade` as `conflict_path`, and the session-start line says
  `1 folder is held by another checkout`.
- Syncthing's config holds the folder id `brigade-f68f272b-c66a6dbc342c` at the `<web>` checkout's path.
  `sha256(".brigade")` starts `c66a6dbc342c`: the id carries the team and the relative path and nothing about the
  repository, so both checkouts ask for the same id and Syncthing binds it once.

Consequence: everything a session writes into `<api>/.brigade/` exists only on that machine. Teammates' `<api>`
checkouts never receive it and their edits never leave. The team copies that folder by hand. Rjae's decision,
2026-09-28: fix it in Brigade ("option 1"), rather than renaming the folder in `<api>`'s file or nesting
`<api>`'s files under the `<web>` repository's synced folder.

**A second consequence the hand-off did not name** (read from the code on 2026-09-28, not measured): the one id
is shared between machines too. When two members each hold both repositories and their instances hold a
different one of the two — one machine's `<web>` session shared the folder first, the other machine's `<api>`
session did — each instance shares the id with the other's device, because each member also runs a session of
the other repository that is on the roster with its device. Syncthing then syncs one machine's `<web>/.brigade`
with the other's `<api>/.brigade`. Whether the team met it depends on which session started first on each
machine.

## 2. What the code did at 0.15.0

- **The id.** `internal/harness/foldersync/foldersync.go:162-176` — `FolderID(teamRef, folder)` is `"brigade-"`
  + the first 8 characters of `team_ref` without dashes + `"-"` + the first 12 hex digits of
  `sha256(folder)`. The doc comment says every checkout of *the repository* reads the same file, which is true,
  and silently assumes one repository per team.
- **The label.** `foldersync.go:178-190` — `Folders(teamRef, root, workspaceLabel, folders)` builds
  `{id, path: root/folder, label: workspaceLabel + "/" + folder}` (the folder alone when the session shares no
  workspace label).
- **Peers are already scoped by repository.** `internal/harness/watch/sync.go:272-300` — `syncPeers` keeps only
  the teammates' sessions with the **same `workspace_label`**, and its comment says why: "the same repository,
  so two repositories of one team never share a folder id". The peer side of the design assumed repository
  scoping; the id side never got it. That is the whole bug.
- **Where the repository name comes from.** `internal/harness/hook/start.go:500-518` — `workspaceLabel` is the
  plugin option `workspace_label` when set, else `teamfile.RepoName(top)`; `""` when `share_workspace_label`
  is off. `internal/harness/teamfile/reponame.go:31-40` — `RepoName` is the name in the `origin` remote's URL
  (else the first remote's), else the directory name, sanitised as a label. The same value is the roster's `REPO`
  column and what `sessions --here` compares (`commands/sessions.go:199,339`).
- **What the adapter left as it found it.** `internal/syncadapters/syncthing/rest.go:311-322,376-381,434-438` —
  at 0.15.0 `apply` removed nothing from the instance, and a folder id the instance holds at another path is
  reported `conflict_path`. `rest.go:532-550` — `folderName(prefix, id, label)` recovers a folder's name from
  its label by hashing each `/`-suffix of the label against the id; `formerFolders` uses it to prove a folder is
  this checkout's before pausing it. **This is what P18-6 built, not a rule** (§10.1).
- **Other id derivations.** `internal/harness/commands/sync.go:57-79` (`formerFolders`, proves an engine folder
  is this checkout's by recomputing its id from the path relative to `sync_root`) and `sync.go:149`
  (`Folders(m.TeamRef, m.SyncRoot, m.WorkspaceLabel, m.SyncFolders)` for `brigade sync status`).
- **The frozen members.** `internal/harness/sessionmap/bypid.go:159-161` — `sync_adapter`, `sync_folders`,
  `sync_root`, frozen at SessionStart by `hook/start.go` `frozenSync`; a change on the continue path respawns.
- **Documented as a limit, not a bug.** `docs/sync.md:267-273` "One checkout per folder id on a machine" names
  "another repository of the team that lists the same path" as a case; `docs/sync-adapters.md:157-167` "Folder
  ids" states the derivation for adapter authors. Folder ids are not on the wire (`docs/protocol-v1.md`
  mentions only `sync_peer`), so BAP/1 is untouched by any of this.

## 3. The change

**Key the id by repository and path.** The hash input is `<scope> + "/" + <folder>`, where `scope` is the
repository name `teamfile.RepoName(top)` — the value the peer rule and the roster already use, the same on every
teammate's checkout because it comes from the shared `origin` URL. The id keeps its shape,
`brigade-<team8>-<hash12>`, so nothing that parses ids by prefix changes.

1. `foldersync.FolderID(teamRef, scope, folder string)`; `LegacyFolderID(teamRef, folder)` keeps the 0.11.0
   derivation for the upgrade in §4, and is what an empty scope gives. `Label(scope, folder)` is
   `scope + "/" + folder`, whatever the session's `workspace_label` option says. `Folders(teamRef, root, scope,
   folders)` builds id, path, label and `Replaces: LegacyFolderID(teamRef, folder)` when that is another id.
2. **A new frozen member** `sync_scope` in the by-pid map (`bypid.go`, `omitzero`; `ValidSyncScope`: a label
   sanitising leaves unchanged, no `/`, not `.` or `..`), set in `resolveSync` from `teamfile.RepoName(top)`
   beside `sync_root`; a change is a respawn reason like the other three. The hook asks `ValidSyncScope` before
   it freezes a name, so a name the map would refuse is left out instead of costing the session its map. The
   watcher and `brigade sync status` read it from the map. `workspace_label` is not reused for the id: it is a
   per-machine option a member may set to anything, and an id must be the same on every checkout without
   exchange. `share_workspace_label` off does not blank it either: the scope is sent to nobody but the engine.
3. **The adapter reads the name from the label.** The id hashes the whole label, so hashing cannot say which
   `/` parts the repository from the folder — every split hashes alike (§10.2). The name is what follows the
   label's **first** `/`, because a repository's name has none, and a label that reads this way is read this way
   only. A folder shared before 0.16.0 is still read the old way (the name alone hashes to the id), so an
   instance's older folders are still known.
4. **Peer selection stays on `workspace_label`** (`watch/sync.go`). It already scopes peers by repository, and a
   member who sets a custom label already isolates their session's peers today; this change does not alter
   that. The documents say so.

What this does and does not fix: two *different* repositories of one team on one machine each get their own
folder ids and both sync. Two *clones of the same repository* on one machine still collide (same scope, same
folder), which is correct — they are the same folder — and stays a documented limit. Two different repositories
whose `origin` slugs are the same name still collide; a checkout whose name differs from its teammates' (a fork,
no remote and another directory name) derives other ids and syncs with nobody. Both are in `docs/sync.md`.

## 4. Upgrading a team that already syncs

Every existing folder id on every team changes with the derivation. Without help, the first session on 0.16.0
posts the new id at a path Brigade's instance already holds under the old id and gets `conflict_path` — every
synced folder of every team stops until someone removes the old folder in the web GUI. Two parts:

- **The adapter moves the folder.** The `apply` request's folder gains an optional member `replaces` (the legacy
  id, from the harness). When the instance holds a folder under `replaces` at the desired `path`, and removing it
  lets the new id take the path (the new id is not held at another path, no third id holds the path), the
  bundled adapter removes that entry (`DELETE /rest/config/folders/<replaces>`) and posts the folder under its
  new id **with the devices the old entry had**, so a server a person added by hand comes along (§10.3). The
  old id at another path is another checkout's and stays. A removal that fails leaves the folder syncing under
  its old id, reports `conflict_path` and is tried again at the next round. An external adapter that does not
  know `replaces` ignores it; `docs/sync-adapters.md` now says that of every unknown member; `protocol_version`
  stays `sync/1`.
- **Measured with Syncthing 2.1.5 (2026-09-28), correcting the hand-off:** removing a folder's entry removes no
  file but **does remove the `.stfolder` marker**; adding the folder again puts it back. `DELETE` answers 200
  for an id that is not configured. Syncthing **accepts two ids at one path** — "one path, one id" is this
  adapter's rule (`place`), not the engine's.
- **Every member upgrades before the team's folders converge.** Old-version peers keep sharing under the old
  id, new-version peers under the new one; the two groups do not exchange until the last member updates the
  plugin. **Measured between two instances** (`TestRealSyncthingMovesASharedFolder`), in step 11 s after the
  second one's upgrade: a file both had alike is left alone, with no conflict copy; a file written on either
  side in between reaches the other; a file changed on one side wins and the older copy is kept as a
  `.sync-conflict-` copy; **a file deleted on one side in between comes back** from the other. The hand-off's
  "nothing is lost" holds; "what follows is hashing, not transfer" holds for files that did not change. The
  changelog and `docs/sync.md` say all four.

## 5. Files touched

| Area | File | Change |
| --- | --- | --- |
| id | `internal/harness/foldersync/foldersync.go` | `FolderID(teamRef, scope, folder)`, `LegacyFolderID`, `Label`, `Folders(teamRef, root, scope, folders)`; `Folder` gains `Replaces` (`omitzero`) |
| map | `internal/harness/sessionmap/bypid.go` | `SyncScope` (`sync_scope`, `omitzero`), `ValidSyncScope`, validation |
| hook | `internal/harness/hook/start.go` | `frozenSync.scope`, `syncScope(cwd)`, `frozenIn` compares it, `buildMap` writes it |
| watcher | `internal/harness/watch/sync.go`, `watch.go` | `syncSetup.scope`; `syncApply` derives from it |
| status | `internal/harness/commands/sync.go` | scope into both derivations; a folder under its earlier id is known; a listed folder's earlier id is not shown as dropped |
| adapter | `internal/syncadapters/syncthing/rest.go`, `run.go` | `replaced`, the move in `apply`, `folderName` (both derivations), `formerFolders` counts `replaces` as requested; `folderSpec.Replaces` |
| tests | `foldersync_test.go`, `sessionmap/bypid_test.go`, `hook/sync_test.go`, `watch/sync_test.go`, `commands/sync_test.go`, `cmd/brigade/testdata/script/sync.txtar`, `syncthing/run_test.go`, `syncthing/real_test.go`, `e2e/sync_test.go` | §6 |
| docs | `docs/sync.md`, `docs/sync-adapters.md`, `docs/security.md` §12, `plugin/README.md`, `CHANGELOG.md` | the derivation, "Updating to 0.16.0", the limits, `replaces`, and the "only adds" passages reworded (§10.1) |
| plans | `.context/plans/folder-sync.md`, `.context/plans/feature-ideas-2026-09-24.md` | a dated note each: "only adds" was what P18-6 built, not a rule |
| log | `.context/plans/brigade-execution-log.md` | rows P24-1..P24-4 |

Not touched: `internal/protocol`, `docs/protocol-v1.md`, `docs/protocol-v1.schema.json`, the conformance suite,
both backend adapters, `supabase/` — nothing here is on the wire.

## 6. Proof

1. **Unit and script tests**, in `make test`. Seen failing under four mutations, each restored: `FolderID`
   ignoring the repository (foldersync, commands, watch); the adapter never moving a folder
   (`TestApplyMovesAFolderToItsNewID`); a listed folder's earlier id counted as dropped
   (`TestApplyMovesNothingItCannotPlace`, three of its cases); the label's scoped reading dropped
   (`TestFolderNameIsProvenByTheID`, `TestApplyPausesAFolderTheProjectNoLongerLists`).
2. **The two-repository case with the real binaries** (`BRIGADE_TEST_SYNCTHING=1`,
   `TestTwoRepositoriesOfOneTeamSyncApart`): two personas, each with one instance and a checkout of each of two
   repositories carrying the same team file and the same `sync.folders`. Four sessions; two ids, the same on
   both machines; all four folders held at their own paths, none `conflict_path`, no notice of a folder held by
   another checkout; a file written in one repository's folder reaches the teammate's checkout of that
   repository and neither checkout of the other; an instance runs until the last of its two sessions ends.
3. **The move on a real instance** (`TestRealSyncthingIntegration`): the legacy-id folder at the path is
   replaced with its hand-added server, its file intact and its marker back; the legacy id of another folder at
   a second clone's path is still there.
4. **The move between two machines** (`TestRealSyncthingMovesASharedFolder`): §4's four measurements.
5. **The live case**, after the release and after every member has run `/brigade:update`: on Rjae's Mac,
   `brigade sync status` in `<api>` lists `<api>/.brigade` as `idle` and the `<web>` checkout's folder as
   `idle`, with different ids; a file written into `<api>/.brigade/` on Rjae's Mac appears on a teammate's within
   a round. Then the team's private README drops its "pending" note and its "copy by hand" instructions. **Not
   done by the building session**: it is the team's own repositories and machines. Before it, the team should
   compare the two `.brigade` folders across machines for the second consequence of §1.

The real-Syncthing tests do not depend on discovery: a Syncthing already running on the machine — a developer's
own Brigade instance — holds the local discovery port, and two throwaway instances then never find each other
(measured: no connection in four minutes). Each test gives its instances the other's loopback address by hand,
which the adapter keeps; the e2e does it after Brigade has introduced the devices from the roster.

## 7. Release

0.16.0. `make release version=0.16.0 card=42`; verified as P23-7 was; both of Rjae's Claude configurations
updated; the card commented. No migration on the backend.

## 8. Out of scope, on purpose

Renaming a synced folder per repository in the team file (Rjae ruled it out); moving the peer rule off
`workspace_label`; showing `workspace_label` in human output (E6's open item); a team-file `sync.scope` member
that overrides the derived name (add it later if a team ever has two repositories with the same `origin` slug
— nothing in this plan prevents it: the scope is one string, wherever it comes from).

Seen while building, not built: a folder held under the id of a **team that was created again**
(`team create --force`, `docs/sync.md`) still holds its path until someone removes it in the web GUI. The move
of §4 would serve it, given the old team's reference.

## 9. The two questions the hand-off left open

Rjae handed the work over with "proceed autonomously" and both questions carried a recommendation. Both
recommendations were taken:

- **The scope's source** is `RepoName` (origin slug, directory name as fallback): zero configuration, and it
  fixes every team that already carries one file in two repositories.
- **The adapter moves the folder by itself** (§4). The manual route would have stopped every synced folder of
  every team on the day of the upgrade.

## 10. What the verification corrected

The hand-off's author was not of this repository. Every file:line anchor was right. These were not:

1. **"Only ever adds" was never a rule.** The hand-off read `rest.go`'s comment, `docs/sync-adapters.md` and
   `docs/security.md` §12 ("Brigade only adds") as an invariant, and asked for the move to be documented as
   "the only removal the adapter ever performs … the one exception". Owner, 2026-09-28: Brigade is features
   first and open by default, restricted by configuration alone; the context that read otherwise is to be
   corrected. Corrected: the `apply` comment, `docs/sync.md`, `docs/sync-adapters.md`, `docs/security.md` §12,
   the changelog, and a dated note in the two plans that carried the wording. They now say what the adapter
   does and why — it keeps a hand-added device because a team set it up — and that the `sync` option is the one
   switch. The move is described as what the adapter does, not as an exception to anything.
2. **Hashing cannot prove the label's split.** The hand-off had `folderName` try each `/` of the label and
   match `sha256(scope + "/" + name)`. With the label `scope/name`, that text is the label itself at every
   split. §3.3 says what is done instead.
3. **The move would have dropped a hand-added server.** The hand-off posted "the new folder as it does today",
   whose device list is the devices the *new* id already has — none — plus the peers. The old entry's devices are
   now carried.
4. **`.stfolder` does not stay** when a folder's entry is removed (§4). Nothing depended on it.
5. **The existing e2e would have stopped syncing.** Its two checkouts have no remote and differently named
   directories, and relied on one `workspace_label`; with the id keyed by the repository's name they would have
   derived two ids. Both checkouts now have the same `origin`.
6. **"Nothing is lost" needed its two riders**: the conflict copy, and the deleted file that comes back (§4).
7. **The cross-machine consequence** of §1.
8. **`formerFolders` and a listed folder's earlier id.** Where the move is declined, the old entry sits at this
   checkout's path with a label that proves its name, and would have been paused as a folder the project
   dropped. The adapter and `brigade sync status` both count a requested folder's `replaces` as listed.
