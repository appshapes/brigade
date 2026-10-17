# Hand over the values

The **project URL** and the **publishable key** are the only two values a team ever receives, and both are public
by design: Supabase says so, and the Team Admin commits them in `.brigade.json`.

| Value | Starts with | Where | Who may have it |
| --- | --- | --- | --- |
| Project URL | `https://<ref>.supabase.co` | the Connect dialog, or built from the reference | everyone |
| Publishable key | `sb_publishable_` | the last line of `make backend-install`; the Connect dialog; Project Settings → API Keys, tab "Publishable and secret API keys" | everyone |
| Secret key | `sb_secret_` | Project Settings → API Keys | nobody: it bypasses every access rule |
| Database password | none | chosen at creation | nobody |
| Personal access token | `sbp_` | Account Preferences → Access Tokens | you only |

Never use the `sb_secret_` key; the "Legacy" tab's JWT keys are not used. None of the last three is ever a
repository variable, a CI secret of this repository, or shipped in a package.

With the two values, the Team Admin runs [Create the team](../../team-admin/create/create-the-team.md). A project
that already holds a team can hold another, with the same two values.

What the owner of the project can read, and why the project is single-purpose:
[docs/security.md, section 2](../../security.md#2-the-person-who-runs-the-backend-can-read-everything).
