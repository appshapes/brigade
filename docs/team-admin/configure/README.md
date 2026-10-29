# Configure

The project configures its sessions through `.brigade.json`: whoever can commit the file decides, every session
of every checkout reads it at start, and a member can only switch things off on their own side.

## If you want to

| If you want to | Go to |
| --- | --- |
| keep folders in step between teammates' checkouts | [Set the folders to sync](#set-the-folders-to-sync) |
| have the team's sessions keep state for a manager digest | [Set up the manager digest](#set-up-the-manager-digest) |
| slow the timers for a backend plan with a request cap, or change when an idle VS Code session closes | [Set the timers](#set-the-timers) |
| tell people without a session how to reach the team | [Name the gateways](#name-the-gateways) |
| know what the file carries, and what every member's plugin must be first | [Know the team file](#know-the-team-file) |

## Set the folders to sync

1. Add `"sync": { "folders": [".context/plans", "docs/shared"] }` to `.brigade.json` and commit it.
2. Every member's plugin must be 0.11.0 or later first; each machine needs `syncthing` on its PATH.

Details: [Set the folders to sync](sync-folders.md).

## Set up the manager digest

1. Add `"state": ".brigade/state"` to the `sync` member, naming one of its listed folders, and ignore the folder
   in `.gitignore`.
2. Every session then keeps its state entry there; `brigade note` and `brigade decision` add to it.

Details: [Set up the manager digest](manager-digest.md).

## Set the timers

1. Add `"polling": { "heartbeat_seconds": 100, "roster_seconds": 300 }` for a project near a free plan's cap.
2. `idle_close_hours` (default 6, 0 switches it off) is how long a session the VS Code extension started may idle.

Details: [Set the timers](timers.md).

## Name the gateways

1. The Project Owner's `make gateway-install` and `make slack-gateway-install` write the `gateway` member.
2. Commit the changed `.brigade.json`; it carries the address and the bot, both public.

Details: [Name the gateways](gateways.md).

## Know the team file

1. Public values only: URL, key, team reference and name, plus the optional `sync`, `polling`, `gateway` and
   `adapter` members.
2. A plugin of 0.10.0 or earlier refuses a file with a member it does not know, or one over 4096 bytes.

Details: [Know the team file](team-file.md).
