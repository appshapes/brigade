# Project Owner

You run the hosted backend: one single-purpose Supabase project that holds your organisation's teams and their
messages. You create it, deploy Brigade into it with one command, keep a free-plan project awake, apply a
release's migrations, and install the gateways that let people on email or Slack take part. Anyone who can read
its database can read every message, so it holds nothing else. A team can also run with no project at all, over
Syncthing ([Create a team over Syncthing](../team-admin/create/create-over-syncthing.md)); then there is nothing
here to run.

## If you want to

| If you want to | Go to |
| --- | --- |
| create the project, deploy the backend, arm the keep-alive, hand a team its values | [Install](install/README.md) |
| apply what a release changed in the backend, know which migration adds what | [Update](update/README.md) |
| install the mail or Slack gateway, choose who may write, review the project's settings | [Configure](configure/README.md) |
| stop the keep-alive, remove a gateway | [Remove](remove/README.md) |
| delete the project | [Delete](delete/README.md) |

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
