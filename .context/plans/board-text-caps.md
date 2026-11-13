# The Brigade Board's text caps (work item 304)

Work item 304, 2026-11-13. The owner asked why a description stops at 8,000 characters, and whether the cap could be
much larger; then for this work item, a review of this plan by a second session before it is built, and an
assessment by that session of storing an edit as a diff rather than the whole text.

## 1. Why there is a cap, and where 8,000 came from

Any member's machine can write any file into the board's folder (`docs/security.md` section 19), and every
`brigade work-item` verb reads the whole board (`workitem.Read`). The readers bound what they take: a change file
of at most `MaxChangeBytes`, and every string in it truncated to its cap (`Change.sanitize`). The caps came in with
the board's first cut (f792df8, work item 263, 2026-11-11). The plan lists them without an argument, and the only
reason in the code is that a description at its cap fits in the file cap: 8,000 code points at 12 bytes each (a
code point outside the BMP written as an escaped surrogate pair, `\uD83D\uDE00`, which any JSON encoder may do and
a reader has to accept) is 96,000 bytes, under 128 KiB. Trello holds 16,384 in a description and in a comment, so
the carry-over had to split long Trello descriptions into comments; 87 of Cavalry's 114 ADRs and 8 of muster's 18
are longer than 8,000 (work item 302).

## 2. The change

| Constant (`internal/harness/workitem/workitem.go`) | Now | After |
| --- | --- | --- |
| `MaxDescriptionChars` | 8,000 | 65,536 |
| `MaxCommentChars` | 4,000 | 16,384 |
| `MaxChangeBytes` | 128 KiB | 1 MiB |
| `MaxTitleChars` | 200 | 200 |

- **The file cap.** The largest change a writer can make sets a title, a description and the tags and adds a
  comment, all at their caps: (200 + 65,536 + 16,384 + 12 x 48) code points at 12 bytes is 992,352 bytes, and with
  16 KiB for the change's other members 1,008,736, under 1,048,576 (the review's finding 4). The writer's refusal
  (`store.go`, "larger than a change can be") keeps the constant.
- **Stdin.** `workItemReadLimit` is derived from the caps already (`4*(MaxTitleChars+MaxDescriptionChars)+16`,
  262,960 bytes after); nothing to change.
- **The carry-over** (`internal/boardcarry`) splits by the constants; its test of a description longer than one
  holds built the text at 11,250 characters, which no longer overflows: it is built from `MaxDescriptionChars`.
- **Tests.** The command's cap tests use the constants. Added: a description and a comment at the new caps written
  through the store and read back whole (`TestTextsAtTheirCapsReadBackWhole`), and a change with a title, a
  description, a comment, the tags, the labels and the session's name all at their caps, every code point escaped
  as a surrogate pair, taken whole by the reader (`TestAChangeAtEveryCapFitsTheFileCap`). A constant expression
  (`changeFits`) stops the package compiling if `MaxChangeBytes` falls under 12 bytes a code point of the caps plus
  16 KiB for the other members: (200 + 65,536 + 16,384 + 12 x 48) x 12 + 16,384 = 1,008,736.
- **Not on the wire.** The board never reaches a backend, so protocol v1, its schema, the conformance suite and the
  REST contract do not change (`internal/protocol.MaxDescriptionChars` is a session's roster line, 256, unrelated).

## 3. What a member on an older release sees

The board exists from 0.50.0. A reader of 0.50.0 to 0.53.1 does two things with a text past the old caps (the
review's findings 1 and 2, measured on HEAD's build, whose reader is 0.53.1's):

- **It cuts it, and an `edit` there writes the cut text back for everyone.** The fold truncates a description to
  8,000 code points and a comment to 4,000, the `...` marker inside the cap. `edit` compares what it is sent with
  that cut text and writes what differs: a title changed and the description passed back as shown writes the
  title alone (safe); a description made longer is refused `too_long`, which steers the session to shorten it; a
  description shortened or kept at the same length is written, cut, as the last change to set the field, so every
  machine, updated ones too, shows the cut text from then on. The whole text stays in the earlier change file, and
  no verb shows it.
- **It skips a change file over 128 KiB.** The writer spends 1 byte on an ASCII letter, 2 on a newline, a quote or
  a Cyrillic letter, 3 on CJK, 4 on an emoji, so an English description at the new cap is a file of about 66-70 KB,
  cut and never skipped; only a text mostly in wider characters passes 128 KiB (from about 65,000 Cyrillic, 43,600
  CJK or 32,700 emoji characters). When the skipped change is a work item's first, that machine shows no work item,
  its `NextNumber` can hand the number out again, and `Repair` then renumbers one of the two. The skip is logged at
  debug level only.

So the rule is: **update every machine before anyone writes a text past the old caps**, and the changelog entry
says so. No gate and no format change: marking a long change as another version would make an old reader skip it,
which is worse than cutting it.

The headroom is the owner's to judge: Cavalry's longest ADR is 64,833 characters, 703 under the new cap before
sanitising and before any origin line. 131,072 would need a 2 MiB file cap. 65,536 is kept, as the owner agreed.

## 4. The cost of a larger cap

- **Honest boards.** This machine's appshapes board on 2026-11-13: 65 work items, 231 changes, 390 KB; 70
  description writes, at most 4 on one item, 5 items written more than once. Full copies on every edit cost little
  at these rates; an ADR-sized description (Cavalry's median 11,163 characters) edited ten times is about 110 KB.
- **A forged folder.** A reader folds at most `MaxChanges` (2,000) changes per item, each at most `MaxChangeBytes`.
  Raw bytes read for one item: 250 MiB before, about 2 GiB after. A change can set a description and add a comment
  at once, so the decoded text one item holds is 2,000 x (65,536 + 16,384 + 200) x 4 bytes, about 627 MiB after and
  93 MiB before (the review's finding 3). That is not the folder's worst case and never was: `Read` folds every item
  and each keeps its comments, so the bound is `MaxItems` times it, 5 TB of files before and 40 TB after; the folder
  is bounded in the end by the disk and by what sync delivers. **Decided with the reviewer: accept it**, and the
  comment on `MaxItems` and `MaxChanges` now says the true bound. A per-item byte budget protects nothing (20,000 items of 64 MiB is still
  1.2 TiB) and breaks the reader's one clean rule, that a file is skipped only for its own name, size or shape.
- **The model's context.** `show` prints the whole description: 65,536 characters is roughly 16,000 tokens. In
  Claude Code a Bash result past about 30,000 characters reaches the session as a short preview and a saved file,
  and `edit` replaces the whole description with what it is sent, so a session that edits from the preview cuts the
  text for everyone (the review's finding 8). The four `work-item` skills now say to read a cut or saved `show`
  whole before an edit. The other harnesses' output limits are unmeasured.

## 5. Documentation and skills

- `docs/team-member/use/work-items.md`: the caps sentence.
- `plugin/skills/work-item/SKILL.md`, `muse/brigade/skills/work-item/SKILL.md`, `codex/brigade/skills/work-item/SKILL.md`,
  `grok/brigade/skills/work-item/SKILL.md`: the `too_long` row, and the `edit` row's sentence on reading a long
  description whole first. `make muse-digest` after the Muse edit.
- `.claude/skills/work-item-start/SKILL.md`: a landing comment holds 16,384.
- `.context/plans/work-items.md` section 4: the caps line, naming this work item.
- `CHANGELOG.md` `[Unreleased]`, `### Changed`; a row in `.context/plans/brigade-execution-log.md`.

## 6. Proof

The acceptance gate, `make plugin-check muse-plugin-check codex-plugin-check grok-plugin-check`; then the built
`bin/brigade` against a scratch board folder (never the team's): `add`, `edit` and `comment` at the cap taken and
shown whole, one past it refused `too_long`.

## 7. A diff per edit instead of the whole text

Assessed by the reviewer session `304-reviewer` on 2026-11-13, agreed by this session: **no meaningful need;
discarded**, and no work item.

- **The need, measured on the appshapes board** (66 items, 234 changes, 392,468 bytes; a listing takes 10-20 ms).
  Description text over all 71 writes is 163,394 bytes, the winning copies 126,041, so the superseded copies are
  37,353 bytes, 9.5 % of the folder; a line diff of each rewrite would carry about 26,683 bytes, a saving of about
  10,670 bytes, 2.7 % of the folder. Comments are 46 % of the folder and only ever added: nothing to diff.
- **With the new caps** (through the real reader, a scratch test): 200 items rewritten whole ten times at 11,163
  characters is 23 MB and a whole-board read of 207 ms; 114 items at 65,536 rewritten ten times, 76 MB and 573 ms;
  1,000 items, 115 MB and 1.12 s. A read costs about 40 microseconds a file plus 5-7 ms a megabyte, and diffs do not
  reduce the number of files. ADRs are written once: 132 of them at the median add about 1.5 MB and 15 ms.
- **The cost.** Every earlier change becomes load-bearing: one late, skipped, oversized, overwritten or forged file
  (any member's machine can overwrite or delete one, `docs/security.md` section 19) leaves the text wrong or
  unreadable, which needs base hashes, a rule for a missing base and whole snapshots anyway. Two machines editing at
  once diff against one base, so the fold stops being "the last change wins" and becomes a walk of a graph. It is a
  format break that every reader of 0.50.0 on, and of this release, would read as a stale text without knowing it.
  The binary has no diff or patch code, and a teammate's patch is untrusted input that expands. Git does not do it
  at this layer either: its loose objects are whole snapshots, and deltas exist only in packs that a local repack
  rewrites; the board is never rewritten and has no single owner.
- **Look again** when a listing on a real board passes about 250 ms, or superseded description text passes half the
  folder. The fix then is a per-machine cache of the fold outside the synced folder: no format change, and it helps
  every kind of change.
- **Next to it, for the owner, no work item on today's evidence:** `edit` sends the whole text, so changing one
  line of a 65,536-character description costs about 16,000 tokens and depends on having read it whole. A partial
  edit that still stores the whole text would answer that, once long descriptions start being edited; no
  description on the board is over 7,975 characters today.
