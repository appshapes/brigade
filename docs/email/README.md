# Pictures for the release-notes email

The weekly release-notes email (`.github/workflows/send-release-notes.yml`) may show one picture under a change:
a terminal capture of the feature in use, the way a product newsletter shows a screenshot. The pictures live in
this directory. Nothing reads a picture before it is sent — the lint reads words, not pixels — so the rules
below are the whole safeguard, and `make test` checks the ones a program can.

## What a reader sees

- **A change with a picture** shows its heading, its sentences, then the picture, 520 px wide with a thin
  border, then its terminal card if it has one. The picture is served from this repository at the commit the
  email was sent from, so an email never changes under its reader.
- **A change without a picture** shows its heading, its sentences and, when the words to type are the point, a
  terminal card: the dark block with the exact words. Nothing marks the absence — no placeholder, no empty
  frame, no shorter item. Most changes have no picture, and that is the expected look: a picture is for a
  change a reader needs to see to recognise it (a new line in the roster, a new shape of message, a new prompt),
  not for every change.

## Who makes one, and when

The person releasing, at release time, before `make release`. One picture at most per change, and only for a
change that a member can see. A release that needs none is the common case.

## How

1. Run the feature in a terminal sized like a reader's and run

   ```sh
   make email-picture version=<x.y.z> slug=<lower-case-and-dashes>
   ```

   Drag a rectangle around what the reader should see. The target saves
   `docs/email/<version>-<slug>.png`, resamples it to 1040 px wide, and prints the line for step 3. macOS only
   (`screencapture`, `sips`); elsewhere, save a PNG by hand at 1040 px wide under the same name.

2. **Look at the picture before anything else.** No email address, no person's name, no secret or token, no
   session id or message of a real team, no customer's repository name. What is in the picture is in a public
   repository and in every reader's inbox for good.

3. Add one line at the end of the change's `CHANGELOG.md` entry, indented two spaces so it stays inside the
   bullet, with alt text that says what the picture shows in words:

   ```markdown
     ![The gateway's answer to a sessions mail, as a mail client shows it](docs/email/0.28.0-mail-summary.png)
   ```

   The writer of the email copies this line as it is, under that change's sentences. A picture the changelog
   does not name is never used.

4. Run `make email-pictures-check`, then commit the picture with the release.

## The rules a program checks

`brigade-release-email -images` (`make email-pictures-check`; `make test` runs it on every push through the
renderer's tests) refuses:

| | Rule |
|---|---|
| Name | `<version>-<slug>.png`: the release's version, a dash, lower-case letters, digits and dashes. `0.28.0-mail-summary.png`. |
| Format | PNG. |
| Width | 1040 px, exactly: twice the 520 px the email shows, so it is crisp on a dense screen. |
| Height | 200 to 1040 px. |
| Size | At most 300 KB: a reader on a phone fetches every picture. |
| Named | By a picture line in the CHANGELOG section of the version in its name; and every picture line in the changelog has alt text, points at a file that exists here, and sits in the section its name says. |

The email's lint adds, for the draft: a picture is a file of this directory (never a URL), it exists in the
checkout, its line has alt text, and there is at most one under a heading. What the picture shows is checked by
nobody but you (step 2).
