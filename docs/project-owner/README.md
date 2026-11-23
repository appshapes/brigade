# Project Owner

You run the backend that holds your organisation's teams and their messages. It is one of two. A Brigade server
is where a new team starts: one program on a machine you keep, your own or a cloud instance, which carries the
gateways that let people on email or Slack take part. A single-purpose Supabase project is the other, and stays
supported for the teams on one: you create it, deploy Brigade into it with one command, keep it awake on a free
plan and update it with a release's migrations. A team moves from a Supabase project to a server with one
command, which its Team Admin runs. Anyone who can read the backend's storage can read every message, so it
holds nothing else.

## If you want to

| If you want to | Go to |
| --- | --- |
| run a Brigade server on a machine you keep, where a new team starts | [Server](server/README.md) |
| have a team move from your Supabase project to a Brigade server: one command, which its Team Admin runs | [Server](server/README.md#move-a-team-from-supabase) |
| have a team move from one Brigade server to another, the same way | [Server](server/README.md#move-a-team-to-another-server) |
| create a Supabase project, deploy the backend, arm the keep-alive, hand a team its values | [Install](install/README.md) |
| apply what a release changed in the backend, know which migration adds what | [Update](update/README.md) |
| install the mail or Slack gateway, choose who may write, review the project's settings | [Configure](configure/README.md) |
| stop the keep-alive, remove a gateway | [Remove](remove/README.md) |
| delete the project, once the teams that are to go on have moved to a Brigade server | [Delete](delete/README.md) |

## Server

A Brigade server is one program on an always-on machine you keep: install it, start it with a data directory,
and hand the Team Admin its URL and its certificate's fingerprint. A team in a Supabase project then moves to it
with one command of its Team Admin's, `brigade team move --to <url> --secret-file <path>`, and you run nothing;
[Server](server/README.md) has each step.

## Install

A Supabase project created for Brigade alone, then `make backend-install project=<ref>` from a clone of this
repository. [Install](install/README.md) ends with the two public values a Team Admin needs.

## Update

A release may add a migration or change a gateway function, and its changelog section says so. [Update](update/README.md)
is the same idempotent command and a check.

## Configure

The gateways are one more member each, hosted in your project; the project's settings are applied one field at a
time and can be re-checked. [Configure](configure/README.md) has each procedure.

## Remove

A keep-alive is a pair of repository variables; a gateway is a member, two functions and a cron job.
[Remove](remove/README.md) says what to take away and in what order.

## Delete

Deleting the project deletes every team in it and every message, at once. [Delete](delete/README.md) is what to
do first.
