# Join a team over Syncthing

A team over Syncthing has no project: its messages live in a folder under each member's Brigade state directory
that Syncthing keeps in step between the members' machines. Its `.brigade.json` names `adapter: syncthing` and
`devices`, the team's seed machines, instead of a URL and a key. You join it the way you join any team, with one
thing to install first and one thing to know.

## Before you join

`syncthing` must be on your PATH: `brew install syncthing` (macOS), `sudo apt install syncthing` (Debian,
Ubuntu). Brigade runs its own instance of it, under your Brigade state directory, while a session of yours is
active; it never touches a Syncthing you run yourself. Without it the join is refused and says so.

## The join

Save the secret file your administrator sent you outside the repository and join as the page for your harness
says: `/brigade:join ~/brigade-<team>.secret` in Claude Code, the `join` skill in Muse Code,
`$brigade:join ~/brigade-<team>.secret` in Codex CLI, or `brigade team join --secret-file ~/brigade-<team>.secret`
in a terminal. The gate line reads `join team "<name>" (<ref>) over syncthing (1 seed device(s), no backend)`.

## What happens next

Your machine connects to the seeds and announces its device under a name that proves it holds the join secret.
The first seed whose Brigade session is running accepts it, and the team's folder reaches your machine within
seconds: measured at 8 s on one machine. Until a seed's session runs, the join is **pending**: `brigade team
status` says `team_folder: pending`, and `brigade sessions` shows only your own session. Nothing is lost; the
folder arrives when a seed is up. Once it is here, your machine syncs with every member's, not only the seeds'.

The join secret is checked twice: by the seed that accepts your device, and again by your own machine when the
folder arrives. A secret rotated away before then leaves you out, and `brigade team status` and your next prompt
say so; join again with the current secret.

## What to know

- Every member's machine, yours included, holds every message of the team on disk, under
  `~/.local/state/brigade/syncthing/teams/<team_ref>` (0700).
- A message to a teammate whose machine is off waits on every machine that holds the folder and reaches them
  when they come back. One of your machines being off never loses a message for the others.
- Your engine runs while a session of yours is active and stops with the last one, exactly as for
  [folder sync](../use/sync-folders.md); `brigade sync status` shows it and your device id.
- Being revoked ends your session's watch with `unauthorized` when the creator's marker reaches your machine, a
  few seconds after they run it; your engine is dropped from the team after a short grace.

Next: [See the team](../use/see-the-team.md).
