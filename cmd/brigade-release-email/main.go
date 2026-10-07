// Command brigade-release-email renders the release-notes email (card 43, card 73): the Markdown an agent
// drafted and the gate approved, laid out as the HTML an inbox shows, and in -check mode the lint's reading of
// a draft, every finding on its own stderr line. `scripts/ci/send-release-notes.sh finish` and
// `scripts/ci/release-notes-lint.sh` run it; send-release-notes.yml builds it beside the CLI it lints with.
//
// The command is DEV-ONLY and never shipped. It writes nothing to stdout.
package main

import (
	"os"

	"github.com/appshapes/brigade/internal/releaseemail"
)

func main() { os.Exit(releaseemail.Run(os.Args[1:], os.Stderr)) }
