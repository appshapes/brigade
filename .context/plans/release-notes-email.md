# Release notes by email

Date: 2026-09-29. Status: **built and committed; a preview is the next step; nothing sent to the list yet** (row
P25-1 of the execution log). Owner: Rjae. Trello card **43**.

## 1. The ask

Rjae, 2026-09-29: send Brigade's release notes by email, preferably only to the people who starred the
repository; no subscribe/unsubscribe capability for now; be informed by
`thinktech-php/.github/workflows/send-release-notes.yml`. For the first email, pick the version to start from.
The same day, after the measurements of section 2: the first email starts at 0.5.0; the mail service is Resend;
"we're going to need a different source (and repository) for persons of interest (subscribers). Let's hard code
the email addresses for now"; collecting addresses is the next task.

## 2. What was measured first

- The repository is public and had 4 stargazers, all from 2026-09-27 and 2026-09-28.
- **GitHub gives out an address only when its owner made it public.** Of the 4, one profile shows an address. An
  unauthenticated request shows none at all, for anybody. That is why the stars were dropped as the source.
- 28 releases in 23 days: an email per release would be a flood. 23 of them are 0.5.0 or later.
- `release-notes.yml` already writes gated notes onto every release page, and `CHANGELOG.md` has a section per
  version. Both are sources an email can be composed from.
- The Resend account has one verified domain, and it is another product's. No domain of Brigade's own is
  verified there.

## 3. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | **The list is fixed and kept by hand** (Rjae). It is the secret `RELEASE_NOTES_RECIPIENTS`, not a line of the workflow. | The repository is public: an address written into a file is published to everyone, for good. A secret is the same fixed list, and changing it is one command. |
| 2 | **Answering the email stops it.** The footer says so, and `RELEASE_NOTES_REPLY_TO` is the inbox the answer reaches. | There is no unsubscribe capability, and an email nobody can stop is not one to send. It needs no build. |
| 3 | **Addresses are secrets, masked, `bcc`, in no artifact**, and read after the last agent step. | The repository and its run logs are public. |
| 4 | **Weekly, Mondays 14:23 UTC, and by hand.** A run by hand is a preview unless it says `audience=recipients`. | The house's cadence. The safe default for the one step that cannot be undone. |
| 5 | **The window follows the last email**: the newest successful run whose send step succeeded; else seven days; `from` overrides, inclusive. | A fixed seven days would send 0.16.0 twice after a first email sent by hand. The job token cannot write a variable, and a tag would reach goreleaser's changelog. The run history needs `actions: read` and nothing else. |
| 6 | **The text is gated as the release notes are**: writer, lint, reviewer, one fix cycle. The rules of the email are `.claude/agents/release-notes-email.md`. | An email cannot be taken back. |
| 7 | **The first email starts at 0.5.0** (Rjae). | Everything before 0.5.0 is install and join mechanics that the README now covers. From 0.5.0 each release adds something a member sees. It is grouped by what a member can do, not by version. |
| 8 | **Resend over SMTP** (`smtp.resend.com`, port 465, user name `resend`, the API key as the password), through the mail step the house already uses. | Rjae's account and key. The workflow stays the house's shape, and another mail service is five settings away. |

## 4. What was built

- `.github/workflows/send-release-notes.yml`
- `scripts/ci/send-release-notes.sh` (`window`, `recipients`, `finish`) and `scripts/ci/send_release_notes_test.go`
- `scripts/ci/release-notes-lint.sh`: the `email` kind (`BRIGADE_NOTES_KIND=email`), with its tests
- `.claude/agents/release-notes-email.md`
- `CHANGELOG.md` (Unreleased), `scripts/ci/README.md`, `docs/development.md`, `CLAUDE.md`

The first design read the stargazers and resolved each to an address (a hand-kept `<login> <address>` list, then
the profile's public address). It was built, tested and replaced the same day, before any commit.

## 5. What is open

- **The sender's domain.** The one verified domain is another product's. A domain of Brigade's own needs DNS
  records that only Rjae can add.
- **Collecting addresses**: Rjae's next task for this card's session.
- **The first send**, after Rjae has read the preview.
