package cli

import (
	"errors"
	"flag"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Program is the name the binary is documented and invoked under. It is a
// constant on purpose: argv[0] is the bootstrap's cache path on every
// legitimate call (6.4), so it is not a usable identity.
const Program = "brigade"

// PoisonFlag is the flag name whose mere presence on argv is a usage error:
// a join secret must never be visible in a process listing, a shell
// history, or a Claude Code transcript (4.5.14, C-05, 5.11).
const PoisonFlag = "join-secret"

// poisonMessage is fixed text. It must never quote the offending argument.
const poisonMessage = "join secrets must never be passed on the command line; " +
	"put the secret in a 0600 file outside the repository and run `" + Program + " team join --secret-file <path>`, or supply it on stdin"

// errPoison is returned by the poison flag's Set method. The flag package
// wraps it with %v rather than %w and the wrapped text contains the secret,
// so callers must detect the poison through poisonHit and must never print
// the error returned by Parse.
var errPoison = errors.New(poisonMessage)

// poisonValue is a flag.Value that fails on any assignment.
type poisonValue struct{ hit *bool }

func (p poisonValue) String() string { return "" }

func (p poisonValue) Set(string) error {
	*p.hit = true
	return errPoison
}

// LogLevels are the accepted values of the global --log-level flag.
var LogLevels = []string{"debug", "info", "warn", "error"}

// globals holds the two flags every command accepts, wherever they appear.
// One globals value is shared by the pre-command flag set and the command's
// own flag set, so the two parses cannot disagree.
type globals struct {
	json     bool
	logLevel string
	poison   bool
}

// register adds the global and poison flags to fs. Every flag set in the
// binary goes through here, which is what makes --json, --log-level and the
// poison flag uniform across the command table.
func (g *globals) register(fs *flag.FlagSet) {
	fs.BoolVar(&g.json, "json", g.json, "write the protocol JSON envelope to stdout instead of human output")
	fs.StringVar(&g.logLevel, "log-level", g.logLevel, "log verbosity: "+strings.Join(LogLevels, "|"))
	fs.Var(poisonValue{hit: &g.poison}, PoisonFlag, "refused: secrets are never passed on argv")
}

// newFlagSet builds a flag set with the 7.3 conventions: ContinueOnError so
// a parse error is returned rather than exiting, and a discarded output so
// nothing the flag package formats can reach stdout or stderr (a flag value
// may be a secret).
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseInterspersed lets flags follow positional arguments
// (`brigade send <session_id> --reply-to <id>`), which stdlib flag does not:
// Parse stops at the first non-flag argument. Everything after a `--`
// separator is positional. Copied from plan 7.3.
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error) {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		tail, args = args[i+1:], args[:i]
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err // flag.ErrHelp or a usage error; the caller maps to help / exit 2
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return append(positional, tail...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// scanPoison reports whether argv mentions the poison flag anywhere at all,
// including after a `--` separator and in positions no flag set would ever
// parse. This runs before any parsing so that a secret cannot reach an
// error message by a route the flag sets do not cover.
func scanPoison(args []string) bool {
	long, short := "--"+PoisonFlag, "-"+PoisonFlag
	for _, a := range args {
		if a == long || a == short ||
			strings.HasPrefix(a, long+"=") || strings.HasPrefix(a, short+"=") {
			return true
		}
	}
	return false
}

// scanJSONFlag reports whether args mentions the --json flag. It decides
// which stream an error goes to whenever the flag sets cannot be trusted to
// have seen the flag: an unknown command word has no flag set at all, a Raw
// command has no second parse, and stdlib flag ABORTS at the first bad
// flag, so in `brigade version --bad-flag --json` the parse never reaches
// --json. Scanning stops at a bare "--" because everything after it is
// positional.
func scanJSONFlag(args []string) bool {
	long, short := "--json", "-json"
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == long || a == short ||
			a == long+"=true" || a == short+"=true" {
			return true
		}
	}
	return false
}

// validateLogLevel maps an unknown --log-level value to a usage error. The
// value is echoed because a log level is not a secret, and an unreadable
// error here would be worse than useless.
func validateLogLevel(command, level string) *Error {
	if level == "" || slices.Contains(LogLevels, level) {
		return nil
	}
	return usagef(command, "unknown --log-level "+strconv.Quote(level)+"; expected one of "+strings.Join(LogLevels, ", "))
}
