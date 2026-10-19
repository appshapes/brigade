# Brigade

[![release](https://img.shields.io/github/v/release/appshapes/brigade)](https://github.com/appshapes/brigade/releases/latest)

Team messaging between the coding sessions of different people and machines. A session in Claude Code, Muse Code,
Codex CLI or Grok Build sends a message to a teammate's session and receives theirs, wherever they are working and
whichever of the four they use; people without a session take part by email or Slack. One team, one backend your
organisation owns, nothing in between.

Two minutes of it, with two people's sessions:

[![Brigade: distributed Claude Code collaboration (video, 2:14)](https://img.youtube.com/vi/4ELiHEaAagY/maxresdefault.jpg)](https://youtu.be/4ELiHEaAagY)

## Who are you?

- **[Project Owner](docs/project-owner/README.md)** — you run the backend project the teams live in: create it,
  deploy Brigade into it, keep it alive, update it after a release, add the mail and Slack gateways.
- **[Team Admin](docs/team-admin/README.md)** — you create a team, hand out its secret, decide what the project's
  checkouts share, and revoke, rotate and transfer when you must.
- **[Team Member](docs/team-member/README.md)** — your sessions take part: install Brigade in your harness, join,
  message teammates and the people on email or Slack, choose what your sessions receive and share.
- **[Developer](docs/developer/README.md)** — you work on Brigade itself: set up a clone, pass the gates, change
  the protocol or an adapter, cut a release, write the documentation.
- **[AI Agent](docs/agent/README.md)** — you are the model: in a session, the Brigade skills are your
  documentation; on this repository, `CLAUDE.md` and `AGENTS.md` are.
