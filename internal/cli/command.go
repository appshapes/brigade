package cli

import "flag"

// A Command is one entry of the `brigade` command table (6.4).
type Command struct {
	// Name is the first argv word that selects the command.
	Name string
	// Args is the argv summary shown after the name in help, without the
	// global flags.
	Args string
	// Summary is the one-line description shown by `brigade help`.
	Summary string
	// Hidden keeps the command out of `brigade help` but not out of
	// `brigade help --all` or `brigade help <name>` (6.4).
	Hidden bool
	// MultiCall marks the entrypoints internal/app intercepts before the
	// human command table: `hook`, `watch` and `adapter`.
	MultiCall bool
	// Task names the plan task that implements the command. It is set only
	// while the command is still a placeholder, and is what the
	// not-implemented message points the caller at.
	Task string
	// Flags registers the command's own flags. The global flags are added
	// separately, so a command must not register --json or --log-level.
	Flags func(fs *flag.FlagSet)
	// Run executes the command. It is nil exactly when Task is set.
	Run func(cx *Context, args []string) error
}

// Implemented reports whether the command does real work yet.
func (c Command) Implemented() bool { return c.Run != nil }

// commands is the P1-1 command table. Every entry of 6.4 is listed so that
// `brigade help` shows the real surface from the first commit; the entries
// that carry a Task are placeholders that fail cleanly with the task that
// will build them.
//
// It is filled in by init rather than by a composite literal because
// runHelp reads the table through Lookup, and a literal would be a static
// initialisation cycle.
var commands []Command

func init() {
	commands = []Command{
		{
			Name:    "version",
			Summary: "print the version of this binary",
			Run:     runVersion,
		},
		{
			Name:    "help",
			Args:    "[command] [--all]",
			Summary: "print usage for the binary or for one command",
			Flags:   func(fs *flag.FlagSet) { fs.Bool("all", false, "include hidden commands") },
			Run:     runHelp,
		},
		{
			Name:    "sessions",
			Args:    "[--all]",
			Summary: "list the team's sessions",
			Task:    "P2-9",
		},
		{
			Name:    "send",
			Args:    "<session_id> [--summary <text>] [--reply-to <message_id>] [--body-file <path>]",
			Summary: "send a message to another session (body on stdin)",
			Task:    "P2-9",
		},
		{
			Name:    "whoami",
			Summary: "print this session's identity",
			Task:    "P2-9",
		},
		{
			Name:    "team",
			Args:    "create|join|leave|members",
			Summary: "create, join or leave a team, and list its members",
			Task:    "P2-8",
		},
		{
			Name:    "profile",
			Args:    "init|status|reset|revoke-credentials",
			Summary: "manage the local profile and its credentials",
			Task:    "P2-8",
		},
		{
			Name:    "inbox",
			Args:    "[release <message_id>]",
			Summary: "show and release held messages",
			Task:    "P5-11",
		},
		{
			Name:      "hook",
			Args:      "session-start|prompt|session-end",
			Summary:   "Claude Code lifecycle hook entrypoint",
			Hidden:    true,
			MultiCall: true,
			Task:      "P3-4",
		},
		{
			Name:      "watch",
			Args:      "[--sink <file>]",
			Summary:   "detached inbound watcher for one session",
			Hidden:    true,
			MultiCall: true,
			Task:      "P3-5",
		},
		{
			Name:      "adapter",
			Args:      "<name> <group> <verb> [flags]",
			Summary:   "run a bundled adapter over the BAP/1 protocol on stdio",
			Hidden:    true,
			MultiCall: true,
			Run:       runAdapterEntry,
		},
	}
}

// Lookup returns the table entry for name.
func Lookup(name string) (Command, bool) {
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// LookupMultiCall returns the table entry for name when internal/app is the
// dispatcher for it rather than the human command table.
func LookupMultiCall(name string) (Command, bool) {
	c, ok := Lookup(name)
	if !ok || !c.MultiCall {
		return Command{}, false
	}
	return c, true
}

// runAdapterEntry is the table's Run for the `adapter` entry, which
// internal/app intercepts BEFORE the table and hands to the bundled
// adapter's own dispatcher (P2-6): the adapter speaks BAP/1 on stdio and
// none of the human table's flags or envelopes apply to it. Dispatch
// therefore never reaches this function; it exists so the entry is
// Implemented and `help --all` no longer lists it as a placeholder, and
// it answers honestly if a future caller routes around internal/app.
func runAdapterEntry(*Context, []string) error {
	return usagef("adapter", "the adapter entrypoint is dispatched by the multi-call seam; run `"+Program+" adapter supabase <group> <verb>`")
}
