# 0005. Ask for a note only for a commit made in the checkout during the turn, and say what git shows

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** none

## Context

In a project that shares a state folder, the Stop hook asks the model for a note after a commit (card 162). Its
sentence read "this turn made a commit and no note followed". The gate behind it read the newest commit in the
session's state entry and compared its time with the last note and the last ask. The entry's commits are
`git log <the session's start head>..HEAD` by the checkout's `user.email`: every commit of that author that
reached the checkout since the session started. So the gate knew neither the turn nor the session, and the line
was wrong in three ways (work item 319):

1. A commit another session of the same member made in the same checkout counted, because the sessions share
   HEAD, its reflog and their author.
2. A commit of an earlier turn counted, when the ten-minute floor or "never twice in a row" had silenced its own
   turn's ask: the ask came at a later Stop, in that turn's name.
3. The member's own commit made in another checkout counted once a pull brought it in.

Git's files hold nothing that tells two sessions of one checkout apart: a commit and its reflog entry carry the
author, the committer and a time, and those are the same for both.

## Decision

The Stop hook asks for a note only for a commit that was **made in the session's checkout, after the prompt that
began the turn**. "Made in the checkout" is the entry's `made` flag, which HEAD's reflog gives: a commit, an
amend, a cherry-pick or a merge commit, never a fast-forward. "After the prompt" compares the commit's time, which
git keeps in whole seconds, with the second of the prompt: a commit in that same second is not the turn's. The
commit is one of the entry's (the session's author, since the session started), newer than the last note and
than the last ask.

The sentence says what the hook found and leaves the rest to the model, which knows whether it committed: "a
commit was made in this checkout during this turn and no note followed; if this session or an agent it started
made it, record one line …; if another session made it, nothing is needed." It stays a constant: no hash, no
subject, nothing read from the repository.

A commit is asked about at the Stop of its own turn or not at all. One whose turn ended without that ask is not
asked about later: the cadence silenced the ask, the turn was interrupted, or the commit landed while the session
waited (a command or an agent it left running). A session with no prompt stamped has no turn to judge and is
asked nothing.

A host with no Stop hook (Muse Code, Codex CLI) carries the ask at the next prompt and has nothing that marks
where a turn ended. There the commit is one made after the previous prompt, the time the session waited
included, and the sentence says so: "a commit was made in this checkout since this session's previous prompt".

## Alternatives considered

- **Keep the gate and change only the words** to "a commit of yours reached this checkout since your last note".
  Lost: true, and still asked of every session of a shared checkout for every commit, at the frequency the owner
  saw.
- **Remember a commit whose turn's ask was silenced, and ask at a later Stop.** It keeps every note the earlier
  gate would have asked for. Lost: the line then comes in a turn that has nothing to do with the commit, which is
  the third sighting on the work item, and the owner's complaint is the number of these lines, not a missing
  note; the model also writes a note after a commit on its own judgement (the `team-messaging` skill). It needs
  a record of the owed commit across turns, which is the first thing to build if the digests turn out thin.
- **Attribute the commit to a session with a hook on every tool call** (record HEAD before and after each Bash
  call). Lost: one more process per tool call in every session of every team, a new hook in four packages, and
  still wrong when one session's long command overlaps another's commit.
- **Read the session's transcript for the commit.** Lost: Brigade reads two facts from the transcript, the model
  and the context in use, and `docs/security.md` says nothing else is read out of it, no tool call among them; a
  nicety does not earn that read.
- **Name the commit's hash in the sentence**, so the model can check it. Lost: the ask's sentences are constants
  with nothing from the repository in them, and the model already knows whether its turn committed.

## Evidence

- Work item 319: four sightings on 2026-11-13 and 2026-11-14, on plugin 0.54.0 and 0.55.0, each in a turn that
  made no commit; one named the other session's two commits.
- The state folder of this repository's team on 2026-11-14, read on the owner's machine: 69 entries listing 109
  distinct commits, 106 of them listed by more than one session, one of them by eight sessions, each marked made
  in its checkout.
- `docs/experiments/E10-doing-triggers.md`: `UserPromptSubmit` fires on a task-notification wake-up turn too, so
  a session that wakes for its agent's report starts a new turn there.
- `internal/harness/hook/stop_test.go`: `TestStopNoteAskIsForACommitOfTheTurn` and
  `TestPromptNoteAskIsForACommitSinceThePreviousPrompt`. On the earlier gate the first failed for each of the
  three ways above and the second for a commit made before the session's first prompt.
- On the binaries, the same day, with sessions of one member in one throwaway checkout on the fs adapter and the
  real `brigade hook` subcommands: the released 0.55.0 said "this turn made a commit" to a session whose turn
  made none after another session's commit, after an earlier turn's and after a pull; the build with this
  decision asked nothing in those three.

## Consequences

- A session alone in its checkout is asked after a turn that committed, as before. It is no longer asked later
  for a commit whose own turn's ask was held back, so a run of commits a few minutes apart gets fewer asks than
  it did.
- In a shared checkout a session is still asked when another session commits during its turn; the sentence then
  tells it that nothing is needed. Only a signal from the committing process would end that, and none is cheap.
- A commit that a command or an agent of the session makes while the session waits is not asked about, and
  neither is one an agent made in a worktree that the session then fast-forwarded to.
- On a host with no Stop hook the first of the three ways remains for a commit another session makes while this
  one waits: it is asked about at the next prompt, in words that are true of it. The first prompt after a
  session is resumed judges the time since the last prompt of its earlier run, as the other two gates of the ask
  already do there.
- The entry's commits and their `made` flag are unchanged, so the digest still credits every session of a shared
  checkout with a commit made there. That is the same missing fact and is not settled here.
- Revisit if a harness gives a hook the commits its own session made, or if the digests show commits without
  notes.

## References

- Work item 319, in whose change this lands.
- `internal/harness/hook/stop.go` (`noteDue`, `stopAskNote`, `promptAskNote`) and `internal/harness/gitfacts`
  (`Made`, the reflog reading behind the entry's `made` flag).
- Card 162 and `.context/plans/manager-digest.md`, section 5.3, the gate's first design; card 172, the reflog
  reading.
