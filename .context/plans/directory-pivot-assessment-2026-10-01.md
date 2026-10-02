# Can Brigade ship without a download, and is it worth it? (2026-10-01, for card 46)

Written overnight for Rjae after the Claude plugin directory reviewer declined Brigade v0.17.0:

> The plugin downloads a program or install script at setup or first run and executes it (for example a release
> binary, or a script piped into a shell). Plugins in the directory may only run code that is in the reviewed
> repository, or a package pinned to an exact version. Ship the program in the repository or package it as an
> MCPB bundle, then resubmit.

## What the rule actually touches

One file: `plugin/bin/brigade`, the 11 KB POSIX-sh bootstrap that downloads the pinned, sha256-checked release
binary (8.3 to 9.2 MiB per platform) on first use. Everything else in the plugin — the three hooks, the five
skills, the README, the icon — passed the scan with holds that a reviewer can clear. The Go program itself is
47,000 lines outside tests and 68,000 in tests, across the harness, the adapters, the protocol, sync and the
proofs.

## The routes the rule leaves

| Route | What it takes | What it costs Brigade | Verdict |
| --- | --- | --- | --- |
| **1. Pinned npm launcher** — publish `@appshapes/brigade` to npm carrying the four binaries; the hooks run `npx -y @appshapes/brigade@<version> hook …` directly | Measured tonight on a throwaway branch: the directory's validator **passes** it, with the hold "Runs a pinned npx or uvx package" ("the pin fixes the package but not its dependencies, so a reviewer looks at the package"). Work: an npm package (esbuild-style, one platform binary per optional dependency or one package with a shim that picks the platform file), `hooks.json` pinned to the version and rewritten by `make release`, npm publish in the release workflow with a token, docs and the `brigade` command on the Bash tool's PATH (`npm i -g`, or a readable shim). Roughly 2 to 4 days plus a review cycle | Node and npm become a requirement on every member's machine (Claude Code's native install does not need them). `npx` adds latency to every prompt (the `UserPromptSubmit` hook; its timeout is 5 s) and a first-use package download that is a download by another name. Every release is held for a reviewer, so a release cadence of several a day becomes one per review | The only proportionate route **if** the listing is wanted soon |
| **2. Commit the binaries** into `plugin/bin` | A platform picker instead of a downloader; 35 MiB added to the repository per release | Each file over 256 KiB is held and compiled code the scan cannot read is held ("commit readable source instead"); the repository must stay under 50 MiB as GitHub archives it, which fails within two releases; every plugin update pulls all four platforms | Not viable |
| **3. Rewrite in "readable source"** (Node or Python, no dependencies, vendored in the plugin, under 256 KiB a file and 512 files) | A new harness: REST polling instead of the websocket, inbox-socket delivery, the watcher, team store, doing lines, notifications, sync, plus the proofs and the security measurements that docs/security.md rests on | Months. A non-shell hook is still held for review (Telder's Python hook was). Python or Node presence is not guaranteed where Go's static binary needed nothing | Not now; this is a different product's year |
| **4. MCPB bundle** | Package an MCP server; Brigade has none by design and CI enforces it | The hooks would still need a local executable, so it removes nothing | Does not fit |

Withdrawing the rejected submission gains nothing; it can be resubmitted from its Review tab after a change.

## Is it worth it, marketing-wise?

**What the listing would give.** A row in claude.ai's directory that people browse and search, one-click add that
syncs into Claude Code as `brigade@synced`, install and search analytics, and the word "listed". Brigade would be
listed for Claude Code only (its `bin/` keeps it off Cowork and the apps).

**What it would not give.** The marketplace people see inside `/plugin` is `claude-plugins-official`, which takes
no submissions. Claude Code developers find plugins today through GitHub, the awesome lists, Hacker News, Reddit,
creators and marketplaces they add by hand; those are the channels already drafted in `launch-card-41.md`, and the
two that are live (the video, the reply on claude-code#87954) do not depend on the directory.

**What route 1 would cost the product.** A Node requirement, a slower prompt hook, releases gated by a reviewer,
and 2 to 4 days of work in the week the launch posts are waiting. Brigade's pitch includes "nothing else is
built, compiled or installed" and a checksum-verified binary; a launcher that resolves a package at first use is
not a better security story, only a sanctioned one.

**Assessment.** Not worth pivoting the stack for the listing now. Keep the marketplace install
(`/plugin marketplace add appshapes/brigade`), run the Show HN, Reddit and creator steps, and revisit the directory
in a few months: the policy was written for MCP connectors and skills, compiled plugins are a known gap, and a
hosted Brigade (if one is ever offered) would make a thin listed plugin natural. If the listing matters sooner than
that, route 1 is the one to take, and tonight's probe says it will validate.

## Done tonight

- Reviewer's decision recorded on card 46 and in `launch-card-41.md`; Rjae notified by push.
- Probe branch `probe/directory-npx` (hooks through `npx -y @appshapes/brigade@0.17.0`) validated on a fresh
  form without saving a draft, then deleted locally and on GitHub. Findings: passed, 4 warnings, 6 holds, the new
  one being `LAUNCHER_PACKAGE_REVIEW` on all three hooks.
- The half-hourly monitor was stopped: nothing on the submission changes until we change the plugin.
