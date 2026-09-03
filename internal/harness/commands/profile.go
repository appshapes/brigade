package commands

import (
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// The verbs of `brigade profile`.
var profileVerbs = []string{"init", "status", "reset", "revoke-credentials"}

// Profile implements `brigade profile init|status|reset|revoke-credentials
// [--profile <p>] …` (6.4, D36): every verb is the terminal pass-through
// of the adapter's own command, run anywhere (it touches only the user's
// own config directory), with two harness additions. `init --adapter
// <spec>` records the profile's default adapter FIRST — the sidecar, and
// for `<name>=<command>` the adapters.json entry — and then runs THAT
// adapter's `profile init` with the remaining arguments; without
// --adapter the adapter is the D36 chain (a fresh profile resolves to the
// bundled one). `status` prints one harness line naming the profile's
// default adapter and, inside a session, the override in force, then
// passes through.
func Profile(inv Invocation) error {
	verb, rest, err := verbOf(inv.Args, "profile", profileVerbs)
	if err != nil {
		return err
	}
	raw, err := parseRaw(rest, verb == "init")
	if err != nil {
		return err
	}
	inv = inv.withRaw(raw)
	switch verb {
	case "init":
		if raw.Adapter != "" {
			return inv.profileInitWithAdapter(raw)
		}
		return inv.passThrough("profile", "init", raw, nil)
	case "status":
		return inv.passThrough("profile", "status", raw, func(t *target) error {
			return writeLines(inv.Out, statusLine(t))
		})
	default:
		return inv.passThrough("profile", verb, raw, nil)
	}
}

// profileInitWithAdapter is `profile init --adapter <spec>`: the sidecar
// (and registry entry) is written before the adapter is spawned, so the
// profile the adapter creates is bound to it from the first moment; the
// adapter then runs with the same resolution the next SessionStart will
// make.
func (inv Invocation) profileInitWithAdapter(raw rawArgs) error {
	t, err := inv.terminalTarget(raw.Profile, true)
	if err != nil && !isCode(err, protocol.CodeConfig) {
		return err
	}
	if t == nil {
		// The D36 chain could not resolve the CURRENT default (a stale
		// sidecar, an unregistered profile member); --adapter replaces it,
		// so resolve the directories alone and go on.
		if t, err = inv.bareTarget(raw.Profile); err != nil {
			return err
		}
	}
	adapter, err := writeDefaultAdapter(t.configDir, t.profile, raw.Adapter)
	if err != nil {
		return err
	}
	t.adapter = adapter
	t.client = inv.client(t)
	return inv.spawnThrough(t, "profile", "init", raw.Rest, nil)
}

// bareTarget resolves only the profile and the directories of a terminal
// command, for the one caller that is about to replace the adapter.
func (inv Invocation) bareTarget(profileFlag string) (*target, error) {
	configDir, err := config.BrigadeConfigDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	profile := profileFlag
	if profile == "" {
		profile = config.ProfileName(inv.Environ)
	}
	if err := adapterkit.CheckProfileName(profile); err != nil {
		return nil, err
	}
	return &target{profile: profile, configDir: configDir, stateDir: stateDir}, nil
}

// writeDefaultAdapter records spec as the profile's default adapter (D36):
//
//   - `<name>=<absolute path>` or `<name>=<JSON array>` registers name in
//     adapters.json first (config.RegisterAdapter) and then writes the
//     sidecar naming it;
//   - `<name>` alone must already be registered, or be `supabase`;
//   - `<absolute path>` or `<JSON array>` writes the sidecar with the
//     value itself and registers nothing.
//
// The resolved Adapter is what the harness then spawns.
func writeDefaultAdapter(configDir, profile, spec string) (config.Adapter, error) {
	spec = strings.TrimSpace(spec)
	if name, command, ok := splitRegistration(spec); ok {
		argv, err := registrationArgv(command)
		if err != nil {
			return config.Adapter{}, err
		}
		if err := config.RegisterAdapter(configDir, name, argv); err != nil {
			return config.Adapter{}, err
		}
		return config.WriteSidecar(configDir, profile, name)
	}
	return config.WriteSidecar(configDir, profile, spec)
}

// splitRegistration recognises the `<name>=<command>` form: a registry
// name (the D36 character rule) before the FIRST '=', and a non-empty
// command after it. A path or an array never starts with a name
// character followed by '=', so the two other forms cannot be mistaken
// for a registration.
func splitRegistration(spec string) (name, command string, ok bool) {
	i := strings.IndexByte(spec, '=')
	if i <= 0 {
		return "", "", false
	}
	name, command = spec[:i], strings.TrimSpace(spec[i+1:])
	if config.CheckAdapterName(name) != nil || command == "" {
		return "", "", false
	}
	return name, command, true
}

// registrationArgv parses the command half of a registration: an absolute
// path (no whitespace) or a JSON array of strings with an absolute first
// element. Anything else is `config` adapter_malformed; the value is never
// echoed.
func registrationArgv(command string) ([]string, error) {
	malformed := &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the command of an adapter registration must be an absolute path or a JSON array of strings whose first element is an absolute path",
		Details: map[string]string{"reason": config.ReasonAdapterMalformed, "source": config.SourceRegistry},
	}
	if strings.HasPrefix(command, "[") {
		var argv []string
		if err := json.Unmarshal([]byte(command), &argv); err != nil || len(argv) == 0 {
			return nil, malformed
		}
		for _, a := range argv {
			if a == "" {
				return nil, malformed
			}
		}
		if !filepath.IsAbs(argv[0]) {
			return nil, &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "the adapter executable must be an absolute path",
				Details: map[string]string{"reason": config.ReasonAdapterRelative, "source": config.SourceRegistry},
			}
		}
		return argv, nil
	}
	if strings.ContainsAny(command, " \t\n\r\"[]{}") {
		return nil, malformed
	}
	if !filepath.IsAbs(command) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the adapter executable must be an absolute path",
			Details: map[string]string{"reason": config.ReasonAdapterRelative, "source": config.SourceRegistry},
		}
	}
	return []string{filepath.Clean(command)}, nil
}

// statusLine renders the one harness line of `profile status` (D36):
//
//	profile <p>: default adapter <name-or-argv> (from <source>)[; this
//	session overrides it with <argv>]
//
// The default is described as the user wrote it — the sidecar's own line,
// the profile file's `adapter` member, or `supabase` for the bundled
// fallback — and the override is named only when the session's map for
// this same profile carries a different resolved command. A default the
// D36 chain could not resolve is said so, with its reason.
func statusLine(t *target) string {
	line := "profile " + t.profile + ": default adapter "
	if t.defaultErr != nil {
		line += "unresolvable (" + defaultErrText(t.defaultErr) + ")"
	} else {
		line += defaultAdapterText(t) + " (from " + t.defaultAdapter.Source + ")"
	}
	if t.session != nil && t.session.Profile == t.profile {
		if override, ok := overrideText(t); ok {
			line += "; this session overrides it with " + override
		}
	}
	return line
}

// defaultErrText names a resolution failure by its fixed reason.
func defaultErrText(err error) string {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		if reason := perr.Details["reason"]; reason != "" {
			return string(perr.Code) + ": " + reason
		}
		return string(perr.Code)
	}
	return string(protocol.CodeConfig)
}

// defaultAdapterText names the profile's default adapter the way the
// user configured it.
func defaultAdapterText(t *target) string {
	switch t.defaultAdapter.Source {
	case config.SourceSidecar:
		if path, err := config.SidecarPath(t.configDir, t.profile); err == nil {
			if data, err := adapterkit.ReadStrict(path); err == nil {
				return oneLine(protocol.Sanitize(string(data)))
			}
		}
	case config.SourceProfile:
		if p, err := adapterkit.LoadProfile(t.configDir, t.profile); err == nil {
			return oneLine(protocol.Sanitize(p.Adapter))
		}
	case config.SourceBundled:
		return config.BundledAdapterName
	}
	if encoded, err := t.defaultAdapter.Encode(); err == nil {
		return encoded
	}
	return config.BundledAdapterName
}

// overrideText reports the session's adapter command when it differs
// from the profile's default.
func overrideText(t *target) (string, bool) {
	if t.defaultErr != nil {
		encoded, err := t.adapter.Encode()
		return encoded, err == nil
	}
	sessionCmd, err := t.adapter.Encode()
	if err != nil {
		return "", false
	}
	defaultCmd, err := t.defaultAdapter.Encode()
	if err != nil || sessionCmd == defaultCmd {
		return "", false
	}
	return sessionCmd, true
}
