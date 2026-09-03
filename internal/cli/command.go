package cli

import (
	"flag"

	harnesscmd "github.com/appshapes/brigade/internal/harness/commands"
)

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
	// Raw hands Run everything after the command word verbatim instead of
	// parsing it: the terminal pass-through commands (`team`, `profile`)
	// forward adapter flags the harness has never heard of, so their
	// grammar is their own (6.4). The global flags BEFORE the command
	// word still apply, --json is honoured wherever it appears, and the
	// poison scan runs first as for every command.
	Raw bool
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
			Args:    "[--all] [--profile <p>]",
			Summary: "list the team's sessions",
			Flags: func(fs *flag.FlagSet) {
				fs.Bool("all", false, "include offline sessions")
				fs.String("profile", "", "the profile to act on (terminal only; a session's profile comes from its map)")
			},
			Run: runSessions,
		},
		{
			Name:    "send",
			Args:    "<session_id> [--summary <text>] [--reply-to <message_id>] [--body-file <path>]",
			Summary: "send a message to another session (body on stdin)",
			Flags: func(fs *flag.FlagSet) {
				fs.String("summary", "", "a one-line summary shown before the body (at most 200 characters)")
				fs.String("reply-to", "", "the message id this message answers")
				fs.String("body-file", "", "read the body from this file instead of stdin")
			},
			Run: runSend,
		},
		{
			Name:    "whoami",
			Summary: "print this session's identity",
			Run:     runWhoami,
		},
		{
			Name:    "team",
			Args:    "create|join|leave|members [--profile <p>] [adapter flags]",
			Summary: "create, join or leave a team, and list its members",
			Raw:     true,
			Run:     runTeam,
		},
		{
			Name:    "profile",
			Args:    "init|status|reset|revoke-credentials [--profile <p>] [--adapter <name-or-command>] [adapter flags]",
			Summary: "manage the local profile and its credentials",
			Raw:     true,
			Run:     runProfile,
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
			Run:       runHookEntry,
		},
		{
			Name:      "watch",
			Args:      "[--sink <file>]",
			Summary:   "detached inbound watcher for one session",
			Hidden:    true,
			MultiCall: true,
			Run:       runWatchEntry,
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

// runHookEntry and runWatchEntry are the table's Run for the `hook` and
// `watch` entries, which internal/app intercepts BEFORE the table and
// hands to internal/harness/hook and internal/harness/watch with the real
// process streams and their production dependencies (P3-4, P3-5): a hook
// exits 0 on every failure and the watcher writes nothing to stdout, so
// neither can be run through the table's flag parser and error reporter.
// Dispatch therefore never reaches these functions; they exist so the two
// entries are Implemented and `help --all` no longer lists them as
// placeholders, and they answer honestly if a future caller routes around
// internal/app. The message is fixed text: argv is never echoed (4.5.14).
func runHookEntry(*Context, []string) error {
	return usagef("hook", "the hook entrypoint is dispatched by the multi-call seam; run `"+Program+" hook session-start|prompt|session-end`")
}

func runWatchEntry(*Context, []string) error {
	return usagef("watch", "the watcher entrypoint is dispatched by the multi-call seam; run `"+Program+" watch [--sink <file>]`")
}

// invocation builds the harnesscmd.Invocation of one table entry from the
// dispatcher's context: the streams, the environment and the two global
// flags cross as they are; nothing of the flag parser does.
func invocation(cx *Context, args []string) harnesscmd.Invocation {
	return harnesscmd.Invocation{
		Args:     args,
		Environ:  cx.Environ,
		In:       cx.In,
		Out:      cx.Out,
		Err:      cx.Err,
		JSON:     cx.JSON,
		LogLevel: cx.LogLevel,
	}
}

// stringFlag reads a string flag the entry registered.
func stringFlag(cx *Context, name string) string {
	if cx.Flags == nil {
		return ""
	}
	f := cx.Flags.Lookup(name)
	if f == nil {
		return ""
	}
	return f.Value.String()
}

// runSessions is the table's Run for `sessions`.
func runSessions(cx *Context, args []string) error {
	return harnesscmd.Sessions(invocation(cx, args), harnesscmd.SessionsOptions{
		All:     cx.Bool("all"),
		Profile: stringFlag(cx, "profile"),
	})
}

// runSend is the table's Run for `send`.
func runSend(cx *Context, args []string) error {
	return harnesscmd.Send(invocation(cx, args), harnesscmd.SendOptions{
		Summary:  stringFlag(cx, "summary"),
		ReplyTo:  stringFlag(cx, "reply-to"),
		BodyFile: stringFlag(cx, "body-file"),
	})
}

// runWhoami is the table's Run for `whoami`.
func runWhoami(cx *Context, args []string) error {
	return harnesscmd.Whoami(invocation(cx, args))
}

// runTeam is the table's Run for `team` (raw: the verb and the adapter
// flags are parsed by the command).
func runTeam(cx *Context, args []string) error {
	return harnesscmd.Team(invocation(cx, args))
}

// runProfile is the table's Run for `profile` (raw).
func runProfile(cx *Context, args []string) error {
	return harnesscmd.Profile(invocation(cx, args))
}
