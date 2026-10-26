package webhook

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/adapters/supabase"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
)

// DefaultListen is loopback: the inbound API is for a caller on the same
// machine (or behind a reverse proxy that terminates TLS).
const DefaultListen = "127.0.0.1:8787"

const usageText = `usage: brigade-webhook-gateway --target-url <url> --secret-file <path> [flags]
       brigade-webhook-gateway send --secret-file <path> (--to <session> | --member <who> | --reply-to <id>) [--text <t> | stdin]
       brigade-webhook-gateway sessions --secret-file <path>

Joins a Brigade team as an ordinary member session (harness gateway-webhook),
POSTs every message it receives to --target-url, signed, and turns signed
POSTs to <listen>/v1/messages into Brigade messages. Messages relayed this way
never approve anything.

The identity is configuration: the profile (--profile, or the one
--team-file names) in BRIGADE_CONFIG_DIR must already be bound to a team by
  brigade team join --secret-file <file> --label <member label>
with the same BRIGADE_CONFIG_DIR. Whichever member that binding is, the
gateway is one more session of it, named --session-name. The profile's
adapter is used (the bundled Supabase adapter by default).
`

// Run is the brigade-webhook-gateway entry point. It is also a multi-call
// binary: `adapter supabase …` runs the bundled Supabase adapter, which is
// what adapterclient spawns for a profile on the bundled adapter (it
// re-executes os.Executable with exactly those words), so one binary is
// enough on a machine with no `brigade` install.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	if len(args) >= 2 && args[0] == "adapter" && args[1] == "supabase" {
		return supabase.Run(args[2:], stdin, stdout, stderr, environ)
	}
	if len(args) >= 1 && (args[0] == "send" || args[0] == "sessions") {
		return runClient(ctx, args[0], args[1:], stdin, stdout, stderr)
	}
	fs := flag.NewFlagSet("brigade-webhook-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = io.WriteString(stderr, usageText+"\nflags:\n"); fs.PrintDefaults() }
	var (
		profile     = fs.String("profile", "", "Brigade profile (team key) the gateway runs as; default: from --team-file")
		teamFile    = fs.String("team-file", "", "a checkout's .brigade.json; names the team (and so the profile) to run as")
		adapterSpec = fs.String("adapter", "", "adapter override: a registered name or a JSON argv array (default: the profile's)")
		target      = fs.String("target-url", "", "URL every received message is POSTed to (https, or http on loopback)")
		secretFile  = fs.String("secret-file", "", "0600 file holding the shared HMAC secret (32+ bytes)")
		// No back-quotes in this usage string. flag.UnquoteUsage takes the
		// first back-quoted span as the flag's value name, so --help would
		// print `-header-file Name: value` and tell the operator the
		// argument is a header line — inviting a receiver's credential onto
		// argv, where a credential must never be. The argument is a path.
		headerFile  = fs.String("header-file", "", `0600 file of "Name: value" lines sent on every POST to --target-url (a receiver's own key)`)
		listen      = fs.String("listen", DefaultListen, `inbound API address; "off" disables inbound`)
		sessionName = fs.String("session-name", DefaultSessionName, "session name shown on the roster")
		description = fs.String("description", "Forwards messages to a webhook and back", "session description shown on the roster")
		lease       = fs.Int("lease", DefaultLeaseSeconds, "lease seconds (clamped to the adapter's range)")
		poll        = fs.Duration("poll", DefaultPoll, "inbox poll interval")
		maxAttempts = fs.Int("max-attempts", DefaultMaxAttempts, "retryable delivery failures before the sender is told and the message dropped")
		perMinute   = fs.Int("inbound-per-minute", DefaultInboundPerMinute, "accepted inbound POSTs per minute, all callers together")
		allowHTTP   = fs.Bool("allow-http", false, "permit a plain-http target URL that is not loopback")
		once        = fs.Bool("once", false, "register, deliver one batch, and exit (no inbound API)")
		logLevel    = fs.String("log-level", "info", "debug, info, warn or error")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// ParseLevel is the repository's grammar for this flag — the four
	// words and nothing else — and never echoes the value, which could be
	// a mis-pasted secret.
	level, err := adapterlog.ParseLevel(*logLevel)
	if err != nil {
		_, _ = io.WriteString(stderr, "brigade-webhook-gateway: --log-level must be one of: error, warn, info, debug\n")
		return 2
	}
	// Diagnostics go to stderr through the REDACTING logger, like every
	// other logger in the tree: this one carries an adapter child's error
	// text (adapterclient logs it, and fail() below prints it), which a
	// Supabase auth failure can fill with a JWT or an Authorization
	// header, and under the documented systemd unit stderr is journald.
	redactor := adapterlog.NewRedactor()
	log := adapterlog.New(stderr, level, redactor)
	fail := func(msg string, err error) int {
		if err != nil {
			msg += ": " + err.Error()
		}
		log.Error(msg)
		return 1
	}
	if err := checkTarget(*target, *allowHTTP); err != nil {
		return fail("invalid --target-url", err)
	}
	if err := checkSecretFile(*secretFile, *teamFile); err != nil {
		return fail(err.Error(), nil)
	}
	raw, err := adapterkit.ReadStrict(*secretFile)
	if err != nil {
		return fail("cannot read the secret file", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	// The shared secret is an exact token from here on: a diagnostic that
	// quotes a request or a header can never print it.
	redactor.Add(string(secret))
	var extra http.Header
	if *headerFile != "" {
		if err := checkPrivateFile("--header-file", *headerFile, *teamFile); err != nil {
			return fail(err.Error(), nil)
		}
		hraw, herr := adapterkit.ReadStrict(*headerFile)
		if herr != nil {
			return fail("cannot read the header file", herr)
		}
		if extra, herr = parseHeaderFile(hraw); herr != nil {
			return fail("invalid header file", herr)
		}
		// The values are a receiver's credential: exact tokens from here
		// on, as the shared secret is.
		redactor.Add(headerSecrets(extra)...)
	}
	if *profile == "" && *teamFile != "" {
		f, ferr := teamfile.Parse(*teamFile)
		if ferr != nil {
			return fail("cannot read --team-file", ferr)
		}
		*profile = teamstore.Key(f.Adapter, f.URL, f.TeamRef)
	}
	if *profile == "" {
		*profile = adapterkit.DefaultProfileName
	}
	if err := adapterkit.CheckProfileName(*profile); err != nil {
		return fail("invalid --profile", err)
	}
	configDir, stateDir, err := gatewayDirs(environ)
	if err != nil {
		return fail("cannot resolve the Brigade directories", err)
	}
	p, err := adapterkit.LoadProfile(configDir, *profile)
	if err != nil {
		return fail("the profile is not bound to a team on this machine (run `brigade team join` first, with the same BRIGADE_CONFIG_DIR)", err)
	}
	name := p.Adapter
	adapter, err := config.ResolveAdapter(config.Options{AdapterCommand: *adapterSpec}, configDir, name)
	if err != nil {
		return fail("cannot resolve the adapter", err)
	}
	client := &adapterclient.Client{
		Adapter: adapter, Profile: *profile, ConfigDir: configDir, StateDir: stateDir,
		Environ: environ, Logger: log,
	}
	gw, err := New(Config{
		TargetURL: *target, Secret: secret, ExtraHeaders: extra, SessionName: *sessionName, Description: *description,
		Version: buildinfo.String(), LeaseSeconds: *lease, Poll: *poll, MaxAttempts: *maxAttempts,
		StatePath: filepath.Join(stateDir, "webhook-gateway", *profile+".json"), Logger: log,
	}, client)
	if err != nil {
		return fail("invalid configuration", err)
	}
	sctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
	err = gw.Start(sctx)
	cancel()
	if err != nil {
		return fail("cannot register the gateway session", err)
	}
	if *once {
		tctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout+postTimeout*receiveLimit)
		defer cancel()
		n, err := gw.Deliver(tctx)
		if err != nil {
			return fail("delivery failed", err)
		}
		log.Info("delivered", slog.Int("acked", n))
		return 0
	}
	if *listen != "off" {
		srv := &http.Server{
			Addr: *listen, Handler: gw.Handler(*perMinute),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: inboundTimeout + 10*time.Second, MaxHeaderBytes: 16 << 10,
			BaseContext: func(net.Listener) context.Context { return ctx },
		}
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
		if err != nil {
			return fail("cannot listen", err)
		}
		log.Info("inbound API listening", slog.String("addr", ln.Addr().String()))
		go func() {
			if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
				log.Error("inbound API stopped", slog.String("error", serr.Error()))
			}
		}()
		defer func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
	}
	if err := gw.Run(ctx); err != nil {
		return fail("gateway stopped", err)
	}
	return 0
}

// sessionVarPrefixes are the coding-session markers this binary must not
// be seen to carry. They are matched by PREFIX, never by a name list:
// "CLAUDE" with no underscore covers CLAUDECODE as well as CLAUDE_*, and
// the next release of a harness may add names no list would have.
var sessionVarPrefixes = []string{"CLAUDE", "MUSE_", "CODEX_", "GROK_", "AI_AGENT"}

// gatewayDirs resolves the config and state directories the gateway's
// identity lives in.
//
// It strips the inherited session environment by prefix first and then
// resolves exactly as adapterkit does, rather than going through
// config.BrigadeConfigDir/BrigadeStateDir: those drop every BRIGADE_*
// variable when a session marker is set, because inside a harness the
// by-pid map decides identity and a repository's env block must not. This
// binary is the opposite case — it has no by-pid map, it is never inside a
// session, and BRIGADE_CONFIG_DIR *is* its identity. Started from a
// session's shell (the first thing anyone will try, and all an agent
// following the documentation can do) the harness rules would silently
// discard the variable, resolve the operator's own ~/.config/brigade, and
// bring the always-on gateway up on the operator's own credentials —
// which is the credential-sharing hazard the page warns against, with no
// error to show for it. Only CLAUDE_CONFIG_DIR survives the strip, as it
// does for every other out-of-session process (gates rule); the adapter
// child's environment is allow-listed separately by adapterkit.ChildEnv,
// so no session variable reaches it either way.
func gatewayDirs(environ []string) (configDir, stateDir string, err error) {
	environ = stripSession(environ)
	if configDir, err = adapterkit.ConfigDir(environ); err != nil {
		return "", "", err
	}
	if stateDir, err = adapterkit.StateDir(environ); err != nil {
		return "", "", err
	}
	return configDir, stateDir, nil
}

// stripSession returns environ without any variable whose name begins
// with one of sessionVarPrefixes, keeping CLAUDE_CONFIG_DIR.
func stripSession(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name != config.ClaudeConfigDirVar && hasAnyPrefix(name, sessionVarPrefixes) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// checkSecretFile is checkPrivateFile's rule for --secret-file.
func checkSecretFile(secretFile, teamFile string) error {
	return checkPrivateFile("--secret-file", secretFile, teamFile)
}

// checkPrivateFile refuses a path a credential flag (--secret-file,
// --header-file) must not name: a relative path, a path with a `..`
// component, and — wherever a project directory is knowable, which is
// wherever --team-file names one — a path inside that checkout. Every
// refusal names the flag it was given.
//
// The location is a rule, not advice (docs/security.md 14.1: a 0600 file
// outside the repository), and the repository already enforces it for the
// join secret in `brigade team create` and `team join`
// (commands.checkSecretFileOutside). It matters more here than it reads:
// the operator page tells the gateway's machine to keep the team's
// checkout, so `--team-file /path/to/checkout/.brigade.json
// --secret-file /path/to/checkout/secret` is a path a careless operator
// can take, and that repository is as public as this one.
//
// A `..` component is refused outright rather than cleaned (the same
// finding as P7-11): Dir and Clean collapse `lnk/..` lexically while the
// kernel resolves the symlink first, so `<outside>/lnk/../x.secret` with
// `lnk` pointing into the checkout would pass both spellings below and
// still land inside. Mode, owner and symlinks on the file itself are
// adapterkit.ReadStrict's job; this is only its location.
func checkPrivateFile(flagName, file, teamFile string) error {
	if file == "" || !filepath.IsAbs(file) {
		return errors.New(flagName + " must be an absolute path outside the team's checkout")
	}
	if slices.Contains(strings.Split(file, string(filepath.Separator)), "..") {
		return errors.New(flagName + " must not contain a `..` component; spell the path plainly")
	}
	if teamFile == "" {
		return nil
	}
	start := filepath.Dir(teamFile)
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	// The checkout the team file sits in, or — outside a repository, where
	// a plain path may name the team file — its own directory.
	top, ok := teamfile.Toplevel(start)
	if !ok {
		top = start
	}
	top = filepath.Clean(top)
	dir := filepath.Clean(filepath.Dir(file))
	// Both the literal spelling and the symlink-resolved one are checked,
	// as the join secret's are: a path may be inside the checkout under
	// either form and must not slip through the other.
	canonTop, terr := teamfile.Canonicalize(top)
	canonDir, derr := teamfile.Canonicalize(dir)
	if isUnder(dir, top) || (terr == nil && derr == nil && isUnder(canonDir, canonTop)) {
		return errors.New(flagName + " must not be inside the team's checkout (" + top +
			"): a gateway credential must never live under a repository")
	}
	return nil
}

// isUnder reports whether dir equals top or sits beneath it.
func isUnder(dir, top string) bool {
	return dir == top || strings.HasPrefix(dir+string(filepath.Separator), top+string(filepath.Separator))
}

// checkTarget requires https, or http to a loopback host, unless allowHTTP.
func checkTarget(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("an absolute http(s) URL is required")
	}
	if u.User != nil {
		return errors.New("credentials in the URL are refused; the signature authenticates the request")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) || allowHTTP {
			return nil
		}
		return errors.New("plain http is accepted only for a loopback host (or with --allow-http)")
	default:
		return errors.New("the scheme must be https or http")
	}
}
