package syncthing

import (
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
	engine "github.com/appshapes/brigade/internal/syncthing"
)

// progName prefixes the one human line a failure writes to stderr and is
// the adapter's 4.4.1 name.
const progName = "brigade-adapter-syncthing"

// Name is the adapter's name: the `adapter` value of .brigade.json that
// selects it, the multi-call word after `brigade adapter`, and the
// `adapter` member of the profiles it owns.
const Name = "syncthing"

// storeVar is the test-only variable that names the teams root and, with
// it, switches the engine off (README, "Local mode"): the conformance
// suite's `--shared-env`, which no live session ever sets (the harness
// builds a child's environment from scratch, 4.1).
const storeVar = "BRIGADE_SYNCTHING_STORE"

// rootDirName is where the teams root lives under BRIGADE_STATE_DIR when
// nothing names it: <state dir>/syncthing/teams, each team's directory a
// Syncthing folder of its own.
const rootDirName = "syncthing/teams"

// usageText is the short usage written to stderr for -h and --help (stdout
// stays protocol output).
const usageText = `usage: brigade adapter syncthing [--root <dir>] [--profile <name>] [--log-level <l>] <group> <verb> [flags]

groups and verbs: describe; profile init|status|reset|revoke-credentials; team create|join|leave|members|
rotate-secret|revoke-member|transfer; session register|heartbeat|list|close; message send|receive|ack|watch.
Requests arrive on stdin as one JSON document; results leave on stdout as one 4.3 envelope. A join secret
is never accepted on the command line.`

var errHelp = errors.New("help requested")

// Run executes one command with the given streams and environment and
// returns its 4.6 exit status. Nothing here reads the process's own
// environment or streams; Main hands them in.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	return run(args, stdin, stdout, stderr, environ, time.Now, engine.RealDeps())
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string, now func() time.Time, d engine.Deps) int {
	c := &command{
		stdin: stdin, stdout: stdout, stderr: stderr,
		environ: environ, now: now, log: slog.New(slog.DiscardHandler), deps: d,
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

const (
	groupDescribe = "describe"
	groupTeam     = "team"
	groupSession  = "session"
	groupMessage  = "message"
	groupProfile  = "profile"
	verbWatch     = "watch"
)

// A command is one invocation: the parsed argv, the streams, and the
// profile, credential and store it resolves as it runs.
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
	deps    engine.Deps

	profileName string
	levelName   string
	levelFlag   bool
	rootFlag    string
	cfgDir      string

	profile *adapterkit.Profile
	cred    *credentialFile
	st      *store
}

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

// rejectJoinSecret refuses a join secret anywhere on argv (4.5.14, C-05).
func rejectJoinSecret(args []string) error {
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "join-secret" || name == "join_secret" {
			return errUsage("a join secret must never be passed on the command line")
		}
	}
	return nil
}

// parseLeading reads the flags the harness may put BEFORE the command
// group (4.1, decision 10): --root, --profile and --log-level. --root is
// accepted only here; after the verb it is unknown and therefore usage.
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
	c.log = adapterlog.New(c.stderr, level, nil).With(slog.String("comp", "adapter-syncthing"))
	return nil
}

func newFlags() *flag.FlagSet {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parse reads a verb's own flags. --profile and --log-level are accepted
// here too, the later occurrence winning.
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

// flagsSeen reports which flags a parsed set carried.
func flagsSeen(fs *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	return seen
}

// rootPath resolves the teams root (README, "Root resolution"): --root,
// else BRIGADE_SYNCTHING_STORE, else <BRIGADE_STATE_DIR>/syncthing/teams.
// A relative value is refused with `config`, exactly as adapterkit refuses
// a relative BRIGADE_STATE_DIR: hooks run with the working directory set to
// the project tree, and a relative root would put a team's messages
// inside somebody's repository.
func (c *command) rootPath() (string, error) {
	value, source := c.rootFlag, "--root"
	if value == "" {
		value, source = adapterkit.Getenv(c.environ, storeVar), storeVar
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

// local reports local mode: the root was named by --root or the test
// variable, so the store is a directory the caller shares by other means
// and no engine is started, configured or asked anything.
func (c *command) local() bool {
	return c.rootFlag != "" || adapterkit.Getenv(c.environ, storeVar) != ""
}

// engine is the machine's Syncthing instance under BRIGADE_STATE_DIR, or
// nil in local mode.
func (c *command) engine() (*engine.Engine, error) {
	if c.local() {
		return nil, nil
	}
	state, err := adapterkit.StateDir(c.environ)
	if err != nil {
		return nil, err
	}
	return engine.New(state, c.deps, c.log, c.environ), nil
}

func (c *command) profileDir() (string, error) {
	return adapterkit.ProfileDir(c.cfgDir, c.profileName)
}

func (c *command) credentialPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialFileName), nil
}

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

// authorize is the store half of the ladder: take the lock, sweep, finish
// a pending join when the team file has arrived, and apply the 4.5.7
// membership check. Commands that read a stdin document run it AFTER
// decoding, so a malformed or forged request is refused before the store
// is touched at all (4.4.6, C-23).
func (c *command) authorize() error {
	if err := c.openStore(); err != nil {
		return err
	}
	if err := c.completeJoin(); err != nil {
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
