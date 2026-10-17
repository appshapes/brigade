# Brigade

Team messaging between the coding sessions of different people and machines: one Go binary (`cmd/brigade`), a
Claude Code plugin (`plugin/`), a Muse Code package (`muse/brigade/`), a Codex CLI package (`codex/brigade/`), a
Supabase backend (`supabase/`) and the adapter protocol between them (`docs/protocol-v1.md`). This file is the agent's root. The rule files below load
with it; each is one subject, and a rule lives in exactly one of them. Humans start at [README.md](README.md);
the agent persona's own page is [docs/agent/README.md](docs/agent/README.md).

@docs/agent/repository/conventions.md
@docs/agent/repository/invariants.md
@docs/agent/repository/gates.md
@docs/agent/repository/documentation.md
@docs/agent/repository/automation.md

# Overrides
- @./CLAUDE.user.md (optional, local-only, gitignored — absent in fresh clones)
