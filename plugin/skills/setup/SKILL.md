---
name: setup
description: >-
  How a HUMAN creates or joins a Brigade team from their own terminal, and how to leave and uninstall: the
  Supabase project prerequisites, `team create` (which writes the project's committed `.brigade.json`), the
  one-command `team join`, and the removal order. Use when someone asks how to set Brigade up, join a team, add a
  teammate, leave, or remove the plugin. It never asks anyone to paste a join secret into the chat.
user-invocable: true
---

# Setting Brigade up

A Brigade team belongs to a **project**: a committed file, `.brigade.json`, at the repository's top level names the
team every session in that checkout talks to. It carries only public values, so it is safe in version control; the
join secret is never in it. The administrator writes the file once; each member runs one command in their checkout.

Every command below is run by a **person, in their own terminal** — never by a model and never through the Bash
tool. The join secret is a bearer capability: **never paste it into a Claude Code chat**, a commit, an issue or a
log. The binary the plugin uses is at `${CLAUDE_PLUGIN_ROOT}/bin/brigade`; a symlink such as
`ln -sf ${CLAUDE_PLUGIN_ROOT}/bin/brigade ~/.local/bin/brigade` keeps a terminal on the same pinned version; the path
carries the plugin version, so re-run it after a plugin upgrade.

## 1. Administrator: create a team

The bundled adapter keeps a team in a Supabase project, so create a **single-purpose** project for it first:
enable anonymous sign-ins under Authentication > Sign In / Providers; leave CAPTCHA off (a command-line client
cannot solve a browser challenge); do not enable the Pro session time-box or inactivity limits (they silently kill
idle principals); turn "Allow public access" off in the Realtime settings; and apply the migrations from this
repository's `supabase/` directory to the project. Members ever receive only two values, and both are non-secret:
the **project URL** and the **publishable key** — and the committed file already carries both.

Run this **in the project checkout**:

```sh
${CLAUDE_PLUGIN_ROOT}/bin/brigade team create --url https://<ref>.supabase.co --key sb_publishable_… \
  --name <team> --secret-file ~/brigade-<team>.secret
git add .brigade.json && git commit && git push
```

`team create` writes `.brigade.json` at the repository top level and stores your credential locally so your
sessions attach at once. `--secret-file` is required and must be an absolute path outside the repository; it holds
the join secret at mode 0600 instead of your terminal scrollback.

Then send each member the join secret **over a password-grade channel** — a password manager share, not chat and
not email. The secret is a bearer capability: anyone holding it can join and pick any label.

Your credential directory (`~/.config/brigade/teams/<key>`, or under the `config_dir` plugin option) is the
team's only administrative credential. Keep a 0700 backup of it somewhere you control; without it nobody can
rotate the secret or administer the team. Rotating the join secret, revoking a member and transferring the team
are terminal-only administrative commands that refuse inside a session; the procedure is in `docs/setup.md`,
"Team administration", in the Brigade repository.

## 2. Member: join

Clone the project, then **in the checkout**:

```sh
${CLAUDE_PLUGIN_ROOT}/bin/brigade team join
```

`team join` reads the project's `.brigade.json`, shows the team and backend host it names and asks you to confirm,
then reads the join secret without echo — so the secret never reaches your scrollback, your shell history or a
chat. There is no `--profile`, no `--url` and no `--key`: the project file supplies all of that.

- The URL in the file must be `https://`; the adapter refuses anything else except a loopback host.
- If your Claude Code sessions run with the Bash sandbox on, add the project host (`<ref>.supabase.co`) to
  `sandbox.network.allowedDomains`, or the first send is refused.
- Then start a Claude Code session in the checkout, or run `/reload-plugins` in one you already have. The
  session-start line names your team, this session's name and id, and the inbound policy.
- A second checkout of the same project needs `team join` once too, but no secret. Several projects means several
  `.brigade.json` files: join each once, then `cd` between them — nothing is shared or switched.
- A backend other than the bundled Supabase adapter is named in the project file's `adapter` field, and the name
  resolves to a command through your own `adapters.json` (an absolute path or a JSON array);
  `docs/adapter-authors.md` in the Brigade repository explains the forms. `brigade team status`, run in the
  checkout, names the team and its adapter.

## 3. Leaving and uninstalling

The order matters. Every step is optional except step 3 when the goal is to remove the plugin.

1. `${CLAUDE_PLUGIN_ROOT}/bin/brigade team leave` closes your open sessions in that team and revokes the
   membership; teammates stop seeing your sessions immediately. Skip it and the membership stays active
   indefinitely while your sessions merely go offline after the lease expires.
2. `${CLAUDE_PLUGIN_ROOT}/bin/brigade team reset` revokes the credential family on the backend and deletes the
   local credential. **Run step 1 first:** after a reset the membership and its sessions can no longer be closed
   from this machine, and a later rejoin mints a new principal that teammates see as a new person.
3. `claude plugin uninstall brigade` removes the plugin itself.
4. `rm -rf ~/.local/state/brigade ~/.local/share/brigade` (or the `XDG_STATE_HOME`/`XDG_DATA_HOME` equivalents)
   removes the session maps, pidfiles, logs and the cached binaries. Keep
   `~/.local/state/brigade/sessions/by-native` if a later reinstall should resume your old Brigade sessions.
5. `rm -rf ~/.config/brigade` (or the directory named by the `config_dir` option) removes every team's credential.
   Do this only after step 2 for each team: a deleted credential whose family was never revoked stays usable by
   any copy of it.

If you **created** the team, step 2 ends secret rotation, revocation and transfer for it, permanently (step 1 alone
does not: a rejoin with the current secret restores administration). Transfer the team to another active member
*before* step 1, and keep that 0700 backup of the credential directory either way.
