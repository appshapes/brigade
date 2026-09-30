# Launch plan — card 41

The demo is published: https://youtu.be/4ELiHEaAagY ("Brigade: distributed Claude Code collaboration", 2:14,
synthetic narration). This is the order of the launch, what each step needs, and the words to use. Research
behind it: the 2026-09-27 competition and marketing pass (no lab ships cross-user session messaging; the direct
third-party rivals are aweb and Agent Relay; demand is on anthropics/claude-code issues #87954 and #28300).

## Order

| Step | Who | State |
| --- | --- | --- |
| 1. Repository ready to be found: description, topics, homepage, the video at the top of the README, `displayName` and `homepage` in the manifest, a "What leaves your machine" section in the plugin README | session | done 2026-09-30 |
| 2. Submit to Anthropic's directory | Rjae, at the portal; the session prepared the answers below | ready |
| 3. Reply on issue #87954 | Rjae's GitHub account; draft below | drafted, awaiting a go |
| 4. Show HN and r/ClaudeAI | Rjae's accounts; drafts below | drafted |
| 5. Creators, earned before paid | Rjae; note below | drafted |

Steps 3 to 5 do not wait on step 2: a listing can take days, and each post can be edited to add the directory
link when it exists.

## Step 2: the directory submission

Portal: https://claude.ai/directory/manage — **Submit new** › **Plugin bundle**. Submitting from a Max plan
works from your own account. Your GitHub account must be connected in claude.ai and able to push to
`appshapes/brigade`.

| Portal step | Answer |
| --- | --- |
| Repository | `appshapes/brigade` |
| Plugin path | `plugin` |
| Branch or tag | leave empty (follows `master`) — every release is a commit on `master` with a raised `version` |
| Validate | `claude plugin validate --strict plugin` passes locally; the portal runs more checks |
| Listing details | read from `plugin/.claude-plugin/plugin.json` and `plugin/README.md`; check the name reads "Brigade" |
| Data handling: reads or stores personal data? | **Yes.** The member's display label, by default the Claude account's email address, read from Claude Code's configuration and stored by the team's own Supabase project; message text; session names and the one-line roster sentence. |
| Data handling: sends data to services other than declared connectors? | **Yes.** To the Supabase project the team's administrator owns (named in the project's `.brigade.json`), and a one-time download of the release binary from GitHub Releases. Nothing to AppShapes or Anthropic. |
| Data handling: how long is data kept? | Unacknowledged messages at least 7 days; acknowledged ones may be deleted 24 hours after acknowledgement; closed sessions and their messages after 7 days. All in the team's own project. |
| Data handling: for people under 18? | No. |
| Compliance | contact email: yours; select the four acknowledgements |
| How new versions reach the directory | GitHub push webhook (needs admin on the repository), or scheduled check |
| Auto-publish | on, if offered |

What to expect from the scan, from the pre-submission checklist:

- **A hold for a reviewer is likely, not a block.** `bin/brigade` is a shell script that downloads and runs a
  binary the validator cannot follow ("Scripts the validator couldn't follow"). The plugin README now states
  what is fetched, sent, read and run, which is what the scan asks for. A hold means an Anthropic reviewer reads
  it before it goes live.
- Everything else in the checklist is met: text files only, no symlinks, every file under 256 KiB, hook commands
  are `${CLAUDE_PLUGIN_ROOT}/bin/brigade`, a README over 40 words, `license` in the manifest, kebab-case name,
  `description`, `author` and `version` set.
- Once listed, a Claude Code user who adds it on claude.ai gets it as `brigade@synced`. The install lines in the
  README and the video's last card still work regardless.

## Step 3: the reply on anthropics/claude-code#87954

Post as a comment from Rjae's account. It answers the issue's proposed shape point by point, in its own words,
and claims nothing the video does not show.

```
We built this as a plugin, and it has been our team's daily tool for a month: https://github.com/appshapes/brigade

What it does, against the shape proposed above:

- Each person keeps their own session. A session sends a message to a teammate's session, on another machine and
  another Claude account, and the message arrives inside that session marked as coming from a teammate. No shared
  session, no shared file.
- Addressing: a roster of the team's live sessions, with a one-line "what I'm working on" sentence each session
  writes itself. The sending session picks the recipient from it.
- Per-message approval: a plugin option `team_inbound` is `accept` (deliver), `hold` (record, deliver nothing until
  you release it yourself), or `refuse`. A message can never approve a permission prompt or count as consent; the
  receiving session's own permissions still apply.
- Opt-in and revocable: a team is created by one person, joined with a secret file, and members can be revoked and
  the secret rotated.
- The backend is a Supabase project your team owns; nothing goes through us. The wire protocol is documented, and
  other backends can be plugged in.

The narrower case in the comment above, one person's sessions across several accounts, is the same mechanism: our
two-minute demo was recorded on one machine with two accounts. https://youtu.be/4ELiHEaAagY

It is MIT, and the security doc says what it protects and what it does not:
https://github.com/appshapes/brigade/blob/master/docs/security.md
```

When the directory listing is live, edit the comment to add one line: "It is also in the Claude plugin directory:
<link>."

## Step 4: Show HN and r/ClaudeAI

Show HN title (under 80 characters; HN removes "Show HN:" from nothing, keep the prefix):

```
Show HN: Brigade – Claude Code sessions of different people message each other
```

Show HN first comment (the text field is left empty for a Show HN with a URL; this goes in the first comment):

```
Claude Code can message your own other sessions, but not a teammate's. Brigade is a plugin that makes that work
across people and machines: a session sends a message, the teammate's session receives it mid-task, acts on it and
replies. There is a roster of live sessions with a one-line "what I'm working on" each session writes itself, so a
session can pick who to tell.

Messages are stored in a Supabase project your team owns; nothing passes through us. Inbound can be accept, hold for
your review, or refuse. A message can never approve anything on the receiving side.

Demo, two minutes: https://youtu.be/4ELiHEaAagY
Security doc, what it protects and what it does not: https://github.com/appshapes/brigade/blob/master/docs/security.md

Written in Go, MIT. Happy to answer questions about the protocol or the threat model.
```

r/ClaudeAI (and r/ClaudeCode) title and body:

```
Brigade: a plugin that lets your teammate's Claude Code session message yours

Claude Code's cross-session messaging only reaches your own sessions. We wanted our team's sessions to talk to each
other, so we built a plugin. Your session sends a message; your teammate's session gets it inside its context,
marked as from a teammate, and can act and reply. There is a roster of who is running what.

Two-minute demo: https://youtu.be/4ELiHEaAagY
Repo (MIT): https://github.com/appshapes/brigade

Install:
/plugin marketplace add appshapes/brigade
/plugin install brigade@brigade

You need a Supabase project for the team (free plan works); the setup guide walks through it.
```

## Step 5: creators

Send to creators who cover Claude Code, as a note, not a pitch. Theo (t3.gg) first; sponsorship stays off until
there is a number to set against it.

```
Subject: Two people's Claude Code sessions talking to each other (2-minute demo)

Hi <name>,

Claude Code can message your own other sessions but not a teammate's. We built a plugin that does the second
part: one developer's session messages another's, on another machine and account, and it arrives inside the
session, which acts and replies. Two minutes, no voice-over tricks, two real sessions:

https://youtu.be/4ELiHEaAagY

Open source, MIT, backend is a Supabase project the team owns: https://github.com/appshapes/brigade

If it is of interest I can set you up with a team to try in ten minutes. No ask beyond that.

Rjae
AppShapes
```

## What was set on 2026-09-30

- GitHub description: "Team messaging between the Claude Code sessions of different people and machines".
- Homepage: the video. Topics: claude-code, claude-code-plugin, ai-agents, team-collaboration, messaging,
  supabase, golang.
- `plugin/.claude-plugin/plugin.json`: `displayName` "Brigade", `homepage` the repository.
- `README.md`: the video under the first paragraph. `plugin/README.md`: "What leaves your machine, and where it goes".
