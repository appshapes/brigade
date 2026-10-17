# Release

The current release is the one `plugin/bin/VERSION` pins. A release is one command on a clean `master`, verified
and published by the workflow the tag triggers.

## If you want to

| If you want to | Go to |
| --- | --- |
| cut a release | [Cut a release](#cut-a-release) |
| know how the release-notes email is made and sent | [Send the release notes](#send-the-release-notes) |
| know what a project owner does after a release | [Update the hosted projects](#update-the-hosted-projects) |

## Cut a release

1. Write the `## [<v>] — <date>` section of `CHANGELOG.md`; `make docs-check` must pass.
2. `make release version=<v> card=<n>`: pins both packages, builds reproducibly, commits `<n>: Release <v>`, tags `v<v>`, pushes.
3. `release.yml` guards, builds, verifies against the committed checksums, publishes, dispatches the notes.

Details: [Cut a release](cut-a-release.md).

## Send the release notes

1. `send-release-notes.yml` emails the releases published since the last email, weekly, to a list kept in a secret.
2. A change a reader needs to see gets one picture, made before the release by `make email-picture`.

Details: [Send the release notes](release-notes.md).

## Update the hosted projects

1. When the changelog says a migration or a gateway function changed, each owner runs `make backend-install` and
   redeploys the gateways.

Details: [Apply a release](../../project-owner/update/apply-a-release.md).
