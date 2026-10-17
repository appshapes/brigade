# Send the release notes

Once a week `send-release-notes.yml` emails the releases published since the last email to a list of recipients,
laid out as HTML by the dev-only `cmd/brigade-release-email`; `release.yml` dispatches it after a publish too. The
release-notes agent (`.claude/agents/release-notes-email.md`) writes and reviews the text; `scripts/ci/release-notes-lint.sh`
lints it.

- **The recipients** are a list kept by hand in the secret `RELEASE_NOTES_RECIPIENTS`. This repository is public,
  so the addresses are secrets, never variables and never a file; every reader is `bcc`; the list is read after the
  last agent step. Never print, upload, commit or put on a card a recipient's address.
- **The gate.** A writer drafts, the lint and a reviewer read it, the writer gets one fix cycle, and the second
  review is bounded to the first review's findings and the lines the fix changed (the first draft is kept beside
  the second), because a finding first raised after the last fix can only refuse the email: runs 36636440249 and
  38265059855 were refused that way. What the second reviewer notices outside the bound goes after a `NOTES` line
  in the artifact's `review.md`. A refusal moves no mark, so the next email covers those releases too, and the
  brief's word cap grows with the window.
- **The send step's name** is how the next run finds the last email: rename it in the workflow and in
  `scripts/ci/send-release-notes.sh` together.
- **Pictures.** A release may show one picture under a change a reader needs to see to recognise it; most need
  none. The person releasing makes it before `make release` with `make email-picture version=<v> slug=<s>` (a
  1040 px capture into `docs/email/`), and `make email-pictures-check` (in `make test`) holds the rules of
  [docs/email/README.md](../../email/README.md).
- Running it locally and triggering it by hand: [scripts/ci/README.md](../../../scripts/ci/README.md).
