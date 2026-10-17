# Add a harness

The code and the package are [Add a harness](../change/harness-packages.md); this page is the documentation the
gate requires before that package can land.

1. **The registry.** Add an entry to `docs/harnesses.json`:

   ```json
   { "id": "<id>", "name": "<Name>", "vendor": "<Vendor>", "host": "<the Host string in internal/harness/config>", "package": "<dir>", "skills": "<dir>/skills" }
   ```

   `id` is the file name of every page below; `host` is what `brigade whoami` prints after `host:`.
2. **The member's pages**, each the same shape as its Claude Code and Muse Code siblings and each named by a
   `Details:` line in its topic root, with a row in the topic's table:
   - `docs/team-member/install/<id>.md`: how the package is installed, how `brigade` reaches the shell, what differs.
   - `docs/team-member/join/<id>.md`: how a member joins from that harness, and when the session attaches.
   - `docs/team-member/update/<id>.md`: how the package is updated, when a running session picks it up.
   - `docs/team-member/configure/<id>.md`: where the options come from in that harness.
   - `docs/team-member/leave/<id>.md`: how the package is removed, with the common steps linked.
3. **The agent's page**, `docs/agent/in-a-team/<id>.md`, named by a `Details:` line in that topic root: a table of
   every skill under `<skills>/*/SKILL.md` by path (the gate checks each path appears), how the session is woken,
   and what differs from Claude Code.
4. **The package page**, `<package>/README.md`: what the package ships and a link to `docs/team-member/install/<id>.md`.
5. **The security section.** What the harness's own layers add and what has not been measured, as
   `docs/security.md` section 15 does for Muse Code, linked from the member's security topic root.
6. **The experiment.** The live test that answered "can anything outside the session wake it?", under
   `docs/experiments/`, in the index, linked from the install page.
7. `make docs-check`. It names whatever is missing.
