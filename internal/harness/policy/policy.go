// Package policy decides the inbound policy a session runs under — the
// step 3 of plan 6.8, with the native `crossSessionInbound` scan of 6.10
// folded in — and is the ONLY implementation of that decision: the
// SessionStart hook (P3-4) calls it to fill the by-pid map's `inbound`,
// the prompt-hook poll and the watcher's pipeline (internal/harness/
// inbound) run under the value it produced.
//
// D18 settles the shape: the policy is `accept`, `hold` or `refuse` and
// NOTHING else — there is no `auto` shape in any phase. `accept` is the
// default in every permission mode and entrypoint — bypassPermissions,
// auto, `claude -p` workers included — so a team message is injected
// immediately and nothing waits for a human. `refuse` and `hold` are the
// opt-ins (the `team_inbound` option): under `refuse` nothing is injected
// and nothing acknowledged; under `hold` (P5-9) each message is recorded
// in the session's pending file, injected only after the human releases
// it from a terminal (`brigade inbox release`), and acknowledged only
// then. `refuse` also applies whenever the option asked for something
// this harness cannot honour (an unknown spelling fails closed) and
// whenever the native settings scan found a `crossSessionInbound` of
// `hold` or `refuse` — even over an option of `hold`. The scan is
// load-bearing, not defensive (E0-9): under a native `refuse` a socket
// post SUCCEEDS and the frame vanishes, and under a native `hold` it is
// held natively, never expires and is lost at session end, so a Brigade
// `accept` or a released `hold` there would acknowledge messages Claude
// never saw — the message gone AND the sender told it arrived. Refusing
// to deliver into a session that cannot receive is strictly better than
// pretending to. `permission_mode`, `non_interactive` and the entrypoint
// are recorded for diagnostics only; Decide echoes them and a test table
// proves they never touch the outcome.
package policy

import (
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// A Policy is the effective inbound policy: Accept, Hold or Refuse (D18).
type Policy string

// The three policies.
const (
	Accept Policy = protocol.InboundAccept
	Hold   Policy = protocol.InboundHold
	Refuse Policy = protocol.InboundRefuse
)

// String renders the policy as the `inbound` wire value (4.4.2) and as the
// SessionStart context line prints it.
func (p Policy) String() string { return string(p) }

// Valid reports whether p is one of the three policies.
func (p Policy) Valid() bool { return p == Accept || p == Hold || p == Refuse }

// WarnOptionUnknown is attached when the option value handed to Effective is
// none of Accept, Hold and Refuse — a caller bug (config.ParseInbound never
// yields anything else) that fails closed rather than open.
const WarnOptionUnknown = `Brigade: the team_inbound option could not be interpreted; the inbound policy is refuse — nothing is acknowledged blind.`

// Effective computes the policy (6.8 step 3, 6.10): the option's own value
// when it is accept, hold or refuse; Refuse when the option was invalid
// (optionWarning is the config.ParseInbound warning that says so, passed
// through unchanged so the hook prints it); and Refuse — whatever the
// option said, `hold` included — when the native scan found `hold` or
// `refuse` (the scan's own warning is appended: Brigade's release path
// ends in a socket post, and under a native hold or refuse that post is
// lost while Brigade would acknowledge it, 3.6). The warnings come back in
// that order, each a fixed text that never echoes a setting value the user
// did not choose from the known ones.
func Effective(option config.Inbound, optionWarning string, native Scan) (Policy, []string) {
	var warnings []string
	p := Accept
	switch option {
	case config.InboundAccept:
	case config.InboundHold:
		p = Hold
	case config.InboundRefuse:
		p = Refuse
	default:
		p = Refuse
		if optionWarning == "" {
			optionWarning = WarnOptionUnknown
		}
	}
	if optionWarning != "" {
		warnings = append(warnings, optionWarning)
	}
	if native.Found {
		p = Refuse
		warnings = append(warnings, native.Warning())
	}
	return p, warnings
}

// Inputs are everything the hook knows when it decides the policy. Only
// Option, OptionWarning and Native are consulted; PermissionMode,
// NonInteractive and Entrypoint are diagnostics (6.5) that Decide copies
// into the Decision so the caller records them beside the policy, and a
// test table proves they cannot change it (D18: no `auto` shape).
type Inputs struct {
	// Option is the policy the team_inbound option asks for, as
	// config.ParseOptions or config.FromWatcherEnv resolved it.
	Option config.Inbound
	// OptionWarning is config.Options.TeamInboundWarning (or the watcher
	// env's), "" when the option was accept, hold or refuse.
	OptionWarning string
	// Native is the result of ScanNative.
	Native Scan
	// PermissionMode is the hook stdin's permission_mode; diagnostics only.
	PermissionMode string
	// NonInteractive is true for a `claude -p` session; diagnostics only.
	NonInteractive bool
	// Entrypoint is CLAUDE_CODE_ENTRYPOINT (cli, sdk-cli); diagnostics only.
	Entrypoint string
}

// A Decision is the policy plus the warnings the hook prints and the
// diagnostics it records.
type Decision struct {
	// Policy is the effective inbound policy.
	Policy Policy
	// Warnings are the fixed-text lines the hook prints as context, in
	// order: the option's, then the native scan's.
	Warnings []string
	// The diagnostics, echoed from Inputs unchanged.
	PermissionMode string
	NonInteractive bool
	Entrypoint     string
}

// Decide is Effective over Inputs, with the diagnostics carried through.
func Decide(in Inputs) Decision {
	p, warnings := Effective(in.Option, in.OptionWarning, in.Native)
	return Decision{
		Policy:         p,
		Warnings:       warnings,
		PermissionMode: in.PermissionMode,
		NonInteractive: in.NonInteractive,
		Entrypoint:     in.Entrypoint,
	}
}
