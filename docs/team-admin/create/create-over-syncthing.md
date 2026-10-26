# Create a team over Syncthing

A team over Syncthing has no project: its messages live in one folder per team, under each member's Brigade
state directory, that [Syncthing](https://syncthing.net) keeps in step between the members' machines. The same
engine syncs a project's folders ([Sync folders](../../team-member/use/sync-folders.md)); this uses it for the
team itself. No account, no URL, no key, no egress cap, and nothing a third party can read.

## What you need

- `syncthing` on PATH, on your machine and on every member's: `brew install syncthing` (macOS),
  `sudo apt install syncthing` (Debian, Ubuntu). Measured with 2.1.5.
- A plugin at 0.39.0 or later, on every member's side.

## The command

Inside the project checkout, from a session (the `!` prefix) or from your own terminal:

```sh
!brigade team create --adapter syncthing --name <team> --secret-file ~/brigade-<team>.secret
```

then `git add .brigade.json && git commit && git push`.

- `team create` starts the engine, creates the team folder on this machine, writes `.brigade.json` and stores your
  credential locally. The file carries the team's reference and name and a `devices` member: your machine's
  Syncthing device id, the team's first **seed**. No `url`, no `publishable_key`.
- `--secret-file` is **required** and must be an absolute path, or one that starts with `~/`, outside the
  repository: the join secret is written there, mode 0600, and never reaches your scrollback.
- `--url` and `--key` are refused with this adapter.

## How members get in

A joining machine knows nobody but the seeds in `.brigade.json`. It connects to them and announces its device
under a name that proves it holds the join secret; the first seed whose session is running accepts it onto the
team folder, and the folder reaches it within seconds. So **a member joins while a session of a seed is
running**, and the output of their join says that the folder is pending until then. Any member can be a seed:
add the `peer` line of their `brigade sync status` to `devices` by hand and commit.

Once a member's machine holds the folder, it syncs with every member's, not only the seeds: every member's
engine adds every other member's device from the membership files. A message to a teammate whose machine is off
waits on every machine that holds the folder and reaches them when they come back.

## What is different from a team in a project

- **Every member's machine holds every message of the team**, on disk, under the Brigade state directory.
  A project's row policy lets a member read only what they sent and what was sent to them; this adapter has no
  such line. Choose it for a team that would share one channel anyway.
- **Nobody vouches for a sender.** The sender's own machine stamps its name on a message. A member who edits the
  files by hand can claim any sender, and the caps on sending are self-enforced by the sender's machine. The join
  secret is the boundary, as it is everywhere in Brigade.
- **Revoking is eventual.** A revocation is a marker the creator writes; every member's machine filters on it
  the moment it arrives, and drops the revoked device after a 15-second grace, so the marker reaches it first.
  Until the marker arrives, a revoked machine still sees the folder.
- **A session's state is read on each machine's own clock.** A machine whose clock is far off sees the roster
  wrong. Keep the clock right.
- **Delivery takes seconds, not milliseconds:** the sender's engine announces the change within a second, the
  file is small, and the recipient's watch polls its copy twice a second. Measured on one machine, two engines:
  a message in 2 s, a join through acceptance in 8 s.
- **The hosted gateways need a project.** Mail and Slack through the hosted gateways, and the webhook gateway,
  stay with a Supabase project.

The adapter's store, its join handshake and its engine round are in
[`internal/adapters/syncthing/README.md`](../../../internal/adapters/syncthing/README.md); what the adapter
changes in the security picture is [docs/security.md, section 18](../../security.md#18-the-syncthing-adapter).

## Check it worked

`brigade team status` in the checkout names the team, `engine: running`, `team_folder: present` and your
`device_id`. After a member joins, `brigade sessions` lists their session within seconds.

## Then

[Hand out the secret](hand-out-the-secret.md) and [Back up the credential](back-up-the-credential.md). There is
no keep-alive to arm and no project to pay for.
