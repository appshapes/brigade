# Write an adapter

Everything you need is in this repository, in this order:

1. **[docs/adapter-authors.md](../../adapter-authors.md)**, the authoring guide. Its "Start here" section carries
   `describe` and `session list` end to end; three independent implementers built both from that page alone and
   passed the suite's first four cases. Read it before the spec.
2. **[docs/protocol-v1.md](../../protocol-v1.md)**, BAP/1, frozen. How you authenticate, store credentials,
   represent membership or transport events, the shape of your identifiers and your profile file are yours
   (section 4.8); a polling adapter omits `message.watch.push`, an adapter whose membership is managed elsewhere
   omits `team.join` (section 4.7).
3. **`internal/adapters/fs/`**, the reference adapter: a complete BAP/1 implementation over a directory on disk,
   insecure and test-only by design. [Its README](../../../internal/adapters/fs/README.md) documents the store
   layout; it is the worked example the guide walks through. **`internal/adapters/syncthing/`** is the same
   layout shipped (card 141): one folder per team that the engine of `internal/syncthing` — shared with the sync
   adapter — replicates between the members' machines, with one writer per path, a join by device acceptance and
   the three `team.admin` verbs. [Its README](../../../internal/adapters/syncthing/README.md) has the differences.
   The binary bundles two adapters, `brigade adapter supabase` and `brigade adapter syncthing`; both run the suite
   in `make test`, the second in its local mode.
4. **`brigade-conformance`**, the arbiter. `make build`, then
   `bin/brigade-conformance --adapter /abs/path/to/your-adapter` (`--shared-env <VAR>` if your backend is a
   directory every test principal must share, `--setup <cmd>` if principals are provisioned out of band). 49
   cases, three principals in two teams. An adapter is correct when the suite is green.

Facts worth knowing: the lease range is each adapter's own and is advertised in `describe`; the suite reads no poll
interval, so a polling adapter gets the same 5 s delivery deadline as a push one; the project's team file names the
adapter (`"adapter": "<name>"`) and a member's `adapters.json` resolves the name to a command, while the session
option `adapter_command` may override it. `docs/allowed-deps.txt` binds only the shipped `brigade` binary, not
your adapter.

What we ask of you: read the frozen core (`describe`, `session *`, `message *`, the semantics of 4.5) against
your backend and tell us, naming section numbers, whether there is anything you cannot express without GoTrue,
PostgREST and Realtime. A real gap is a BAP/1.x question.

Brigade integrates with the harness, not with a model: a member is a session, whatever model answers in it.
