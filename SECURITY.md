# Security

## Reporting a vulnerability

Report it privately through GitHub: **Security → Report a vulnerability** on
[appshapes/brigade](https://github.com/appshapes/brigade/security/advisories/new). Private vulnerability
reporting is enabled for this repository (2026-09-08), so the report is visible only to the maintainers until a
fix is published. Please do not open a public issue for a suspected vulnerability, and do not include a join
secret, a credential file or a Supabase key in the report — describe where they would leak, not their values.

## What to expect

A maintainer reads the report and answers in the advisory thread. Brigade is a small project with no security
team and no response-time commitment; what it does promise is that a confirmed vulnerability is fixed in a
release before the advisory is published, and that the fix is recorded in [`CHANGELOG.md`](CHANGELOG.md).

## Supported versions

The latest release only: the plugin pins one binary per version and every session downloads the version its
plugin names, so a fix ships as a new release and users move to it with `/brigade:update`.

## What Brigade protects, and what it does not

[`docs/security.md`](docs/security.md) is the threat model as shipped: what a teammate's message can and cannot
make a session do, where each secret lives, what the Bash sandbox changes, and the residual risks the project
has chosen to document rather than fix. Read it before deciding whether a finding is a vulnerability or a
documented limit.
