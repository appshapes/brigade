# Leave the team

Every step is optional, and each is for every checkout of the team on this machine: the credential is per team
per machine, not per checkout. Steps run with `!` inside a session or in your own terminal.

## 1. Leave

```sh
brigade team leave
```

Closes your open sessions in that team and marks your membership revoked, so teammates stop seeing your sessions
at once and messages addressed to them wait for retention rather than being delivered. Skip it and your membership
stays active indefinitely, while your sessions merely show as offline after the lease expires. A rejoin with the
current secret re-activates the same membership and keeps the same principal, so this step is reversible.

## 2. Revoke and delete the credential

```sh
brigade team reset
```

Revokes the credential family at the backend and deletes the local credential. **Run step 1 first**: after a
reset there is no credential left on this machine, so the membership and its sessions can no longer be closed
from here, and a later rejoin mints a new principal that teammates see as a new person.
`brigade team revoke-credentials` revokes at the backend and keeps the binding, for a credential you think has
leaked. Both refresh the credential first, so "revoked" is true even if the local copy had gone stale.

## If you created the team

Step 2 ends secret rotation, member revocation and transfer for that team, permanently. Hand the team to another
active member **before** step 1:

```sh
brigade team transfer --principal <ref>     # in your own terminal; the ref from `brigade team members --json`
```

Step 1 alone is recoverable: `created_by` survives a leave, so a rejoin with the current secret restores your
administration. Keep a 0700 backup of the credential directory either way
([Where the authority lives](../../team-admin/members/where-the-authority-lives.md)).

## 3. Remove the state and the cached binaries

```sh
rm -rf ~/.local/state/brigade ~/.local/share/brigade
```

Session maps, pidfiles, held-message records, logs, the Syncthing instance and every cached binary. Keep
`~/.local/state/brigade/sessions/by-native` if a later reinstall should resume your old Brigade sessions. Use the
`XDG_STATE_HOME` and `XDG_DATA_HOME` equivalents if you set those (a Muse Code or Codex CLI session ignores them).

## 4. Remove the credential store

```sh
rm -rf ~/.config/brigade
```

Or the directory named by the `config_dir` option. Do this **only after step 2 for each team**: a deleted
credential file whose family was never revoked stays usable by anyone holding a copy of it.

Removing the package itself is [Uninstall from Claude Code](claude-code.md), [Uninstall from Muse Code](muse-code.md)
or [Uninstall from Codex CLI](codex.md).
