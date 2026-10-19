package cli

import (
	"flag"
	"slices"
)

// This file is the command table as the documentation sees it (card 112):
// docs/developer/reference/commands.md is rendered from Commands, UsageAll,
// UsageCommand and OwnFlags by `make docs-generate`, and `make docs-check`
// fails when the page is stale or a command is named on no page. Nothing
// here changes what the binary prints.

// Commands returns the command table in the order `brigade help --all`
// lists it, hidden entries included.
func Commands() []Command {
	return slices.Clone(commands)
}

// UsageAll is the text `brigade help` prints, or `brigade help --all` when
// all is true.
func UsageAll(all bool) string { return usageAll(all) }

// UsageCommand is the text `brigade help <name>` prints for c.
func UsageCommand(c Command) string { return usageCommand(c) }

// A Flag is one of a command's own flags as the command registers it. The
// global flags (--json, --log-level) are never among them.
type Flag struct {
	Name    string // without the leading dashes
	Default string // the flag package's rendering of the default value
	Usage   string // the one-line description the command registered
	Bool    bool   // true for a flag that takes no value
}

// OwnFlags returns the flags c registers, in name order (the order
// flag.FlagSet visits them). A Raw command registers none: its grammar is
// its own.
func (c Command) OwnFlags() []Flag {
	if c.Flags == nil {
		return nil
	}
	fs := newFlagSet(c.Name)
	c.Flags(fs)
	var out []Flag
	fs.VisitAll(func(f *flag.Flag) {
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		out = append(out, Flag{Name: f.Name, Default: f.DefValue, Usage: f.Usage, Bool: ok && b.IsBoolFlag()})
	})
	return out
}
