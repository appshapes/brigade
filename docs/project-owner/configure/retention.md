# Know the retention

The backend keeps a message until it is acknowledged or **7 days** old, whichever is later, and an acknowledged
message for **24 hours** after the acknowledgement. A closed or expired session is removed after **7 days**, with
its messages. Measured against a live project: a session that had been offline for 3 days resumed and received
everything; one offline for 8 days did not.

- A session that holds or refuses messages leaves them unacknowledged, so they live at least 7 days; the backend
  refuses new messages to a recipient once 60 are unacknowledged.
- **A team that nobody uses is deleted.** A team older than 7 days that has at most one membership and no session
  seen within 7 days is deleted with its membership, by `gc_expired()`; the principal that created it then falls
  under the next rule. Someone who tries Brigade alone and comes back after a week finds no team, and creates it
  again. A team with more than one membership, or with a session seen in the last 7 days, is kept. The record of
  a move goes with the team: once a team with one membership has moved to a Brigade server and is deleted here, a
  checkout that has not followed finds no record, stays where it is, and joins the server with the team's join
  secret file instead ([When your team moves](../../team-member/join/follow-a-move.md)).
- Anonymous principals that never joined a team (the keep-alive's daily sign-up is one) are deleted once they are
  7 days old, in batches of up to 1,000, by `brigade.gc_anonymous_users()`, which runs last inside `gc_expired()`:
  from the hourly `pg_cron` job where the extension exists, and opportunistically from about one member heartbeat
  in fifty everywhere. A principal that created a team, or holds a membership row of any status, is never deleted.
- A gateway's thread table is kept 90 days.
- The numbers are `describe`'s `retention` members, which a test pins to the migrations. Nothing here needs your
  hand.

## What each backend keeps

The rules above are the Supabase backend's. A Brigade server keeps the same three windows (the constants of
`server/crates/brigade-core/src/limits.rs`) and never removes a team by itself:

| | A Supabase project | A Brigade server |
| --- | --- | --- |
| An unacknowledged message | at least 7 days | at least 7 days |
| An acknowledged message | 24 hours after the acknowledgement | 24 hours after the acknowledgement |
| A closed or expired session, with its messages | 7 days | 7 days |
| A team that nobody uses | deleted once it is 7 days old, with at most one membership and no session seen in 7 days | kept: only the server's operator removes a team, with `--remove-team` ([Remove a team](../server/name-the-owner.md#remove-a-team)) |
| Mail through a provider other than Resend and Postmark | a connector of your own ([docs/mail-connectors.md](../../mail-connectors.md)) | not offered: the server speaks to Resend and Postmark itself ([Open the gateways](../server/gateways.md)) |

Who can read what the backend holds: [docs/security.md, section 2](../../security.md#2-the-person-who-runs-the-backend-can-read-everything).
