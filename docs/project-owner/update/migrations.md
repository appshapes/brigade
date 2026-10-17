# Know which migration adds what

**Plugin versions and migrations are independent, by design.** A member updates the plugin when they like and you
apply a migration when you like. A migration only ever *appends* RPC parameters with defaults, so an older adapter
works unchanged on a migrated project, and a newer adapter works on a project you have not migrated yet: it names a
new parameter only when it has a value for it, and when the project refuses one (`PGRST202`) it sends the call
again without it, drops that value rather than the call, says so once on stderr naming the migration, and checks
again every ten minutes. What a team loses until you migrate is only what the migration adds, per migration.

| Migration | Adds | Until applied |
| --- | --- | --- |
| `20260910193200_session_model_context` | the MODEL and CONTEXT columns of `brigade sessions` | both columns blank for the team |
| `20260917170000_session_human_label` | the member label, filled from each member's default at their next session start when the membership has none; a label a member chose is never overwritten | the label the registration offers is dropped; nobody has to rejoin, either order works |
| `20260920180000_session_brigade_version` | the VERSION column, which is how a team sees who is behind a release | the column is absent for the team; afterwards a blank cell means a plugin older than 0.10.0 |
| `20260923022323_session_sync_peer` | each session's Syncthing device id, which folder sync needs to introduce machines | no checkout is introduced to another, so listed folders sync nowhere; a team that lists none loses nothing |
| `20261003160000_mail_gateway` and later | the gateway tables and settings; applied by the gateway installers | no gateway |

`make backend-install project=<ref>` applies whatever is pending, and `npx --yes supabase@2.116.0 migration list
--project-ref <ref>` tells you where each project stands. Nobody has to be told to do anything in particular.
