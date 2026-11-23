# Join in Claude Code

## The command

Clone the project, save the secret file your administrator sent you outside the repository (say
`~/brigade-<team>.secret`), open a Claude Code session in the checkout, and at the prompt:

```
/brigade:join ~/brigade-<team>.secret
```

That runs `brigade team join --secret-file ~/brigade-<team>.secret` and relays what it printed. `team join` reads
the project's `.brigade.json`, prints one line naming the team and the backend host it is joining (invoking it is
the consent), reads the secret from the file and joins. Its output names the team, never the secret. The path is
absolute or starts with `~/`, which Brigade reads as your home directory; `~someone/` and relative paths are
refused. Brigade checks that the file is outside the repository and nothing else about it. Delete the file once
every machine that needs it has joined, and never paste its contents into a chat.

There is no `--profile`, no `--url` and no `--key`: the project file supplies all of that.

## What to know

- The URL in `.brigade.json` must start with `https://`; the adapter refuses anything else except a loopback host.
- With the Bash sandbox on, add the host of the URL in `.brigade.json` to `sandbox.network.allowedDomains`, or the
  first send is refused: `<ref>.supabase.co` for a Supabase project, the server's name for a Brigade server.
- Joined from inside a session, that session attaches at your next prompt. A session already attached to another
  team stays there until you start a new session or exit and resume that one, and the join says so;
  `/reload-plugins` does not move it. A session attached to this team on the backend the team left moves at its
  next prompt when you joined in the checkout it was started in ([When your team moves](follow-a-move.md)).
  Joined from a terminal, start a session in the checkout; one you already have there that is on no team
  attaches at a prompt within a minute.
- A join from a terminal (`brigade team join --secret-file <path>`) sends the address of the Claude account of
  the profile that terminal names, the default profile's when it names none. Where your sessions run under
  another profile, pass `--label '<label>'`; a label the team already holds is corrected the same way
  ([Choose your label](your-label.md#join-from-a-terminal)).
- A project that syncs folders (its `.brigade.json` carries a `sync` member) needs Syncthing on your PATH:
  `brew install syncthing`, `sudo apt install syncthing`. Without it the session connects and a prompt soon after
  says file sync is off.
- The file's `adapter` field names the backend's kind. `supabase` (or no name) and `rest`, a Brigade server, are
  bundled with Brigade; any other name resolves to a command through your own `adapters.json`
  ([docs/adapter-authors.md](../../adapter-authors.md)).
- On Windows the command is the same, in Claude Desktop's Code tab or Claude Code with Git for Windows:
  `~/brigade-<team>.secret` is `C:\Users\<you>\brigade-<team>.secret`, outside the repository
  ([Install on Windows](../install/windows.md)).

## You know it worked

The session starts with a line like:

```
Brigade: this session is "payments-api" (09365acd…) in team "ops"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`.
```

`/brigade:sessions` prints the roster. Joining shares your label with the team: [Choose your label](your-label.md).

Next: [See the team](../use/see-the-team.md).
