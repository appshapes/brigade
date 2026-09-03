# E3 — Interactive checks (P3-8)

Date prepared: 2026-09-03 · Ticket 15 · Status: **PENDING — to be run by Rjae at a keyboard** · Claude Code 2.1.259
at preparation time (record the version you run on).

This is the checklist of plan row P3-8: the things only an interactive terminal can show. Every headless half of the
plugin is measured in `E3-wiring.md` and `E3-smoke.md`; this file records the keyboard-dependent half. Fill in the
"Observed" column with what you saw and paste a transcript excerpt per row (the transcript is
`$CLAUDE_CONFIG_DIR/projects/<cwd-key>/<session>.jsonl`; `jq -c 'select(.type=="attachment")'` shows the hook
records, `jq -c 'select(.type=="queue-operation")'` the injected messages). Treat a green here as evidence only
where you name what you saw, not what you expected.

## 0. Setup (once, in your own terminal — never from inside a Claude Code session)

`team create` and `team join` refuse inside a session on purpose. Three terminals in all: ONE plain terminal in the
repository for everything in this section (both command blocks below run there, one after the other; keep it open
afterwards for the terminal-side steps of checks 10–13), then two more for the Claude Code sessions A and B. Run
this in the setup terminal:

```sh
make build
bin/brigade profile init --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
bin/brigade team create --name ops --label dev --secret-file ~/brigade-ops.secret
bin/brigade profile status                              # expect: default adapter [...] (from sidecar)
```

Then, in the same terminal, a second profile for the peer session (it joins the same fs team; the secret goes from
the 0600 file into the stdin document, never onto argv). The join is ONE command — a brace group piped into
`brigade team join` — written on one line here so it is one paste and one Enter:

```sh
bin/brigade profile init --profile bob --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
{ printf '{"human_label":"bob","join_secret":"'; tr -d '\n' < ~/brigade-ops.secret; printf '"}'; } | bin/brigade team join --profile bob
bin/brigade team members --profile bob                  # expect: two principals
```

Where things live (notes, not commands): `make plugin-dev` writes the dev-binary pointer
`${XDG_CONFIG_HOME:-~/.config}/brigade/dev-binary` and starts Claude Code with `--plugin-dir ./plugin`; `make
plugin-dev-off` removes the pointer afterwards. The fs store is `${XDG_STATE_HOME:-~/.local/state}/brigade/fs-adapter`;
the maps and pidfiles are under `${XDG_STATE_HOME:-~/.local/state}/brigade/`. Nothing here touches the Supabase
stack.

Now open the two session terminals (the setup terminal stays open):

- **A (alice)**: `make plugin-dev` (the sidecar written by `profile init --adapter` above selects the fs adapter;
  `make plugin-dev adapter=fs` is the same thing spelled out).
- **B (bob)**: `make plugin-dev profile=bob`.

Both should print, as the first context line of the session, `Brigade: this session is "<name>" (<id>) in team
"ops"; inbound: accept; …`. Record both lines.

## 1. The checklist

| # | Check (plan row P3-8 / E0-8) | How | Expect | Observed |
| --- | --- | --- | --- | --- |
| 1 | **The skill grant in Manual mode** (D20, E0-8 (b)) | In A, with NO `permissions.allow` rule for `brigade`: ask "Use the brigade team-messaging skill to list the team's sessions and tell me who is online." | ONE Skill dialog ("Use skill brigade:team-messaging?") whose dismissal is scoped to the project directory; then `brigade sessions` runs with **no** Bash prompt in that turn. | |
| 2 | **The grant does not outlive the turn** | In A, next prompt, without invoking the skill: "Run `brigade sessions` again." | A Bash permission prompt appears (the grant cleared when you sent the next message). | |
| 3 | **`permissions.allow` removes the prompt for the session** | Add `"permissions": {"allow": ["Bash(brigade:*)"]}` to your USER settings (never the project's), restart A, repeat check 2. | No prompt. | |
| 4 | **The ask rule in bypass mode** (E0-8 (b)) | Start A with `--permission-mode bypassPermissions` and `"permissions": {"ask": ["Bash(brigade send*)"]}` in user settings (`make plugin-dev` runs `claude`; pass extra flags through a manual `claude --plugin-dir ./plugin --permission-mode bypassPermissions …` after `make plugin-dev-pointer`). Ask A to send B a message. | The dialog appears for the heredoc `brigade send` (it is not silently denied); record how the multi-line heredoc renders and whether the dialog offers only Yes/No (an explicit ask rule cannot be one-click disabled — sitting correction 3). `brigade sessions` runs unprompted. | |
| 5 | **The deny rule is the off switch** | Same, with `"permissions": {"deny": ["Bash(brigade send*)"]}`. Ask A to send. | The send is blocked in bypass mode; the model reports the denial and (per the skill) does NOT propose another invocation form. Record its words. | |
| 6 | **Send and mid-turn receive between two people's sessions** | In A: "Send bob's session a message saying hello and then sleep 30 seconds with the Bash tool." While A sleeps, in B: "Reply to alice's message with brigade send --reply-to." | A receives B's reply mid-turn as a message from `@<bob's session name>` with the `<brigade-message …>` frame; the fs store shows the message under `acked/<alice>/`; A does not call the native `SendMessage`. | |
| 7 | **`/rename` propagates** | In A: `/rename alice-renamed`, wait ~30 s (one heartbeat), then in B: "Run `brigade sessions`." | B's listing shows A under the new name (the watcher re-reads the registry each heartbeat). | |
| 8 | **`/clear` keeps the Brigade session** | In A: note the session id from `brigade whoami`; `/clear`; `brigade whoami` again. | The same Brigade session id; the SessionStart hook re-fired (a new context line) and did NOT rotate the identity; the watcher pidfile pid is unchanged (`cat ~/.local/state/brigade/watchers/<claude pid>.json`). | |
| 9 | **`/compact` is a no-op for Brigade** | In A: `/compact`; then `brigade whoami`. | Same id, no new registration, no new context line beyond Claude's own. | |
| 10 | **The shadowing warning** (E0-8 (e)) | Put another `brigade` earlier on PATH in a terminal (`mkdir -p /tmp/shadow && printf '#!/bin/sh\necho DECOY\n' > /tmp/shadow/brigade && chmod +x /tmp/shadow/brigade`), then `PATH=/tmp/shadow:$PATH make plugin-dev`. | A second context line: `Brigade: another \`brigade\` at /tmp/shadow/brigade shadows the plugin's; …`; asking the model to run `brigade sessions` prints `DECOY`. Remove the decoy afterwards. | |
| 11 | **`~/.local/bin` symlink is NOT a shadow** | `ln -s "$PWD/plugin/bin/brigade" ~/.local/bin/brigade` (the setup skill's suggestion), start A. | No shadowing line (a symlink resolving to the plugin's bootstrap is exempt). Remove the symlink if you do not want it. | |
| 12 | **SessionEnd closes the session** | Quit A with `/exit`; in a terminal: `bin/brigade-adapter-fs --profile bob session list --include-offline` (needs `BRIGADE_FS_ROOT=$HOME/.local/state/brigade/fs-adapter` in that terminal) or in B: "Run `brigade sessions --all`." | A's session shows `offline` at once (closed by SessionEnd), not 90 s later; A's watcher pidfile is gone within ~2 s. | |
| 13 | **The watcher survives a SIGKILLed Claude and closes the session itself** (E0-5) | Start A, note its pid (`echo $CLAUDE_PID` via the Bash tool) and the watcher pid (the pidfile); `kill -9 <claude pid>` from a terminal. | Within ~2 s of the process being reaped the watcher exits, the pidfile disappears and A's session is `offline` (the zombie reads as dead). | |
| 14 | **Sandbox** (6.12; only if `sandbox.enabled` is on for you) | With the Bash sandbox on and no `allowedDomains` entry, ask A to run `brigade sessions`. | The fs adapter needs no network, so it works under the sandbox; `brigade send` succeeds with a read-only home (the map is only read). A hosted Supabase profile would need `<ref>.supabase.co` in `sandbox.network.allowedDomains` — record as not applicable if no hosted project exists (D32). | |
| 15 | **A third first-run interruption** (sitting correction 5) | Note any onboarding prompt (`Claude in Chrome extension detected`, the trust dialog, the fullscreen-renderer write) that appeared before the session prompt in any of the runs above. | Recorded, for P4-3/P4-5's `expect` drivers. | |

## 2. Teardown

`make plugin-dev-off`; optionally `rm -rf "$HOME/.local/state/brigade" "$HOME/.config/brigade"` (absolute paths, only if
you created them for this checklist and nothing else uses them) and `rm -f ~/brigade-ops.secret`.

## 3. Limits (fill in)

- What you did not run, and why.
- Anything the model did that the skill forbids (an evasive form, a proposed alternative after a denial, a native
  `SendMessage`).
