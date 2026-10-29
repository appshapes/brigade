package cli

import (
	"flag"
	"io"

	harnesscmd "github.com/appshapes/brigade/internal/harness/commands"
	"github.com/appshapes/brigade/internal/syncadapters/syncthing"
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
	// not-implemented message points the caller at. No entry carries one
	// since P5-9 filled `inbox`; the mechanism stays for the next one.
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
// `brigade help` shows the real surface from the first commit; an entry
// that carries a Task is a placeholder that fails cleanly with the task
// that will build it (none is left since P5-9).
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
			Args:    "[--all] [--here] [--member <who>] [--team <ref-or-name>]",
			Summary: "list the team's sessions",
			Flags: func(fs *flag.FlagSet) {
				fs.Bool("all", false, "include offline sessions")
				fs.Bool("here", false, "only the sessions of this repository")
				fs.String("member", "", "only the sessions of one member: their label, or the first 8 or more characters of their principal reference")
				fs.String("team", "", "the team to act on, by ref or name (terminal only; a session's team comes from its map)")
			},
			Run: runSessions,
		},
		{
			Name:    "send",
			Args:    "<session_id> | --member <who> [--summary <text>] [--reply-to <message_id>] [--body-file <path>]",
			Summary: "send a message to another session, by its id or its SESSION characters, or to a member's best session (body on stdin)",
			Flags: func(fs *flag.FlagSet) {
				fs.String("member", "", "address a member's best session instead of one session: their label as the MEMBER column shows it, or the first 8 or more characters of their principal reference")
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
			Name:    "doing",
			Args:    "[--clear]",
			Summary: "publish one sentence about what this session is working on (on stdin)",
			Flags:   func(fs *flag.FlagSet) { fs.Bool("clear", false, "remove the sentence; nothing is read from stdin") },
			Run:     runDoing,
		},
		{
			Name:    "note",
			Summary: "record one line about the work for the manager digest — Done:, Next: or Blocked: — in this session's state entry (on stdin)",
			Run:     runNote,
		},
		{
			Name:    "decision",
			Args:    "--to <label> [--options <a | b>] [--default <option>] [--by <YYYY-MM-DD>] [--card <n>] | --close <id>",
			Summary: "ask a teammate for a decision (the question on stdin), sent at once to their digest recipients or their session, or close one with its outcome",
			Flags: func(fs *flag.FlagSet) {
				fs.String("to", "", "the person the decision is for, by their label as the roster shows it (required)")
				fs.String("options", "", "the answers offered, separated by |")
				fs.String("default", "", "the option taken when the person says nothing (one of --options)")
				fs.String("by", "", "the date an answer is needed by, YYYY-MM-DD")
				fs.Int("card", 0, "the card the question belongs to")
				fs.String("close", "", "close the decision with this id; the outcome travels on stdin")
			},
			Run: runDecision,
		},
		{
			Name:    "report",
			Args:    "subscribe [--recipients <@name | #channel | address>] [--every <duration>] [--at <HH:MM>] [--tz <zone>] [--only mine] | unsubscribe | show [--to <label>] | send [--to <label>]",
			Summary: "subscribe this session's member to the digest (reports.json in the state folder) or unsubscribe, print the digest a subscriber would get now, or send one now",
			Flags: func(fs *flag.FlagSet) {
				fs.String("recipients", "", "where your digest and the decisions for you go, in order of precedence, separated by |: a Slack handle or channel, an email address (your label when left out and it is an address)")
				fs.String("every", "", "the period between two digests, a duration such as 24h or 4h (24h when unset)")
				fs.String("at", "", "a daily digest's time of day, HH:MM")
				fs.String("tz", "", "the zone of --at, an IANA name such as America/Los_Angeles (UTC when unset)")
				fs.String("only", "", "mine: list only the decisions addressed to you; the contributions stay the team's")
				fs.String("to", "", "the subscriber a show or a send is for, by their label (show: your own subscription when left out; send: every subscriber)")
			},
			Run: runReport,
		},
		{
			Name:    "inbound",
			Args:    "[accept|hold|refuse]",
			Summary: "show this session's inbound policy, or hold or refuse team messages for this session alone (accept returns to its own policy)",
			Run:     runInbound,
		},
		{
			Name:    "sync",
			Args:    "status",
			Summary: "show the folders this project syncs, the sync engine and the teammates' peers",
			Run:     runSync,
		},
		{
			Name:    "team",
			Args:    "create|join|leave|members|status|reset|revoke-credentials|list|rotate-secret|revoke-member|transfer [--team <ref-or-name>] [adapter flags]",
			Summary: "create, join, leave and administer a team, and list its members",
			Raw:     true,
			Run:     runTeam,
		},
		{
			Name:    "inbox",
			Args:    "[release] [--session <id>] [--all | <message_id>…] | follow [--incarnation <token>] | read [--cursor <token>]",
			Summary: "show and release held messages; follow prints each team message as it lands (a native inbox monitor's command)",
			Flags: func(fs *flag.FlagSet) {
				fs.String("session", "", "the Brigade session to list or release for (terminal only)")
				fs.String("cursor", "", "validated continuation from the previous Grok inbox read page")
				fs.String("incarnation", "", "the exact Grok receiver incarnation (follow only)")
				fs.Bool("all", false, "release every held message of the selected sessions")
			},
			Run: runInbox,
		},
		{
			Name:      "hook",
			Args:      "session-start|prompt|stop|session-end [--host codex|grok --plugin-root <dir>]",
			Summary:   "harness lifecycle hook entrypoint (Claude Code, Muse Code, Codex CLI)",
			Hidden:    true,
			MultiCall: true,
			Run:       runHookEntry,
		},
		{
			Name:      "watch",
			Args:      "[--sink <file> | --host codex]",
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
		{
			Name:      "sync-adapter",
			Args:      "<name> describe|attach|apply|status|detach",
			Summary:   "run a bundled sync adapter over the sync-adapter protocol on stdio",
			Hidden:    true,
			MultiCall: true,
			Run:       runSyncAdapterEntry,
		},
	}
}

// A SyncAdapterFunc runs one bundled sync adapter (docs/sync-adapters.md):
// args are the words after its name — the verb — and it speaks the
// sync-adapter protocol on the real process streams, returning the exit
// status.
type SyncAdapterFunc func(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int

// syncAdapters is the bundled sync adapters, by the name a team file's
// `sync` member gives (folder-sync plan §4.3). internal/app dispatches
// `brigade sync-adapter <name> <verb>` through it.
var syncAdapters = map[string]SyncAdapterFunc{
	syncthing.Name: syncthing.Run,
}

// LookupSyncAdapter returns the bundled sync adapter named name.
func LookupSyncAdapter(name string) (SyncAdapterFunc, bool) {
	f, ok := syncAdapters[name]
	return f, ok
}

// runSyncAdapterEntry is the table's Run for the `sync-adapter` entry,
// which internal/app intercepts before the table, exactly as `adapter`.
func runSyncAdapterEntry(*Context, []string) error {
	return usagef("sync-adapter", "the sync adapter entrypoint is dispatched by the multi-call seam; run `"+Program+" sync-adapter syncthing <verb>`")
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
	return usagef("watch", "the watcher entrypoint is dispatched by the multi-call seam; run `"+Program+" watch [--sink <file> | --host codex]`")
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
		All:    cx.Bool("all"),
		Team:   stringFlag(cx, "team"),
		Here:   cx.Bool("here"),
		Member: stringFlag(cx, "member"),
	})
}

// runSend is the table's Run for `send`.
func runSend(cx *Context, args []string) error {
	return harnesscmd.Send(invocation(cx, args), harnesscmd.SendOptions{
		Member:   stringFlag(cx, "member"),
		Summary:  stringFlag(cx, "summary"),
		ReplyTo:  stringFlag(cx, "reply-to"),
		BodyFile: stringFlag(cx, "body-file"),
	})
}

// runWhoami is the table's Run for `whoami`.
func runWhoami(cx *Context, args []string) error {
	return harnesscmd.Whoami(invocation(cx, args))
}

// runDoing is the table's Run for `doing` (card 25).
func runDoing(cx *Context, args []string) error {
	return harnesscmd.Doing(invocation(cx, args), harnesscmd.DoingOptions{Clear: cx.Bool("clear")})
}

// runNote is the table's Run for `note` (card 162).
func runNote(cx *Context, args []string) error {
	return harnesscmd.Note(invocation(cx, args))
}

// runDecision is the table's Run for `decision` (card 162).
func runDecision(cx *Context, args []string) error {
	return harnesscmd.Decision(invocation(cx, args), harnesscmd.DecisionOptions{
		To:      stringFlag(cx, "to"),
		Options: stringFlag(cx, "options"),
		Default: stringFlag(cx, "default"),
		By:      stringFlag(cx, "by"),
		Card:    cx.Int("card"),
		Close:   stringFlag(cx, "close"),
	})
}

// runReport is the table's Run for `report` (card 162; the verb is the
// one positional argument).
func runReport(cx *Context, args []string) error {
	return harnesscmd.Report(invocation(cx, args), harnesscmd.ReportOptions{
		Recipients: stringFlag(cx, "recipients"),
		Every:      stringFlag(cx, "every"),
		At:         stringFlag(cx, "at"),
		TZ:         stringFlag(cx, "tz"),
		Only:       stringFlag(cx, "only"),
		To:         stringFlag(cx, "to"),
	})
}

// runInbound is the table's Run for `inbound` (card 126; the one optional
// word is a positional argument).
func runInbound(cx *Context, args []string) error {
	return harnesscmd.Inbound(invocation(cx, args))
}

// runInbox is the table's Run for `inbox` and `inbox release` (the verb
// is the first positional argument; the flags are the harness's own, so
// the entry is not Raw).
func runInbox(cx *Context, args []string) error {
	return harnesscmd.Inbox(invocation(cx, args), harnesscmd.InboxOptions{
		Session:     stringFlag(cx, "session"),
		Incarnation: stringFlag(cx, "incarnation"),
		All:         cx.Bool("all"),
		Cursor:      stringFlag(cx, "cursor"),
	})
}

// runSync is the table's Run for `sync status` (card 33; the verb is the
// one positional argument).
func runSync(cx *Context, args []string) error {
	return harnesscmd.Sync(invocation(cx, args))
}

// runTeam is the table's Run for `team` (raw: the verb and the adapter
// flags are parsed by the command).
func runTeam(cx *Context, args []string) error {
	return harnesscmd.Team(invocation(cx, args))
}
