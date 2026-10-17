# Review the project's settings

`make backend-install` applies four settings through the Management API, one field at a time, and reads every one
back; `scripts/backend-settings.sh <ref>` alone re-checks them and reports what it found, changing nothing that is
already right (`--dry-run` changes nothing at all). The dashboard equivalents, if you would rather click:

- **Settings → API → Exposed schemas**: add `brigade` (keep `public` and `graphql_public`). Only after the
  migrations have run.
- **Authentication → Sign In / Providers → Allow anonymous sign-ins**: on. Every Brigade principal is an
  anonymous user; membership, not anonymity, is the authorisation gate. Leave CAPTCHA off in the same place: a
  command-line client cannot solve a browser challenge.
- **Authentication → Sessions**: leave the time-box and the inactivity limit **unset**. They silently kill idle
  principals, which is exactly what a long-running session looks like.
- **Realtime → Settings → Allow public access**: **off**. Brigade only ever joins private channels; with this off,
  a public join is refused outright rather than quietly receiving nothing.

Not an option: `supabase config push`, which would send this repository's local-stack `config.toml` whole
([Deploy the backend](../install/deploy-the-backend.md)).

## The Bash sandbox host

Members whose Claude Code sessions run with the Bash sandbox on add the project's host, `<ref>.supabase.co`, to
`sandbox.network.allowedDomains`; tell them the host when you hand over the values
([Run headless or sandboxed sessions](../../team-member/configure/headless-and-sandbox.md)).
