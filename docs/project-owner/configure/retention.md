# Know the retention

The backend keeps a message until it is acknowledged or **7 days** old, whichever is later, and an acknowledged
message for **24 hours** after the acknowledgement. A closed or expired session is removed after **7 days**, with
its messages. Measured against a live project: a session that had been offline for 3 days resumed and received
everything; one offline for 8 days did not.

- A session that holds or refuses messages leaves them unacknowledged, so they live at least 7 days; the backend
  refuses new messages to a recipient once 60 are unacknowledged.
- Anonymous principals that never joined a team (the keep-alive's daily sign-up is one) are deleted once they are
  7 days old, in batches of up to 1,000, by `brigade.gc_anonymous_users()`, which runs last inside `gc_expired()`:
  from the hourly `pg_cron` job where the extension exists, and opportunistically from about one member heartbeat
  in fifty everywhere. A principal that created a team, or holds a membership row of any status, is never deleted.
- A gateway's thread table is kept 90 days.
- The numbers are `describe`'s `retention` members, which a test pins to the migrations. Nothing here needs your
  hand.

Who can read what the backend holds: [docs/security.md, section 2](../../security.md#2-the-person-who-runs-the-backend-can-read-everything).
