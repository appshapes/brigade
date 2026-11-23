# 0025. Hold a skill's description to the length a loader accepts

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

CI does not police what a skill declares (the owner's ruling of 2026-09-08), with one exception until now: no
skill takes the model's reach away (`disable-model-invocation`, card 28). A skill's `description` has a length a
host may refuse. The Agent Skills format says 1 to 1,024 characters, and Codex CLI refused a skill over that
through release 0.141.0. Nothing measured it. Work item 300's first draft took `work-items` to 1,058 characters
and only an agent review caught it. It landed at 997. On 2026-11-16 work item 353 added a clause, and from then
the description on master was 1,085 characters in three packages and 1,076 in the fourth, with no check to say
so; Codex CLI and Grok Build read their package from the default branch.

The owner ruled on 2026-11-13 (decision d-20261113-720cd1, work item 306): a check, as a second deliberate
exception.

## Decision

A skill's `description` is at most 1,024 characters, and each of the four package checks fails on a skill that is
over, naming the file and the length.

- The count is of characters of the value a YAML parser gives for the key, so a folded scalar counts as a loader
  reads it and not as its raw lines.
- 1,024 is the smallest limit a host enforces: the format's own, which Codex CLI enforced.
- The limit is held on the copies, because a copy is what a host loads, and by the generator on each rendering
  of a source, because the source is where a person puts it right: `make skills-generate` writes no copy a
  package check would refuse.
- It is a fact about whether a host loads the file, the same kind as a package's file allow-list. What a
  description says stays nobody's to check, and so does a skill that has none: the format's lower bound of one
  character is not held here, because the ruling is about a description too long to load.

## Alternatives considered

- **No check, and care.** Lost: care failed twice in a week, on the one skill nearest the limit, and the second
  time nothing caught it.
- **The largest limit a current host enforces** (none found that refuses: Codex CLI from 0.142.0 checks the
  name alone, and Claude Code's listing cuts at 1,536). Lost: a member's Codex CLI may be older than 0.142.0,
  and the format's own limit is what the next host is most likely to enforce.
- **Hold it on the sources only.** Lost: a mark makes a description as long as the harness's own invocation
  form, so one source has four lengths, and the ruling names the package checks.
- **Read the raw lines of the frontmatter.** Lost: a folded scalar's raw text is longer than its value by its
  indentation, and a check that refused a description at the limit would teach people to turn it off.

## Evidence

- Agent Skills specification, read 2026-11-23: "Must be 1-1024 characters."
- Codex CLI's source, read 2026-11-23 at its release tags: `MAX_DESCRIPTION_LEN` is 1024 and
  `validate_len(&description, …)` refuses the skill ("exceeds maximum length of 1024 characters", counted with
  `chars().count()` after whitespace is collapsed) in `codex-rs/core-skills/src/loader.rs` through
  `rust-v0.141.0`; the call is gone from `rust-v0.142.0` on, and in `codex-rs/skills/src/parser.rs` of 0.166.0,
  the release on the machine this was written on.
- Claude Code's documentation, read 2026-11-23: the listing cuts `description` and `when_to_use` together at
  1,536 characters; no refusal is documented. Not measured.
- Muse Code's documentation, read 2026-11-23: no limit on a description; a `SKILL.md` of at most 262,144 bytes
  and a budget for the whole catalog. Not measured.
- Grok Build: no page on skills found; the 1.0.92 binary's strings carry no message about a description's
  length. Not measured: a measurement needs a session of the host, which waits on the owner's word.
- The check on master's own tree, 2026-11-23: it failed on `work-items` (1,085 characters), which was shortened
  to 1,019 in its source by removing a parenthesis its body already says and one adjective.
- `scripts/ci/skill_description_test.go`: each of the four package checks passes at 1,024 and fails at 1,025;
  `internal/packagecheck/description_test.go`: what is counted.

## Consequences

- The next clause added to a long description fails `make skills-generate` and the package checks, before it
  ships; whoever adds it shortens something.
- The package check reads YAML with `github.com/pb33f/go-yaml`, a module the schema generator already brought
  in. It is not linked into the shipped binary.
- If a host turns out to enforce less than 1,024, the constant moves and its comment says which host and where.
  What Muse Code and Grok Build do with a longer description is still unmeasured.
- Frontmatter that is not YAML, or that is opened and never closed, fails the check too, because nothing can be
  counted.

## References

- Work item 306 on the Brigade Board; decision d-20261113-720cd1; work item 300, where it was first seen.
- `internal/packagecheck/description.go` (`DescriptionLimit`, with each source), `internal/skillgen/skillgen.go`.
- `docs/agent/repository/invariants.md`, "Every Brigade skill is reachable by the model"; `gates.md`, "Package
  checks".
- https://agentskills.io/specification; https://github.com/openai/codex at the tags named above.
