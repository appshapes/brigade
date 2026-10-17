# Brigade, for an agent that reads no imports

`CLAUDE.md` is this repository's agent root and loads its rule files through `@` imports. A harness that reads
`AGENTS.md` instead reads the same files here, in the same order, before changing anything:

1. [Conventions: git, commits, cards, plans](docs/agent/repository/conventions.md)
2. [Invariants: protocol, output, secrets, configuration, dependencies](docs/agent/repository/invariants.md)
3. [Gates: what runs before a push, and what a release needs](docs/agent/repository/gates.md)
4. [Documentation: where a page goes, and the gate that holds it](docs/agent/repository/documentation.md)
5. [Automation: the release-notes email, the agentic loop, pinned actions](docs/agent/repository/automation.md)

The agent persona's page, [docs/agent/README.md](docs/agent/README.md), says what an agent does inside a Brigade
team and what it does on this repository. Humans start at [README.md](README.md).
