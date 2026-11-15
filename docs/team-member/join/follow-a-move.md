# When your team moves

A team can move from one backend to another: from a Supabase project to a Brigade server your organisation runs,
or from one Brigade server to another. Its administrator moves it with one command, and you run nothing: no join
and no command. Once you have pulled the changed `.brigade.json`, the next session you start in the project
follows the team: on plugin 0.55.0 or later when the team leaves a Supabase project, and on 0.57.0 or later when
it leaves a server. A session that is already running follows at its next prompts. Either tells you so in one
line:

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. To leave the team run `brigade team leave`; to stay put until you join a moved team yourself, set the option follow_team_move to off.
```

## What made it follow

Not the project's `.brigade.json` alone: anyone who can commit can change that file. The session asked the backend
you had joined whether the team's administrator recorded a move there, and followed only because the record named
exactly what the file names: the same server, the same certificate, the same team. Then your machine joined the
server under the label you had. Nothing you hold for the old backend was sent to the server.

## A session that was already running

It follows at its next prompts, from plugin 0.56.0; on 0.55.0 and 0.55.1 it stays where it is until it
ends. Nothing is asked of you.

1. The first prompt after the team file changed says once that the file names another backend, and starts the
   follow beside the session: the same question to the backend you joined, and the same join. Your prompt is not
   held up.
2. A later prompt, once the checkout has followed, says the line above and moves the session: it is registered
   on the server under the name it had, and the line after it gives the session's new id. A reply sent to the
   id it had does not reach it.

- **It moves with the checkout it was started in.** A session follows the `.brigade.json` of the checkout it
  was started in and no other, wherever it works: after a `cd`, in a worktree or in another repository it
  still follows that checkout, at its next prompts. No other checkout's file moves it, and a `/clear` there
  does not either: a session that has moved does not go back because another checkout has not pulled the
  change yet, and one that has not moved does not go ahead of its own. Told while it works elsewhere, the line
  above begins `Brigade: the checkout this session was started in followed team`: the directory it is in
  followed nothing.
- **If the prompt that moved it was cut short**, the next prompt tells you. A prompt has a few seconds, and
  one that runs out of them after the session moved cannot say so: the session then has a new id for one
  prompt before you are told. A message sent to that id meanwhile may wait on the server until that prompt,
  and in Muse Code and Codex CLI a `brigade` command of the session may be refused until then.
- **A quiet session stays quiet.** `/brigade:quiet` holds on the server as it did before.
- **Held messages keep it back.** A message that is held for your review is on the backend the team left and
  does not move. The session says so once and stays until you release it (`brigade inbox`, then `brigade inbox
  release`, in your own terminal), and moves at its next prompt after that. `/clear` does not move it either:
  the conversation that begins is told the same, and the session is the one it was.
- **If the server cannot be reached**, the session stays where it is, says so once, and tries again at a later
  prompt, a minute later at the earliest; after a `/clear` too, which never says that such a session is not
  connected. A prompt that tries waits for the server, for no longer than the few seconds a prompt may take.
- **With no record behind the file**, the session stays where it is. The backend you joined is asked again at
  most once a minute while you prompt, and the address the file names is sent nothing.
- **A Grok Build session** follows when it starts, not while it runs: Grok Build shows nothing a prompt hook
  prints, so the session could not tell you.

## If you would rather decide yourself

Set the option `follow_team_move` to `off` ([Know the options](../configure/options.md)): ask your session `Set
follow_team_move to off.` From then on a session never follows. When a team of yours moves, the session says that
`.brigade.json` does not match the team you joined, and you join the server yourself with the secret file:
`/brigade:join ~/brigade-<team>.secret`. A session that is running says once that it stays where it is, and
moves at its next prompt after you joined in the checkout it was started in.

## If the session says the file does not match

Then nothing vouched for the change: the administrator has not recorded a move, or recorded another server than
the file names, or the move has ended. Ask your administrator before you join anything. A team file that changed
with no record behind it is exactly what this check is for.

A plugin older than 0.55.0 does not follow either, though the move is recorded, and neither does one older than
0.57.0 when the team left a Brigade server: update it ([Update](../update/README.md)) and start a new session, or
join with the secret file.

**If the team moved twice.** A move is followed from the backend your machine is on, one move at a time. If the
team went on to a third backend before a session of yours followed the first move, the backend you joined
records a move to the second, the file names the third, and the session says that the file does not match. Join
the team where it is now with the secret file, `/brigade:join ~/brigade-<team>.secret`. Your administrator can
tell you that this is what happened.

**If the session says that the two backends each record that the team moved to the other.** The backend you
joined says the team went where the file names, and that one says the team went back. One of the two records is
out of date, nothing on your machine says which, and the session follows neither: a file that flipped between
the two would otherwise send it back and forth. It says so once and stays where it is. Tell your administrator,
who clears the record that is out of date.

**If the team comes back to a server you were on before.** Your machine still holds the membership it had
there. Where the administrator ended that membership after the team had left, the server refuses it, and the
session joins again with the move's ticket, with no command of yours, under the credential your machine holds:
you come back as the member you were. Where the membership is still good, the session uses it and joins
nothing. Where the server refuses that join too, because the administrator banned you there or the move's
ticket no longer admits, a session says once that the backend refuses the membership your machine holds, with
the command that joins. No session presents the ticket again for fifteen minutes; the first to start after that
asks once more.

**If the session says that the backend the team moved to did not answer.** The move is recorded and the file is
the right one; the new backend could not be reached, or did not answer in time, when the session asked it.
Nothing was changed, and nothing is yours to do: the next session follows when it starts, and this one at one of
its next prompts. Until then a prompt says the same.

**If the session says that the backend refuses the membership this machine holds there.** The move is recorded
and the file is the right one, and the backend the team moved to answered: it does not know the credential your
machine holds for it, or it no longer counts you a member and did not take the move's ticket. Waiting does not
mend that. Ask your administrator for the file of the team's current join secret and run
`brigade team join --secret-file <path>` in the project (`/brigade:join <path>` in a session); the session
connects at its next prompt.

A session that said so because the move was not recorded yet needs no new start, except in Grok Build: while
you prompt it asks again, at most once a minute, says once that it is asking, and follows within a prompt or
two of the record being there.

## To leave

`brigade team leave` in the checkout leaves the team on the server. It holds: a session in another checkout of
the team does not join you again. To come back, join yourself.

What this opens, and to whom, is in [Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend).
