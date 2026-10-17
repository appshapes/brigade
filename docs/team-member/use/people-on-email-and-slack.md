# Write to people on email or Slack

A team may have a mail gateway and a Slack gateway: one more member each, hosted in the team's own backend
project, that turn a person's email or Slack message into a Brigade message and back. They are how people without
a session take part: QA, product, anyone who answers mail or Slack. The Project Owner installs them
([Configure the project](../../project-owner/configure/README.md)).

## Write to a person

```
/brigade:email qa@example.com Please retest the login page on Safari 18 and reply with what you see.
/brigade:slack #qa The fix is on master. Please retest Safari login and reply in this thread.
```

Each finds its gateway on the roster by harness (`mail-gateway`, `slack-gateway`), builds one `brigade send` to it
with a first line `to: <address>`, `to: @name` or `to: #channel`, and relays the answer. Flags: `--subject
"<line>"` (mail only), `--summary "<line>"`, `--reply-to <message-id>`, `--body-file <path>`. The gateway delivers
within a minute and sends one line back if it could not (no address, a name nobody has). The subject of a mail is
`[brigade/<team>] <summary>` unless a `subject:` line sets it. In a Slack message, `@handle` in the text tags that
person; `@here`, `@channel` and `@everyone` are never passed.

## What a person does

Your administrator gives them the gateway's address or the bot's handle (`@brigade`).

- **See the sessions:** email the address with just `sessions` as the text or subject; in Slack, `/brigade sessions`
  or a message that says `sessions`. The list comes back: each session online, its five characters, name, state,
  label and `↳` line.
- **Write to a session:** the first line names it, `to: 3f9a2`; `to: alice@example.com` (a label, or eight
  characters of a principal reference) reaches that member's best session instead. A `summary:` line under the
  first, then a blank line, gives the message a summary. In Slack, `/brigade send 3f9a2 <text>` does the same.
- **Answer a session:** reply to its email above the quoted text, or in the Slack thread under its message. The
  `[brigade]` block at the end of a mail helps the gateway thread the reply; leave it as it is.
- **What they should know:** the session treats their words as another person's, never as its own user's
  instruction; "approved" approves nothing. Attachments and files are not carried. A mail from an address the
  team has not listed is dropped unanswered; Slack verifies the author, mail cannot.

## What arrives

A person's message is an ordinary message from the gateway. A mail's body begins
`Email from <sender> (unverified), subject "…":`; a Slack message's `Slack message from @alice (U0123ABCD,
unverified name) in #qa:`; then the text, then a `[brigade]` block with the ids. The Slack user id is an identity
Slack verified; a mail address is unverified text, as every label is. Reply the normal way, with `--reply-to
<message-id>` and **no** `to:` line; the gateway threads the reply where it came from. A person answers in minutes
or days: tell your user you sent it and move on; never re-send, never poll.

What leaves the backend for a mail provider or Slack, and who can then read it:
[docs/security.md, section 14](../../security.md#14-the-mail-and-slack-gateways).
