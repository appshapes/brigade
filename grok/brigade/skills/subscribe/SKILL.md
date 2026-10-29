---
name: subscribe
description: >-
  Subscribe the user to the project's Brigade digest, or unsubscribe: `/brigade:subscribe` runs `brigade report subscribe`
  for the user's own roster label, so the team's sessions send them the digest on the period and a
  `brigade decision --to <their label>` reaches them at once; `--recipients "<@name | #channel | address>"`,
  `--every <duration>` or `--at <HH:MM> --tz <zone>`, and `--only mine` are the only flags; `/brigade:subscribe off` runs
  `brigade report unsubscribe`. Only the user's own entry changes: nobody subscribes anyone else. Use when the
  user asks to get, receive or stop the digest, the report or the decisions for them, or asks to subscribe or
  unsubscribe. A project with no state folder has no digest.
---

# Subscribe to the digest, or unsubscribe

In a project whose `.brigade.json` names a state folder, the team's own sessions produce a digest from it — the
decisions waiting on a person and the contributions since the last one — and send it to everyone who opted in,
each by their own precedence. The subscription is your user's own act from this session. `$ARGUMENTS` is what
came after `/brigade:subscribe`: typed by the user, or passed by you when they asked in words and you loaded this skill.

1. Read `$ARGUMENTS`, ignoring surrounding whitespace. These are the only forms, and the flags combine:

   | `$ARGUMENTS` | Meaning |
   | --- | --- |
   | empty | subscribe with the defaults: the user's own label as the recipient (an email address, which the Slack gateway finds the person by, or the mail gateway carries), every 24 hours, every decision |
   | `--recipients "<list>"` | where the digest and the decisions for the user go, in order of precedence, separated by `\|`: a Slack handle (`@name`), a channel (`#name`), an email address |
   | `--every <duration>` | the period, such as `4h` or `24h` |
   | `--at <HH:MM> --tz <zone>` | a daily digest at that time in that IANA zone, instead of `--every` |
   | `--only mine` | the digest's decision sections list only the decisions addressed to the user; the contributions stay the team's |
   | `off` | unsubscribe |

   `<list>` and `<zone>` are the user's words, never ones you chose. For anything else, say which forms this
   skill takes, and stop. Never a flag the user did not ask for, never another invocation form after a refusal.

2. **Subscribe.** Run exactly one command, with the flags as given:

   ```bash
   brigade report subscribe --recipients "@rjae | rjae@example.com" --every 24h
   ```

   Print what it printed, verbatim. Its one line names the label the entry was written under, the recipients,
   the period and the filter. Then say, in one line, that the digest comes from whichever teammate's session is
   active, on the period, and that `/brigade:subscribe off` undoes it.

3. **`off`.** Run `brigade report unsubscribe` and print what it printed, verbatim.

4. **A refusal** is final for this conversation: print it once and stop. The ones to expect: `config` with
   `folder_not_ignored` — an email address (a given one, or the user's own label as the default) goes into the
   folder only when git ignores it; tell the user to add the state folder to `.gitignore` or to name a Slack
   handle or channel instead, and run nothing else. `config` with `no_state_folder` — the project shares no state
   folder, so there is no digest here. `config` with `no_state_entry` — the session's entry is not there yet; the
   user's next prompt creates it. `config` with `label_needed` — the session has no roster label; the `label` option
   names one. `not_found` with `not_subscribed` on `off` — nothing to undo.

Rules that hold throughout:

- The entry is the user's own: this skill never writes, removes or names anyone else's, and never takes a
  `--label`, because there is none. An administrator subscribes nobody.
- A recipient is sent as the user wrote it; the command refuses any other shape. No local paths, no secrets.
- Only this project's digest changes: no option, no settings file, no other project.
- A subagent never runs any of this.

What the digest holds, `brigade decision --to <label>` and `brigade report show` are in the `team-messaging` skill.
