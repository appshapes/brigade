# Leave

Leaving a team and removing Brigade are separate, and the order matters: leave first, revoke the credential
second, remove the package last.

## If you want to

| If you want to | Go to |
| --- | --- |
| leave a team, keeping Brigade installed | [Leave the team](#leave-the-team) |
| remove Brigade from Claude Code | [Uninstall from Claude Code](#uninstall-from-claude-code) |
| remove Brigade from Muse Code | [Uninstall from Muse Code](#uninstall-from-muse-code) |
| remove Brigade from Codex CLI | [Uninstall from Codex CLI](#uninstall-from-codex-cli) |

## Leave the team

1. `brigade team leave` (with `!` in a session, or in a terminal): closes your sessions, marks the membership revoked; a rejoin reverses it.
2. `brigade team reset`: revokes the credential at the backend and deletes it locally. **Run step 1 first.**
3. If you created the team, `brigade team transfer --principal <ref>` **before** step 1.

Details: [Leave the team](leave-the-team.md).

## Uninstall from Claude Code

1. Steps 1 and 2 of Leave the team, for each team.
2. From `/plugin` inside a session, uninstall `brigade`; optionally remove the `brigade` marketplace.
3. `rm -rf ~/.local/state/brigade ~/.local/share/brigade`, then `rm -rf ~/.config/brigade`.

Details: [Uninstall from Claude Code](claude-code.md).

## Uninstall from Muse Code

1. Steps 1 and 2 of Leave the team, for each team.
2. `muse plugins remove brigade`, and remove the `~/.local/bin/brigade` symlink.
3. `rm -rf ~/.local/state/brigade ~/.local/share/brigade`, then `rm -rf ~/.config/brigade`.

Details: [Uninstall from Muse Code](muse-code.md).

## Uninstall from Codex CLI

1. Steps 1 and 2 of Leave the team, for each team.
2. `codex plugin remove brigade@brigade`; optionally `codex plugin marketplace remove brigade`; remove the `~/.local/bin/brigade` symlink.
3. `rm -rf ~/.local/state/brigade ~/.local/share/brigade`, then `rm -rf ~/.config/brigade`.

Details: [Uninstall from Codex CLI](codex.md).
