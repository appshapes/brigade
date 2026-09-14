# E8 — Which version of a plugin a Claude Code session loads

Card 23. Run 2026-09-14 on macOS, Claude Code 2.1.270, Brigade plugin versions 0.4.1 and 0.6.1 from the
`appshapes/brigade` marketplace. Driver: `scripts/experiments/E8-plugin-scope/run-session.sh`.

## Why

Rjae, with three clones of one repository under two Claude Code accounts, saw one folder stay on 0.3.0 after a
`/brigade:update`, and had never seen the collaborator-side install offer `docs/setup.md` described. The update
skill ran `claude plugin update --scope project` first, on the assumption that the project-scope record was the
one a session in that folder loads. Nothing in Claude Code's documentation says which record wins when both a
user-scope and a project-scope install exist, nor whether the per-version directories under `plugins/cache/`
matter on their own. Both had to be measured before recommending a scope.

## Rig

- A spare Claude Code account (`~/.claude-alyne`) with no Brigade install: the negative control.
- Three throwaway git repositories: `ctl` commits `.claude/settings.json` with `enabledPlugins` **and**
  `extraKnownMarketplaces` (what `docs/setup.md` prescribed); `mkt` commits `extraKnownMarketplaces` only;
  `neutral` commits neither.
- Sessions started with `claude -p` through the driver, which unsets every `CLAUDE*`-prefixed variable and
  `AI_AGENT` and rebuilds `PATH` from the `claude` binary's directory plus the system directories.
- Probe: the session runs `brigade version` through the Bash tool (`--allowedTools "Bash(brigade version)"`).
  The plugin's bootstrap execs `brigade-$version-$os-$arch`, verified against its own `checksums.txt`, so the
  answer is the pinned version of whichever plugin directory that session put on the Bash tool's PATH.
- Records were set between arms by editing `plugins/installed_plugins.json` (`version` and `installPath`)
  under the account; both versions were present in `plugins/cache/brigade/brigade/`.

## The first run was invalid

The first driver unset the `CLAUDE*` variables but inherited the calling session's `PATH`, which carried that
session's own `…/plugins/cache/brigade/brigade/0.6.1/bin`. Every arm answered `0.6.1` whatever the account under
test had installed, and the run was read as "the newest cached version loads". A negative control exposed it:
with no install in the account, `brigade version` still answered. The driver now rebuilds `PATH`, and the
negative control — `command not found`, exit 127, with nothing installed — is the first arm of the valid run.

## Results (valid run)

| arm | records (user / project for `ctl`) | `neutral` | `ctl` | `mkt` |
| --- | --- | --- | --- | --- |
| negative control | none installed | `command not found` | — | — |
| positive control | user 0.6.1 / (auto-created 0.6.1) | 0.6.1 | 0.6.1 | 0.6.1 |
| every record aged, 0.6.1 still cached | user 0.4.1 / project 0.4.1 | 0.4.1 | 0.4.1 | 0.4.1 |
| user newer | user 0.6.1 / project 0.4.1 | — | 0.6.1 | 0.6.1 |
| project newer | user 0.4.1 / project 0.6.1 | — | **0.4.1** | 0.4.1 |

Record behaviour, observed from the registry file rather than the probe (these held in the invalid run too):

- Opening `ctl` with a user-scope install present **created a project-scope record for that folder**, seeded from
  the installed version. Opening `mkt` created nothing. Opening `ctl` with **no** install in the account created
  nothing and installed nothing.
- Records never re-seed from each other: a project record aged to 0.4.1 stayed 0.4.1 across later opens with a
  0.6.1 user record present.

## Superseded in part by E9

E9 (2026-09-14, later the same day) found that `claude -p` sessions and interactive sessions resolve scope
differently: in a trusted folder with a project-scope record at 0.6.1 and a user-scope record at 0.6.3, a `-p`
session loaded 0.6.3 while a real interactive session in an equivalent state (`~/.claude`, `appshapes/brigade`,
session `df064357`) loaded the project record. Conclusions 2 and 4 below therefore describe headless sessions
only; for interactive sessions Claude Code's documented settings precedence — project over user — is what was
observed. The record-creation findings (1 and 3) stand. E9 carries the corrected procedures.

## What it settled

1. **The install records decide the loaded version; the cache directories do not.** Aging every record to 0.4.1
   with 0.6.1 still on disk loaded 0.4.1 everywhere.
2. **A user-scope record, when one exists, decides the version in every folder — even when a project-scope record
   for that folder is newer.** `claude plugin update --scope project` therefore changes nothing a session loads
   while a user-scope install exists. With no user-scope record, each folder loads its own project-scope record.
3. **A committed `enabledPlugins` installs nothing and asks nothing.** Its effect is to create a per-folder
   project-scope record on first open when the account already has the plugin — inert under a user-scope install,
   and the folder's own version otherwise.
4. Hence, for one account with several repositories and clones: install at **user** scope; one
   `/brigade:update` moves every folder at its next session start; `/brigade:update` must update user scope
   first (it did the opposite before 0.6.2).

## What it does not prove

- The interactive first-open path, only in part. `claude -p` cannot show a prompt, so whether Claude Code offers
  to install a plugin named by a committed `enabledPlugins` remains unmeasured — Rjae has never seen it. Whether it
  adds a committed marketplace on trust was measured the same day by Rjae on a machine that had never seen
  Brigade: it does, and `/plugin install brigade@brigade` alone installed the plugin.
- Whether `plugins/.last_inuse_sweep` can remove a cached version no record references. Every `install`/`update`
  writes a record for the version it fetches, so in ordinary use the loaded version is always referenced.
- Anything about a running session: it keeps the version it started with until `/reload-plugins` or a restart,
  which is Claude Code's documented behaviour and was not re-measured here.
