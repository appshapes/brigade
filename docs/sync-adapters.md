# Writing a Brigade sync adapter

A sync adapter is an executable that keeps a project's folders in sync between the checkouts of a team, by driving
some sync engine (the bundled one drives [Syncthing](https://syncthing.net)). Brigade never carries a file byte. It
tells the adapter which folders this checkout shares and which teammates' peers to introduce — learned from the
team roster — and asks how it is going. Everything else is the engine's: discovery, transfer, conflicts, deletions.

This page is the whole contract. It is short because the protocol is: five verbs, one JSON document in, one out.
What file sync does for a team, with the bundled adapter, is [docs/sync.md](sync.md).

## Which adapter runs

The project's `.brigade.json` names the adapter by **name**, never by path:

```json
"sync": { "adapter": "syncthing", "folders": [".context/plans", "docs/shared"] }
```

`adapter` is optional and defaults to `syncthing`. A name matches `^[a-z0-9][a-z0-9_-]{0,31}$`. Brigade resolves it
on the user's machine:

| Name | Runs |
| --- | --- |
| `syncthing` | the bundled adapter: `<plugin binary> sync-adapter syncthing <verb>` |
| anything else, say `rsyncish` | `brigade-sync-rsyncish <verb>`, found on the user's `PATH` (the git-subcommand convention) |

An adapter that is not on `PATH` switches file sync off for that session, with one line to the user. The session
itself carries on.

## Invocation

One process per verb, no shell (the argv array is executed directly):

```
brigade-sync-<name> <verb>
```

- **stdin:** exactly one JSON document, the verb's request, then EOF.
- **stdout:** exactly one result envelope, in the shape of `docs/protocol-v1.md` 4.3:
  `{"ok":true,"protocol_version":"1","result":{…}}`, or on failure
  `{"ok":false,"protocol_version":"1","error":{"code":"…","message":"…","retryable":…}}`. The envelope's
  `protocol_version` is BAP/1's `"1"`; the sync protocol's own version is `describe`'s `protocol_version`.
- **exit status:** 0 on success; on failure the status of the error's code, as `docs/protocol-v1.md` 4.6 lists them
  (`unavailable` is 9, `invalid_input` 3, `usage` 2, `internal` 1, …).
- **stderr:** free-form diagnostics. Brigade appends it to `<state dir>/logs/sync-<name>.log` and never shows it to
  the model. Never write a secret there.
- **environment:** built from scratch — `PATH`, `HOME`, `TMPDIR`, `LANG`, `LC_*`, `XDG_*`, `CLAUDE_CONFIG_DIR`, the
  proxy variables and `SSL_CERT_FILE`/`SSL_CERT_DIR` when set, plus **`BRIGADE_STATE_DIR`**, Brigade's state
  directory. Keep your engine's files under it (the bundled adapter uses `<state dir>/sync/syncthing/`).
- **time:** 20 seconds per call. A call still running then is killed (SIGTERM, then SIGKILL 5 s later) and counted
  as `unavailable`.

Every verb is **idempotent**: Brigade repeats them freely.

## The verbs

| Verb | Request | Result |
| --- | --- | --- |
| `describe` | `{}` | `{"name","version","protocol_version":"sync/1"}` |
| `attach` | `{"state_dir","session_id","pid"}` | `{"peer":"<descriptor>"}` |
| `apply` | `{"state_dir","session_id","folders":[{"id","path","label"}],"peers":[{"peer","label"}]}` | `{"folders":[{"id","state"}],"peers":[{"peer","connected"}]}` |
| `status` | `{"state_dir"}` | `{"running","peer","folders":[{"id","path","state"}],"peers":[{"peer","connected"}]}` |
| `detach` | `{"state_dir","session_id","pid"}` | `{"stopped"}` |

`state_dir` is the same directory as `BRIGADE_STATE_DIR`. `session_id` is the Brigade session the call is for.
`pid`, in `attach` and `detach`, is **optional**: a positive process id, the process that holds the session's
reference (Brigade sends its watcher's), or absent. An adapter may ignore it. The bundled adapter records it with
the session's reference and, on every `attach` and `detach`, drops the references whose process has died without
a `detach` (a watcher killed with `SIGKILL`), so the engine does not outlive the last session; a reference with no
`pid` is dropped only by its own `detach`. An adapter written before `pid` existed ignores the member and stays
conforming.

### `describe`

Who you are. Must answer offline, fast, and without starting anything. `protocol_version` must be `"sync/1"`; any
other value switches file sync off for the session.

```
$ echo '{}' | brigade-sync-rsyncish describe
{"ok":true,"protocol_version":"1","result":{"name":"rsyncish","version":"0.3.0","protocol_version":"sync/1"}}
```

### `attach`

A session has started on this machine. Make sure the engine runs, count this session as one of its users, and
return this machine's **peer descriptor**: whatever a teammate's adapter needs to reach this machine's engine (for
Syncthing, the device id). Brigade publishes it in the team roster as the session's `sync_peer`,
`<adapter name>:<descriptor>` — so `syncthing:MFZWI3D-…` — and hands it, prefix stripped, to your teammates'
adapters of the same name. The whole `sync_peer` is at most 256 characters.

Brigade calls `attach` when the session's watcher starts and again before every later `apply`, so a reference lost
along the way comes back.

```
$ echo '{"state_dir":"/home/u/.local/state/brigade","session_id":"6f0f2b41","pid":48213}' | brigade-sync-rsyncish attach
{"ok":true,"protocol_version":"1","result":{"peer":"laptop-7.example.net:8022"}}
```

### `apply`

The whole desired set, every time: share these folders with these peers. Called right after the first `attach`;
then Brigade reads the roster every 15 seconds and calls `apply` again as soon as the teammates' peers change, and at
least every 60 seconds while the session lives.

- `folders`: the project's folders. `id` is the same on every checkout of the team (see *Folder ids* below), `path`
  is the absolute local path, `label` is for humans (`<repository>/<folder>`).
- `peers`: every teammate's peer the roster lists for the same repository and the same adapter, online or not —
  an offline peer is still worth introducing; the engine connects when it can. Never this machine's own. `label` is
  the teammate's roster label: unverified text, for display only.

Report each folder's state in your engine's own words (`idle`, `syncing`, `error`, …) and whether each peer is
connected right now. A peer or folder that is no longer listed may stay configured; removing it is not required.
`conflict_path` says this checkout cannot hold the folder because another checkout on the machine does — the same
folder id of a second clone of the repository. The bundled adapter only ever adds: a device, or a folder's device,
that someone configured by hand stays.

```
$ brigade-sync-rsyncish apply <<'EOF'
{"state_dir":"/home/u/.local/state/brigade","session_id":"6f0f2b41",
 "folders":[{"id":"brigade-6f0f2b41-46b42b4229cd","path":"/home/u/work/brigade/docs","label":"brigade/docs"}],
 "peers":[{"peer":"desk-2.example.net:8022","label":"bob@example.com"}]}
EOF
{"ok":true,"protocol_version":"1","result":{"folders":[{"id":"brigade-6f0f2b41-46b42b4229cd","state":"idle"}],"peers":[{"peer":"desk-2.example.net:8022","connected":true}]}}
```

Brigade tells the user `Brigade sync: <f> folders, <c> of <p> peers connected` whenever that summary changes,
followed by `; 1 folder is held by another checkout` (or `; <n> folders are held by another checkout`) when a folder
is `conflict_path`.

### `status`

What `brigade sync status` shows: whether the engine runs, this machine's descriptor, and every folder and peer the
engine knows — the engine is shared by every session and project on the machine, so this may include other
projects' folders; the human form shows only the project's own, and `--json` carries them all. Must not start the
engine. The bundled adapter answers a stopped engine with `{"running":false,"peer":"","folders":[],"peers":[]}`
rather than an error, and `brigade sync status` then prints `engine stopped`.

```
$ echo '{"state_dir":"/home/u/.local/state/brigade"}' | brigade-sync-rsyncish status
{"ok":true,"protocol_version":"1","result":{"running":true,"peer":"laptop-7.example.net:8022","folders":[{"id":"brigade-6f0f2b41-46b42b4229cd","path":"/home/u/work/brigade/docs","state":"idle"}],"peers":[{"peer":"desk-2.example.net:8022","connected":false}]}}
```

### `detach`

The session has ended. Stop counting it; stop the engine when no session is left (sync runs only while a session is
active). `stopped` says whether this call stopped it. Detaching a session that was never attached is not an error.

```
$ echo '{"state_dir":"/home/u/.local/state/brigade","session_id":"6f0f2b41","pid":48213}' | brigade-sync-rsyncish detach
{"ok":true,"protocol_version":"1","result":{"stopped":true}}
```

## Folder ids

Every checkout derives the same id for the same folder, without exchanging it:

```
"brigade-" + first 8 characters of team_ref with its dashes removed
           + "-" + first 12 hex digits of SHA-256(folder exactly as .brigade.json writes it)
```

Team `6f0f2b41-5a3c-…` and folder `docs` give `brigade-6f0f2b41-46b42b4229cd`. The team part keeps two teams apart
on one engine. Use the id as your engine's folder id, or map it to one.

## Failing

Answer a failure with an error envelope and the matching exit status; never a partial result. The message reaches
the user in one line (`Brigade sync: the rsyncish sync adapter is not usable (unavailable: <message>); file sync is
off for this session`), so make it say what to do — `rsync is not on PATH` — and keep secrets, paths to credentials
and file contents out of it.
