package config

import (
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The plugin options as the hooks receive them (6.1, A.5): Claude Code
// exports USER-SET values only, as CLAUDE_PLUGIN_OPTION_<KEY>; defaults
// are never exported and are applied here.
const (
	OptionProfile             = "CLAUDE_PLUGIN_OPTION_PROFILE"
	OptionConfigDir           = "CLAUDE_PLUGIN_OPTION_CONFIG_DIR"
	OptionAdapterCommand      = "CLAUDE_PLUGIN_OPTION_ADAPTER_COMMAND"
	OptionTeamInbound         = "CLAUDE_PLUGIN_OPTION_TEAM_INBOUND"
	OptionShareWorkspaceLabel = "CLAUDE_PLUGIN_OPTION_SHARE_WORKSPACE_LABEL"
	OptionWorkspaceLabel      = "CLAUDE_PLUGIN_OPTION_WORKSPACE_LABEL"
	OptionPollOnPrompt        = "CLAUDE_PLUGIN_OPTION_POLL_ON_PROMPT"
)

// The BRIGADE_* variables that stand in for two options OUTSIDE a session
// (a human's terminal, 4.1). Inside a session they are stripped.
const (
	envAdapterCommand = "BRIGADE_ADAPTER_COMMAND"
	envTeamInbound    = "BRIGADE_TEAM_INBOUND"
)

// The details.reason values for option failures.
const (
	// ReasonInvalidProfileName: the profile option fails
	// adapterkit.CheckProfileName.
	ReasonInvalidProfileName = "invalid_profile_name"
	// ReasonInvalidBoolean: a boolean option is none of the accepted
	// spellings.
	ReasonInvalidBoolean = "invalid_boolean"
)

// Inbound is the harness's inbound policy as the team_inbound option asks
// for it: accept, hold or refuse (D18's value set, nothing else). Group
// D's policy package consumes it and folds in the native settings scan.
type Inbound string

// The three Inbound values.
const (
	InboundAccept Inbound = protocol.InboundAccept
	InboundHold   Inbound = protocol.InboundHold
	InboundRefuse Inbound = protocol.InboundRefuse
)

// The warning ParseInbound attaches. The hook prints it as context so the
// user learns why their session refuses.
const (
	// WarnInboundInvalid: the option is none of accept, hold and refuse.
	// The value is deliberately not echoed.
	WarnInboundInvalid = `Brigade: team_inbound must be "accept", "hold" or "refuse"; the value set is none of them, so the inbound policy is refuse — nothing is acknowledged blind.`
)

// ParseInbound maps a team_inbound value to the policy the option asks
// for: "" or "accept" → accept; "hold" → hold; "refuse" → refuse; anything
// else → refuse with WarnInboundInvalid. Matching is exact after trimming
// surrounding whitespace: an unexpected spelling fails closed rather than
// being guessed at.
func ParseInbound(raw string) (Inbound, string) {
	switch strings.TrimSpace(raw) {
	case "", protocol.InboundAccept:
		return InboundAccept, ""
	case protocol.InboundHold:
		return InboundHold, ""
	case protocol.InboundRefuse:
		return InboundRefuse, ""
	default:
		return InboundRefuse, WarnInboundInvalid
	}
}

// ParseBool parses a boolean option: "" is false (unset); true/false,
// 1/0, yes/no, on/off, case-insensitively after trimming; anything else is
// an error the caller maps to `config`.
func ParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0", "no", "off":
		return false, nil
	case "true", "1", "yes", "on":
		return true, nil
	default:
		return false, errInvalidBool
	}
}

// errInvalidBool is the sentinel ParseBool returns; optionErr wraps it.
var errInvalidBool = &protocol.Error{
	Code:    protocol.CodeConfig,
	Message: "boolean option must be true or false (also accepted: 1/0, yes/no, on/off)",
	Details: map[string]string{"reason": ReasonInvalidBoolean},
}

// Options are the plugin options with their defaults applied (6.1). They
// are the hook's configuration; the session-bound commands and the
// watcher never see them and read the by-pid map instead.
type Options struct {
	// Profile is the validated profile name; default "default".
	Profile string
	// ConfigDir is the absolute Brigade config directory: the config_dir
	// option, else the XDG default — never BRIGADE_CONFIG_DIR inside a
	// session.
	ConfigDir string
	// AdapterCommand is the raw adapter_command option, "" when unset. It
	// is the D36 per-session override; ResolveAdapter interprets it.
	AdapterCommand string
	// TeamInbound is the policy the option asks for (accept, hold or
	// refuse); TeamInboundWarning is non-empty when the option was invalid
	// and the hook must print it.
	TeamInbound        Inbound
	TeamInboundWarning string
	// ShareWorkspaceLabel gates WorkspaceLabel.
	ShareWorkspaceLabel bool
	// WorkspaceLabel is the sanitised label to register, "" unless
	// ShareWorkspaceLabel is on and the label is non-empty. Never the
	// working directory (T10).
	WorkspaceLabel string
	// PollOnPrompt enables the prompt-hook poll (6.3).
	PollOnPrompt bool
}

// ParseOptions resolves Options from environ. Only CLAUDE_PLUGIN_OPTION_*
// is read for the options themselves; the fallbacks for an unset option
// go through Trusted, so BRIGADE_PROFILE, BRIGADE_CONFIG_DIR,
// BRIGADE_ADAPTER_COMMAND and BRIGADE_TEAM_INBOUND are honoured only
// outside a session and an explicit option always wins over them.
//
// Failures are `config` (exit 11) with details.option naming the option
// and details.reason the rule: an invalid profile name, a relative
// config_dir, an unparsable boolean. The offending value is never echoed.
func ParseOptions(environ []string) (Options, error) {
	trusted := Trusted(environ)
	opt := func(name string) string { return strings.TrimSpace(adapterkit.Getenv(environ, name)) }
	var o Options

	o.Profile = opt(OptionProfile)
	if o.Profile == "" {
		o.Profile = adapterkit.ProfileName(trusted)
	}
	if err := adapterkit.CheckProfileName(o.Profile); err != nil {
		return Options{}, optionErr("profile", ReasonInvalidProfileName, "profile option must be 1-64 characters from letters, digits, dot, dash and underscore, and must not start with a dot")
	}

	if v := opt(OptionConfigDir); v != "" {
		if !filepath.IsAbs(v) {
			return Options{}, optionErr("config_dir", ReasonRelativePath, "config_dir option must be an absolute path")
		}
		o.ConfigDir = filepath.Clean(v)
	} else {
		dir, err := adapterkit.ConfigDir(trusted)
		if err != nil {
			return Options{}, err
		}
		o.ConfigDir = dir
	}

	o.AdapterCommand = opt(OptionAdapterCommand)
	if o.AdapterCommand == "" {
		o.AdapterCommand = strings.TrimSpace(adapterkit.Getenv(trusted, envAdapterCommand))
	}

	inbound := opt(OptionTeamInbound)
	if inbound == "" {
		inbound = adapterkit.Getenv(trusted, envTeamInbound)
	}
	o.TeamInbound, o.TeamInboundWarning = ParseInbound(inbound)

	share, err := ParseBool(opt(OptionShareWorkspaceLabel))
	if err != nil {
		return Options{}, optionErr("share_workspace_label", ReasonInvalidBoolean, errInvalidBool.Message)
	}
	o.ShareWorkspaceLabel = share
	if share {
		o.WorkspaceLabel = protocol.SanitizeLabel(opt(OptionWorkspaceLabel))
	}

	poll, err := ParseBool(opt(OptionPollOnPrompt))
	if err != nil {
		return Options{}, optionErr("poll_on_prompt", ReasonInvalidBoolean, errInvalidBool.Message)
	}
	o.PollOnPrompt = poll
	return o, nil
}

// optionErr is the `config` failure for one option. message is fixed
// text; the value is never part of it.
func optionErr(option, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"option": option, "reason": reason},
	}
}
