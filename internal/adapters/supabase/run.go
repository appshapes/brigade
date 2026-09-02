package supabase

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// progName prefixes every human line this adapter writes to stderr.
const progName = "brigade-adapter-supabase"

// errHelp is the internal signal that argv asked for help. Help is a
// `usage` failure with the text on STDERR: stdout carries the 4.3 envelope
// and never free text (4.1).
var errHelp = errors.New("help requested")

// usageText is the whole help.
const usageText = "brigade adapter supabase" + ` [--profile <name>] [--log-level <l>] <group> <verb> [flags]

groups:  describe | team <create|join|leave|members> | session <register|heartbeat|list|close>
         message <send|receive|ack|watch> | profile <init|status|reset|revoke-credentials>
flags:   --session <id>  --include-offline  --limit <n>  --name  --label  --prompt  --secret-file
         profile init: --url <https-url> --key <publishable-key> [--force]
The backend is the profile's (profile init); no environment variable configures it (4.1).`

// run is Run with the clock injected, so tests drive the refresh margin
// of 5.1 without sleeping.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string, now func() time.Time) int {
	c := &command{
		stdin: stdin, stdout: stdout, stderr: stderr,
		environ: environ, now: now, log: slog.New(discardHandler{}),
		ctx: context.Background(),
	}
	if err := c.setup(args); err != nil {
		if errors.Is(err, errHelp) {
			_, _ = io.WriteString(stderr, usageText+"\n")
			return c.fail(errUsage("usage requested"))
		}
		return c.fail(err)
	}
	if c.group == groupMessage && c.verb == verbWatch {
		return c.watch()
	}
	result, err := c.execute()
	if err != nil {
		return c.fail(err)
	}
	return adapterkit.WriteResult(c.stdout, result)
}

// The command groups and the verbs of 4.1/4.2.
const (
	groupDescribe = "describe"
	groupTeam     = "team"
	groupSession  = "session"
	groupMessage  = "message"
	groupProfile  = "profile"
	verbWatch     = "watch"
)

// A command is one invocation: the parsed argv, the streams, the resolved
// directories and, once authenticate has run, the profile, the credential
// and the backend client.
type command struct {
	group string
	verb  string
	args  []string

	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	environ []string
	now     func() time.Time
	log     *slog.Logger
	ctx     context.Context

	profileName string
	levelName   string
	levelFlag   bool
	cfgDir      string

	profile *adapterkit.Profile
	cred    *session
	client  *client
}

// setup parses argv, resolves the profile name, the log level and the
// configuration directory, and builds the redacting stderr logger. The
// order is the fs adapter's (and the guide's): the poison scan, the
// grammar, the log level, the directories.
func (c *command) setup(args []string) error {
	if err := rejectJoinSecret(args); err != nil {
		return err
	}
	rest, err := c.parseLeading(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errUsage("no command given; expected a group and a verb")
	}
	c.group, rest = rest[0], rest[1:]
	switch c.group {
	case groupDescribe:
	case groupTeam, groupSession, groupMessage, groupProfile:
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return errUsage("the command group needs a verb")
		}
		c.verb, rest = rest[0], rest[1:]
	default:
		return errUsage("unknown command group")
	}
	c.args = rest
	if c.profileName == "" {
		c.profileName = adapterkit.ProfileName(c.environ)
	}
	if c.levelName == "" {
		c.levelName = adapterkit.Getenv(c.environ, "BRIGADE_LOG_LEVEL")
	}
	if err := c.applyLevel(); err != nil {
		return err
	}
	c.cfgDir, err = adapterkit.ConfigDir(c.environ)
	return err
}

// rejectJoinSecret enforces 4.1: --join-secret is refused with `usage` on
// every command of every adapter, and its value is never echoed (C-05).
func rejectJoinSecret(args []string) error {
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "join-secret" || name == "join_secret" {
			return errUsage("a join secret must never be passed on the command line")
		}
	}
	return nil
}

// parseLeading consumes the flags the harness may prepend through
// adapter_command's fixed arguments (4.1, decision 10): --profile and
// --log-level only. There is NO --root: this adapter's backend is the
// profile's, never an argument or a variable. An empty value is `usage`.
func (c *command) parseLeading(args []string) ([]string, error) {
	for len(args) > 0 {
		arg := args[0]
		if arg == "" || arg[0] != '-' {
			return args, nil
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch name {
		case "h", "help":
			return nil, errHelp
		case "profile", "log-level":
			if !hasValue {
				if len(args) < 2 {
					return nil, errUsage("a leading flag is missing its value")
				}
				value, args = args[1], args[1:]
			}
			if value == "" {
				return nil, errUsage("a leading flag was given an empty value")
			}
			if name == "profile" {
				c.profileName = value
			} else {
				c.levelName, c.levelFlag = value, true
			}
		default:
			return nil, errUsage("unknown flag before the command group")
		}
		args = args[1:]
	}
	return args, nil
}

// applyLevel resolves --log-level / $BRIGADE_LOG_LEVEL and rebuilds the
// logger. An invalid FLAG value is `usage`; an invalid ENVIRONMENT value
// is `config` (4.1, 4.6).
func (c *command) applyLevel() error {
	name := c.levelName
	if name == "" {
		name = "info"
	}
	level, err := adapterlog.ParseLevel(name)
	if err != nil {
		if c.levelFlag {
			return errUsage("--log-level must be one of: error, warn, info, debug")
		}
		return errConfig("BRIGADE_LOG_LEVEL must be one of: error, warn, info, debug", "invalid_value")
	}
	c.log = adapterlog.New(c.stderr, level, nil).With(slog.String("comp", "adapter-supabase"))
	return nil
}

// newFlags returns a flag set that never prints: a parse failure is a
// fixed-text `usage` refusal, because the offending argument could be a
// mis-pasted secret (4.1, C-05).
func newFlags() *flag.FlagSet {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parse finishes a command's flag set. --profile and --log-level apply to
// every command (4.1), so they are declared here rather than by each
// caller, and a positional argument left over is `usage` — that is what
// makes a verb after `describe` a refusal. An empty --profile after the
// verb fails CheckProfileName with `config` (the guide's measured rule).
func (c *command) parse(fs *flag.FlagSet) error {
	profile := fs.String("profile", c.profileName, "the profile to act on")
	level := fs.String("log-level", c.levelName, "error|warn|info|debug")
	if err := fs.Parse(c.args); err != nil {
		return errUsage("invalid arguments")
	}
	if fs.NArg() != 0 {
		return errUsage("unexpected argument")
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if seen["profile"] {
		c.profileName = *profile
	}
	if seen["log-level"] {
		c.levelName, c.levelFlag = *level, true
		if err := c.applyLevel(); err != nil {
			return err
		}
	}
	return adapterkit.CheckProfileName(c.profileName)
}

// requireSessionFlag reads the --session id every session and message
// verb but `register` and `list` needs; an absent value is `usage`.
func (c *command) requireSessionFlag(id string) (string, error) {
	if id == "" {
		return "", errUsage("--session <id> is required for this command")
	}
	return id, nil
}

// readInput reads the one stdin document of an input-taking command
// (adapterkit.ReadInput: TTY refusal, 1 MiB cap, empty is invalid_input)
// and decodes it with the shape's own Validate. It runs AFTER
// authenticate and BEFORE any network call, the fs adapter's order
// (C-02, C-23).
func (c *command) readInput(v protocol.Validator) error {
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return err
	}
	return protocol.Decode(data, v)
}

// fail writes the 4.3 failing envelope to stdout, one human line to
// stderr, and returns the exit status of the error's 4.6 code.
func (c *command) fail(err error) int {
	code := adapterkit.WriteError(c.stdout, err)
	message := "internal error"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		message = string(perr.Code) + ": " + perr.Message
	} else {
		c.log.Debug("unclassified failure", adapterlog.Err(err))
	}
	_, _ = io.WriteString(c.stderr, progName+": "+message+"\n")
	c.log.Debug("command failed", slog.String("group", c.group), slog.String("verb", c.verb))
	return code
}

// execute dispatches one command. `message watch` is not here: it writes
// NDJSON rather than an envelope and run calls it directly.
func (c *command) execute() (any, error) {
	switch c.group {
	case groupDescribe:
		return c.describe()
	case groupProfile:
		return c.profileCommand()
	case groupTeam:
		return c.teamCommand()
	case groupSession:
		return c.sessionCommand()
	case groupMessage:
		return c.messageCommand()
	default:
		return nil, errUsage("unknown command group")
	}
}

// discardHandler is the logger in force before --log-level is resolved: a
// failure that early has nothing to say that stderr's human line does not.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
