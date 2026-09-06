package fs

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// progName prefixes every human line this adapter writes to stderr. The
// P1-1 smoke script greps for it, and a person running the binary by hand
// needs to know which program refused them.
const progName = "brigade-adapter-fs"

// rootDirName is the store directory under ${BRIGADE_STATE_DIR} when
// neither --root nor BRIGADE_FS_ROOT names one.
const rootDirName = "fs-adapter"

// errHelp is the internal signal that argv asked for help. Help is a
// `usage` failure with the text on STDERR: stdout carries the 4.3 envelope
// and never free text (4.1).
var errHelp = errors.New("help requested")

// usageText is the whole help. This is a test adapter; nobody reads it.
const usageText = progName + ` [--root <dir>] [--profile <name>] [--log-level <l>] <group> <verb> [flags]

groups:  describe | team <create|join|leave|members> | session <register|heartbeat|list|close>
         message <send|receive|ack|watch> | profile <init|status|reset|revoke-credentials>
flags:   --session <id>  --include-offline  --limit <n>  --name  --label  --prompt  --secret-file  --force
root:    --root, else $BRIGADE_FS_ROOT, else $BRIGADE_STATE_DIR/` + rootDirName + `
This adapter is INSECURE and TEST-ONLY. Never point it at anything you care about.`

// Run executes one adapter command and returns its 4.6 exit status. It is
// the seam every test drives in-process: argv, the three streams and the
// environment are all parameters, so no test ever reads the developer's
// own environment or writes their own state directories.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	return run(args, stdin, stdout, stderr, environ, time.Now)
}

// run is Run with the clock injected, so tests can drive lease expiry and
// the retention sweep of 4.5.8/4.5.9 without sleeping.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string, now func() time.Time) int {
	c := &command{
		stdin: stdin, stdout: stdout, stderr: stderr,
		environ: environ, now: now, log: slog.New(discardHandler{}),
	}
	if err := c.setup(args); err != nil {
		if errors.Is(err, errHelp) {
			_, _ = io.WriteString(stderr, usageText+"\n")
			return c.fail(errUsage("usage requested"))
		}
		return c.fail(err)
	}
	defer c.finish()
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
// directories and, once authenticate and openStore have run, the profile,
// the credential and the locked store.
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

	profileName string
	levelName   string
	levelFlag   bool
	rootFlag    string
	cfgDir      string

	profile *adapterkit.Profile
	cred    *credentialFile
	st      *store
}

// setup parses argv, resolves the profile name, the log level and the
// configuration directory, and builds the redacting stderr logger.
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

// parseLeading consumes the flags the harness prepends through
// adapter_command's fixed arguments (4.1, decision 10):
// ["brigade-adapter-fs", "--root", "/x"] + ["describe"]. --root is
// accepted ONLY here; after the verb it is an unknown flag and therefore
// `usage` (C-02).
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
		case "root", "profile", "log-level":
			if !hasValue {
				if len(args) < 2 {
					return nil, errUsage("a leading flag is missing its value")
				}
				value, args = args[1], args[1:]
			}
			if value == "" {
				return nil, errUsage("a leading flag was given an empty value")
			}
			switch name {
			case "root":
				c.rootFlag = value
			case "profile":
				c.profileName = value
			default:
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
	c.log = adapterlog.New(c.stderr, level, nil).With(slog.String("comp", "adapter-fs"))
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
// makes a verb after `describe` a refusal.
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

// rootPath resolves the store root: --root, else $BRIGADE_FS_ROOT, else
// ${BRIGADE_STATE_DIR}/fs-adapter. A relative value is refused with
// `config`, exactly as adapterkit refuses a relative BRIGADE_STATE_DIR: a
// relative root would be resolved against the hook's working directory,
// which is the project tree.
func (c *command) rootPath() (string, error) {
	value, source := c.rootFlag, "--root"
	if value == "" {
		value, source = adapterkit.Getenv(c.environ, "BRIGADE_FS_ROOT"), "BRIGADE_FS_ROOT"
	}
	if value != "" {
		if !filepath.IsAbs(value) {
			return "", errConfig(source+" must be an absolute path", "relative_path")
		}
		return filepath.Clean(value), nil
	}
	state, err := adapterkit.StateDir(c.environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, rootDirName), nil
}

// profileDir is ${BRIGADE_CONFIG_DIR}/teams/<name>.
func (c *command) profileDir() (string, error) {
	return adapterkit.ProfileDir(c.cfgDir, c.profileName)
}

// credentialPath is the adapter's own credential file beside team.json.
func (c *command) credentialPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialFileName), nil
}

// loadProfile reads team.json. present is false only when the file is
// ABSENT; a malformed or world-readable file is `config` (4.6), never a
// silent "unconfigured".
func (c *command) loadProfile() (*adapterkit.Profile, bool, error) {
	path, err := adapterkit.ProfilePath(c.cfgDir, c.profileName)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	p, err := adapterkit.LoadProfile(c.cfgDir, c.profileName)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// loadCredential reads credential.json through the strict reader, so a
// group- or world-readable copy is refused with `config` (U-10).
func (c *command) loadCredential() (*credentialFile, bool, error) {
	path, err := c.credentialPath()
	if err != nil {
		return nil, false, err
	}
	data, err := adapterkit.ReadStrict(path)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var cf credentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, false, errConfig("credential file is not a valid JSON document", "malformed_json")
	}
	if cf.PrincipalRef == "" || !safeRef(cf.PrincipalRef) {
		return nil, false, errConfig("credential file names no usable principal", "invalid_credential")
	}
	return &cf, true, nil
}

func (c *command) saveCredential(cf *credentialFile) error {
	path, err := c.credentialPath()
	if err != nil {
		return err
	}
	return writeJSON(path, cf)
}

// authenticate is the ladder of the adapter design's section 5, in order:
// no profile is `config`, no credential is `unauthenticated`, and — for
// every command that is not itself a binding command — no team is `config`
// (4.6: "credential present, no team bound"; C-06, C-08).
func (c *command) authenticate(needTeam bool) error {
	p, ok, err := c.loadProfile()
	if err != nil {
		return err
	}
	if !ok {
		return errNoProfile()
	}
	cred, ok, err := c.loadCredential()
	if err != nil {
		return err
	}
	if !ok {
		return errNoCredential()
	}
	c.profile, c.cred = p, cred
	if needTeam && p.TeamRef == "" {
		return errNoTeam()
	}
	return nil
}

// lockStore takes the exclusive store lock, creating the root 0700. The
// watch loop calls it once per poll and releases immediately, so a
// concurrent send is never blocked for longer than one poll's work.
func (c *command) lockStore() (*store, error) {
	root, err := c.rootPath()
	if err != nil {
		return nil, err
	}
	return openStore(root, c.now)
}

// openStore takes the store lock and runs the retention sweep of 4.5.9.
// Every command except `describe` does this before it reads the store.
func (c *command) openStore() error {
	st, err := c.lockStore()
	if err != nil {
		return err
	}
	c.st = st
	return st.sweep(protocol.DefaultRetention())
}

// authorize is the store half of the ladder: take the lock, sweep, and
// apply the 4.5.7 membership check. Commands that read a stdin document
// run it AFTER decoding, so a malformed or forged request is refused
// before the store is touched at all (4.4.6, C-23).
func (c *command) authorize() error {
	if err := c.openStore(); err != nil {
		return err
	}
	return c.st.requireMembership(c.profile.TeamRef, c.cred.PrincipalRef)
}

// teamScoped is the whole ladder for a command that reads no input.
func (c *command) teamScoped() error {
	if err := c.authenticate(true); err != nil {
		return err
	}
	return c.authorize()
}

// finish releases the store lock.
func (c *command) finish() {
	if c.st != nil {
		c.st.close()
	}
}

// fail writes the 4.3 failing envelope to stdout, one human line to
// stderr, and returns the exit status of the error's 4.6 code.
func (c *command) fail(err error) int {
	code := adapterkit.WriteError(c.stdout, err)
	message := "internal error"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		message = string(perr.Code) + ": " + perr.Message
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
