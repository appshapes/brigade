# E9 — Installing and updating the plugin: one account, many repositories and clones

Card 23. Run 2026-09-14 on macOS, Claude Code 2.1.270. Driver: `scripts/experiments/E8-plugin-scope/run-session.sh`
(a `claude -p` session as a fresh process with a clean `PATH`). Spare account `~/.claude-alyne`, the three real
clones of `GoThinkTech/thinktech` (`thinktech-php`, `-fix`, `-work`), and two scratch clones of that repository:
`tt-enabled` at the commit that still committed `enabledPlugins` for Brigade, `tt-mktonly` at the commit that
dropped it. All folders trusted in the account under test (`hasTrustDialogAccepted` in its `.claude.json`).

## Why

Rjae's requirement for a tool aimed at non-technical users: installing and updating must be simple, and behave
the same way every time, across two Claude Code accounts and three clones of one repository. A day of
configuration by hand had produced a mix of user- and project-scope installs, a folder that stayed on an old
version after an update, and a documented "collaborator install offer" nobody had seen. E8's rig had also been
contaminated once (inherited `PATH`), so this run opens with controls.

## What Anthropic documents (fetched 2026-09-14, code.claude.com/docs/en/…)

- Scopes (`discover-plugins`): "**User scope**: install for yourself across all projects. **Project scope**:
  install for all collaborators on this repository, which adds the plugin to `.claude/settings.json`. **Local
  scope**: install for yourself in this repository only." So a committed `enabledPlugins` entry *is* a
  project-scope install, for everyone who clones.
- Precedence (`settings`): managed > command line > project local > shared project > user. `enabledPlugins` is a
  setting, so a project-scope install takes precedence over a user-scope one in that folder.
- Marketplaces (`discover-plugins`): "Team admins can set up automatic marketplace installation for projects by
  adding marketplace configuration to `.claude/settings.json`. Once a team member trusts the repository folder,
  Claude Code adds these marketplaces without a further prompt."
- Auto-update (`discover-plugins`): "Claude Code can automatically update marketplaces and their installed
  plugins in the background after startup … checks … after your session starts, with a random delay of up to ten
  minutes … If any plugins were updated, you'll see a notification prompting you to run `/reload-plugins`."
  "Third-party and local development marketplaces have auto-update disabled by default." Enabled per marketplace
  by `"autoUpdate": true` on the `extraKnownMarketplaces` entry (documented for managed settings) or the `/plugin`
  › Marketplaces toggle.
- Headless (`headless`): "a `-p` session shows no workspace trust dialog"; it runs project hooks and MCP servers
  "even in a folder you've never trusted". Silent on plugins and scope resolution in `-p`.
- Silent on: whether `enabledPlugins` for an uninstalled plugin installs or prompts; which record loads when both
  scopes exist; whether `autoUpdate` may be committed at project level.

## Controls

- Negative: account with nothing installed, marketplace removed, clean `PATH` → `brigade version` in a session is
  `command not found` (exit 127). Every "loaded version" below is therefore the plugin that session loaded.
- Rig limit found here: with a **trusted** `tt-enabled` (commits `enabledPlugins`), user record 0.6.3 and the
  folder's project record aged to 0.6.1, a `-p` session loaded **0.6.3**; Rjae's interactive session `df064357`
  in the equivalent state (`~/.claude`, `appshapes/brigade`, user 0.6.2 / project 0.6.1) loaded **0.6.1**. `-p`
  and interactive sessions resolve scope differently; `-p` measures record creation and CLI behaviour, not
  interactive precedence. The interactive observation agrees with the documented precedence.

## Fresh install (F), account that has never seen Brigade

| step | action | observed |
| --- | --- | --- |
| F0 | remove marketplace, cache, records, settings entries | nothing brigade-related left |
| F1 | open trusted `thinktech-php` (repo commits the marketplace **only**) | marketplace **added**; no install, no record, `brigade` not found |
| F2 | `claude plugin install brigade@brigade --scope user` (what `/plugin install` runs) | one user record 0.6.3; user `enabledPlugins` set |
| F3 | open `thinktech-php`, `-fix`, `-work` with no further action | 0.6.3, 0.6.3, 0.6.3 |

Also measured (E8, and again in V1 here): opening a trusted folder that commits `enabledPlugins` **creates a
project-scope record for that folder**, seeded from the installed version; a repository that commits the
marketplace only creates nothing. A committed `enabledPlugins` installs nothing and asks nothing on its own.

## Update (U), same account

| step | action | observed |
| --- | --- | --- |
| U1 | user record set to 0.6.1 (an older install) | `thinktech-php` loads 0.6.1 |
| U2 | `claude plugin marketplace update brigade`; `claude plugin update brigade@brigade --scope user` | "updated from 0.6.1 to 0.6.3 for scope user" |
| U3 | open all three clones, no further action | 0.6.3, 0.6.3, 0.6.3 |

## Auto-update flag (AU)

`tt-mktonly`'s committed entry given `"autoUpdate": true`; marketplace removed from the account; folder opened
once → the account's `known_marketplaces.json` entry for `brigade` carries `"autoUpdate": true`. A committed flag
reaches every collaborator on first trusted open; no toggle needed. The background update run itself (interactive
only, delayed) was not observed in this rig.

## What it settled

1. **Repository configuration:** commit `extraKnownMarketplaces.brigade` with `"autoUpdate": true`; commit **no**
   `enabledPlugins` for Brigade. `GoThinkTech/thinktech` dropped the enablement on 2026-09-14 (`54339b248`); this
   repository does the same in this change.
2. **Install:** trust the folder, `/plugin install brigade@brigade` at **user** scope, `/reload-plugins`; then
   `/brigade:join` once per clone (secret file only for a team's first join on the machine). Repeatable: three
   clones, one version, one command.
3. **Update:** automatic — Claude Code updates in the background and prompts `/reload-plugins`; `/brigade:update`
   for right now. Both act on the one user-scope install, so every folder follows. `/brigade:update` also
   updates a project-scope install where a repository committed one, so that folder is never left behind.
4. **What each setting is for:** `<config>/settings.json` `enabledPlugins` — the user-scope enablement;
   `.claude/settings.json` `enabledPlugins` — a project-scope install for every collaborator (avoid);
   `.claude/settings.local.json` — a per-checkout, uncommitted enablement; `plugins/installed_plugins.json` — one
   record per (scope, project) with version and install path; `plugins/known_marketplaces.json` — the
   marketplaces, with `autoUpdate`; `plugins/cache/<mkt>/<plugin>/<version>/` — the plugin copies (old ones
   orphaned and swept after ~14 days); `<config>/.claude.json` `projects[path].hasTrustDialogAccepted` — trust,
   which gates `extraKnownMarketplaces` and `permissions.allow`; `~/.config/brigade/` — Brigade's own credential
   store and the per-checkout pins, shared by every Claude Code account on the machine.

## What it does not prove

- Interactive precedence between a user and a project record is observed once (`df064357`), not repeated here,
  because the rig cannot start interactive sessions; it matches the documented rule and is moot under the
  configuration above, which never creates two records.
- The auto-update run and its `/reload-plugins` prompt were not observed; they are Claude Code's documented
  behaviour and will be seen in ordinary use.
- Whether `autoUpdate` in a project-committed entry is *intended* to be honoured (the docs name managed
  settings); it was honoured on 2.1.270.
