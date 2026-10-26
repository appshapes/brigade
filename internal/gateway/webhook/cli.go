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
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
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
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		_, _ = io.WriteString(stderr, "brigade-webhook-gateway: invalid --log-level\n")
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
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
	if *secretFile == "" || !filepath.IsAbs(*secretFile) {
		return fail("--secret-file must be an absolute path", nil)
	}
	raw, err := adapterkit.ReadStrict(*secretFile)
	if err != nil {
		return fail("cannot read the secret file", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
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
	configDir, err := config.BrigadeConfigDir(environ)
	if err != nil {
		return fail("cannot resolve the Brigade config directory", err)
	}
	stateDir, err := config.BrigadeStateDir(environ)
	if err != nil {
		return fail("cannot resolve the Brigade state directory", err)
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
		TargetURL: *target, Secret: secret, SessionName: *sessionName, Description: *description,
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
