# E19: `brigade:work-item-start` names the session and leads the user's other sessions (work item 303)

**Date** 2026-11-13. **Driver** `scripts/experiments/E19-work-item-start-leads.sh`, run from this checkout on the
dev build `0.53.1-dev` over `74dcbba` plus the work item's working tree, as it was committed. **Machine** this one:
macOS, Claude Code 2.1.325, the sessions' model Opus 5.5, tmux 3.7c. **Backend** the fs adapter in a throwaway root: a scratch
team, `lead-proof`, with a board of its own. No hosted team, no real board and no gateway was involved, and
nothing was sent to anyone.

## What the work item changed

The skill `work-item-start`, in all four packages, now does three things it did not
([Start a work item, alone or with your other sessions](../team-member/use/work-items.md#start-a-work-item-alone-or-with-your-other-sessions)):

1. It names the session `<number>-<slug>` after the work item, unless the name already begins with the number.
2. With other sessions named after the number, it leads them as followers: only the user's own sessions,
   recognised by `principal_ref`, each with a first message of its own, and the leader alone touching the work
   item.
3. It puts the project's own instruction files first, and carries the work through to the list for finished
   work once the approach is agreed.

The skill is text a model follows, so the proof is what real sessions do with it.

## The rig

One temporary root holding its own XDG directories, so the dev-binary pointer, the fs store, the board and the
session maps are throwaway and the machine's own are never read or written. A project, Lanternfish, with a bare
origin repository and two clones of it; the team file turns the board on. Every session runs the plugin of this
checkout (`--plugin-dir`, the installed Brigade plugin switched off) in the default permission mode.

- **The observer** is a session of the same member, alice, driven by the real `brigade hook` subcommands with a
  sleeper as `CLAUDE_PID`. It adds the four work items up front and reads the board and the roster after each leg.
- **bob** is a second member, driven the same way from a config directory of his own. He joined with the **same
  label as alice**, `alice@lead.invalid`: his session `helper-c` shows her label in the MEMBER cell and carries a
  `principal_ref` of his own.
- **The second clone** joined as a second checkout of a team the machine holds does, from inside a session there
  with no secret.

A plain `brigade sessions --json` marks no row `is_self`: the session's own row is the one whose `session_id` is
the result's `self_session_id`, which is what the skill says to compare a follower's `principal_ref` with.

## The four legs

One run, forty-two assertions, forty-two ok. The session of each headless leg is one `claude -p`; the three
of the `team` leg are held open on a FIFO, as [E17](E17-plan-invitation.md)'s are, so a message can wake them;
the `live` leg's is an interactive session in tmux.

| Leg | Assertion | Outcome |
| --- | --- | --- |
| setup | The team has a board; the second clone joined with no secret; bob's session `helper-c` is on the roster under alice's label with a `principal_ref` of his own; no row of a plain roster is `is_self`; the observer added work items 1 to 4 | ok |
| solo | `/brigade:work-item-start 1`, nobody else named, and no `brigade` command allowed on the command line: the session ran `show 1`, `assign 1 --mine`, `lists`, `move 1 doing`, `brigade doing`, then `comment 1`, `move 1 done` and a second `brigade doing` saying the work item is in Done | ok |
| solo | It read its name with `brigade whoami` and ran one `brigade rename`, with the name the slug rule gives for "Say the project's hello in a HELLO.md at the top": `1-say-the-projects-hello-in-a-hello` (the apostrophe dropped, cut at a hyphen under 35 characters). The command answered `recorded: 1-say-the-projects-hello-in-a-hello` | ok |
| solo | It read no roster and sent no message; `HELLO.md` holds `hello`; work item 1 is in Done, assigned, open, with one comment; its report leads with the list and says there is no commit | ok |
| solo | No tool call was denied: the skill's `allowed-tools` granted `brigade work-item`, `doing`, `whoami` and `rename` | ok |
| ask | `/brigade:work-item-start 2 nobody-here helper-c` in a session named `2-my-own-name-for-it`, again with no `brigade` command allowed on the command line: it ran `brigade whoami` and no `brigade rename`, and said the name already begins with `2-` | ok |
| ask | It ran `brigade sessions --json` and sent no message. Its answer: no live session is named `nobody-here`; `helper-c` is live and carries the user's label, "but it belongs to a different person on the roster ... Anyone can type that label ... If `helper-c` is your own session on another machine, only you can tell me that"; then it asked how to go on | ok |
| ask | Work item 2 stayed in Doing; no tool call was denied, so `allowed-tools` granted `brigade sessions` | ok |
| team | The leader, `helper-a` and `helper-b` are one principal on the roster. `/brigade:work-item-start 3 helper-a 54395`, the second follower named by its SESSION cell; the user had said beforehand which follower shares the leader's working tree | ok |
| team | The leader ran `brigade sessions --json`, named itself (`recorded: 3-document-the-two-commands-and`) and sent each follower one first message, each with a summary that begins `3: ` | ok |
| team | Each first message gives that follower one page, says the leader leads and the follower never comments on, moves or closes the work item, states the contract (which file is whose, in both messages) and what to report | ok |
| team | `helper-a`, in the leader's working tree, was told "don't commit, stage or push anything. I'm the one committer in this working tree"; it ran no `git commit` and no `git push`, and reported its file | ok |
| team | `helper-b`, in the second clone, was told to commit and push its page under the repository's own rules; it did (`e36b767`); the leader merged it with its own commit, then read the page and the commit before it wrote the landing comment | ok |
| team | The leader committed `docs/USAGE.md` and the README line in the shared tree (`f695329`), pulled with a merge and pushed: the origin's `main` holds both pages and the line | ok |
| team | Neither follower ran a `brigade work-item` verb that changes the work item; every comment on it is the leader's; it is in Done, assigned to the member | ok |
| team | The landing comment names both followers' work: "docs/USAGE.md: written by helper-a in the lead's working tree, committed by the lead in f695329 ... docs/FAQ.md: written, committed and pushed by helper-b from its own clone in e36b767", how it was checked, and that nothing is open | ok |
| team | The leader wrote to each follower again once the work item was in Done ("Work item 3 is finished ..."); no tool call was denied in any of the three | ok |
| live | `/brigade:work-item-start 4` typed into an interactive session named `e19-live`: it carried the work item to Done. Before the next prompt the roster still said `e19-live` | ok |
| live | After a second prompt ("Thank you.") the roster names the session `4-name-the-two-commands-the-readme` | ok |

The work item reached Done 50 seconds after the leader was given the command: three turns for each of the
three sessions, the followers' reports waking the leader. A session held open this way takes a delivered
message as a prompt, so the leader's own name was applied during the leg and its comment carries the new name.

**What reached the backend.** The board's 18 change files are under `boards/` and nowhere else. The title of
work item 3 is in two of the backend's files, the leader's two first messages, which begin with it as the skill
says; the titles of work items 1, 2 and 4 are in none. A session's name carries the number and the slug to the
roster, which is the backend's. The page says so
([Start a work item, alone or with your other sessions](../team-member/use/work-items.md#start-a-work-item-alone-or-with-your-other-sessions)).

## What the runs changed

Each leg was first run alone. The whole driver then ran twice: the run recorded above is the second, and the
first on the text as committed.

- **The first `team` run**, alone, failed on the driver, not the skill: a helper called in a command substitution lost
  its list of known sessions, so all three sessions were given one id and the leader was handed one session
  under two names. It stopped before any message, said that `helper-a` and the SESSION cell name the same
  session, and asked which was meant. Nothing in the skill covers that case by name; the rule it followed is the
  one for a name that does not resolve to one live session of the user's.
- **The agent review**, read while the first whole run was under way, changed the skill's text in four places: the
  name goes onto the roster, where the board does not, so a customer, a host or a person that the title names
  is left out of the slug; a follower is written to with `brigade send` though it is the user's own session
  (the `team-messaging` skill's first table sends a reader to the built-in tools for those); a session of the
  user's on another machine has a `principal_ref` of its own, reads as another member's, and follows only on
  the user's word (the `ask` leg's answer above is on that text); and a leader whose own part is done says that
  it waits on a follower's report and stops.
- **The first whole run's `live` leg** stalled at a permission prompt: its work item asked for a file, the session
  wrote it with a shell redirection, and nobody was at the keyboard. The leg's work item now changes no file.
  The `live` leg run alone before it, on a work item that asked for a file the session wrote with the Write
  tool, had already shown the name applied. The first whole run's `team` leg passed as the second's did.

## What it does not show

- **One model, one host.** Every session was Claude Code on Opus 5.5. On Muse Code, Codex CLI and Grok Build
  `brigade rename` prints its `not recorded:` line at exit 0, which `internal/harness/commands`' tests pin; no
  session of those hosts ran the skill here.
- **Followers the user had spoken to.** Each follower's user had told it that a session of theirs would send it a
  part. What a session does with a first message that comes with no word from its user was not measured here;
  [E17](E17-plan-invitation.md)'s control legs measured that for an ordinary message.
- **One run a leg**, on the build as committed. A model's wording varies from run to run; the assertions are on
  what it ran and what it left on the board, and the evidence under `.ignored/work-item-303/e19/` holds the rest.
