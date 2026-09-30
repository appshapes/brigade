# Release notes by email

Date: 2026-09-29. Status: **the first email was sent on 2026-09-29** (rows P25-1 and P25-2 of the execution
log; commits `a2c1ddd` and `c366b15`). Owner: Rjae. Trello card **43**.

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
- The Resend account had one verified domain, another product's. `appshapes.com` was added and verified on
  2026-09-29 (section 5), so the sender is `brigade@appshapes.com`.

## 3. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | **The list is fixed and kept by hand** (Rjae). It is the secret `RELEASE_NOTES_RECIPIENTS`, not a line of the workflow. | The repository is public: an address written into a file is published to everyone, for good. A secret is the same fixed list, and changing it is one command. |
| 2 | **Answering the email stops it.** The footer says so. The answer goes to the sender's address, which is an inbox Rjae reads; no Reply-To is set (Rjae, 2026-09-29, after the first email, which carried one). | There is no unsubscribe capability, and an email nobody can stop is not one to send. It needs no build. |
| 3 | **Addresses are secrets, masked, `bcc`, in no artifact**, and read after the last agent step. | The repository and its run logs are public. |
| 4 | **Weekly, Mondays 14:23 UTC, and by hand.** A run by hand is a preview unless it says `audience=recipients`. | The house's cadence. The safe default for the one step that cannot be undone. |
| 5 | **The window follows the last email**: the newest successful run whose send step succeeded; else seven days; `from` overrides, inclusive. | A fixed seven days would send 0.16.0 twice after a first email sent by hand. The job token cannot write a variable, and a tag would reach goreleaser's changelog. The run history needs `actions: read` and nothing else. |
| 6 | **The text is gated as the release notes are**: writer, lint, reviewer, one fix cycle. The rules of the email are `.claude/agents/release-notes-email.md`. | An email cannot be taken back. |
| 9 | **A send by hand can take the draft of an earlier run** (`draft_run`). The scheduled run always writes its own. | A preview that is read and approved, then replaced by a text nobody read, is not a preview. The schedule has no reader to wait for. |
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

## 5. What was measured after the commit

- **The preview run, 36626521185, green in 19 minutes.** The first draft was not approved, the writer fixed it, the
  second review approved it, the lint was clean. The preview was delivered to one address and the two recipient
  steps were skipped. The run's log and its artifact carry no recipient's address.
- **The sender's domain.** Rjae wanted `brigade@appshapes.com`. Google SMTP would have needed an app password made
  by hand and a send-as alias. Instead `appshapes.com` itself was added to Resend and four records were added at
  Hover, the domain's DNS: TXT `resend._domainkey`, MX `send`, TXT `send`, CNAME `rsend`. Resend lists the CNAME
  for domains created after August 2026, and the domain did not verify in 18 minutes without it. The 19 records
  that were there before are as they were. Verified at 21:39 UTC; a one-line test from the new address was
  delivered.
- **Hover has no public API.** The records went in through the control panel's own endpoints, signed in with
  Rjae's login and a code she read from her authenticator. The recovery code was never used.

- **The first send, run 36636440249, was refused by the gate, and nothing was sent.** It wrote a new draft, as
  every run does. The first review had three findings, the writer fixed them, and the second review had three
  others: 23 releases in 450 words leaves no room for one more thing a reader must know. The preview of the same
  releases had been approved. Hence `draft_run` and `send-release-notes.sh adopt`: a run by hand can send the
  draft an earlier run's gate approved, so that what was previewed is what is sent. Tried read-only against the
  two runs: the preview's draft is adopted byte for byte and lints clean, and the refused run is refused.

- **The first email went out in run 36639635317, 2026-09-29 22:27 UTC, in 36 seconds.** It adopted the draft of
  the preview run, byte for byte; no agent ran. The mail service records one email from `brigade@appshapes.com`,
  seven readers as `bcc`, delivered. The run's log shows the `bcc` and the Reply-To as `***`, and neither the log
  nor the artifact carries a recipient's address. `window`, run afterwards, finds that run as the last email and
  nothing to send.

## 6. What is open

- **A scheduled run the gate refuses sends nothing that week.** The mark stays where it was, so the next run
  covers those releases too. Whether one fix cycle is enough week after week is not measured.
- **The schedule is live**: Mondays 14:23 UTC, first on 2026-10-05.
- **Collecting addresses**: Rjae's next task for this card's session.
