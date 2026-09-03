package commands

import (
	"context"
	"slices"
	"strings"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the argument grammar of the raw commands (`team`,
// `profile`): the harness consumes exactly its own flags — the globals
// --json and --log-level, the profile selector --profile and, for `profile
// init`, --adapter — and forwards every other argument to the adapter in
// order, so an adapter flag the harness has never heard of (--prompt,
// --url, --secret-file, a future one) reaches it untouched (6.4). A bare
// `--` ends the harness's parsing; what follows is forwarded verbatim.

// The harness-owned flags of the raw grammar.
const (
	flagJSON     = "json"
	flagLogLevel = "log-level"
	flagProfile  = "profile"
	flagAdapter  = "adapter"
)

// rawArgs is one parsed raw argument vector.
type rawArgs struct {
	// JSON, LogLevel, Profile and Adapter are the harness's own flags.
	JSON     bool
	LogLevel string
	Profile  string
	Adapter  string
	// Rest is everything forwarded to the adapter, in order.
	Rest []string
}

// parseRaw parses args with the raw grammar. adapterFlag says whether
// --adapter is the harness's here (`profile init` only); elsewhere it is
// forwarded like any other flag.
func parseRaw(args []string, adapterFlag bool) (rawArgs, error) {
	var out rawArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out.Rest = append(out.Rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			out.Rest = append(out.Rest, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch {
		case name == flagJSON:
			// --json[=true|false]; a value other than the two is usage,
			// as the stdlib parser would say.
			if !hasValue || value == "true" {
				out.JSON = true
			} else if value != "false" {
				return out, usage("invalid arguments: --json takes no value")
			}
		case name == flagLogLevel || name == flagProfile || (adapterFlag && name == flagAdapter):
			if !hasValue {
				if i+1 >= len(args) {
					return out, usage("invalid arguments: --" + name + " needs a value")
				}
				i++
				value = args[i]
			}
			if value == "" {
				return out, usage("invalid arguments: --" + name + " was given an empty value")
			}
			switch name {
			case flagLogLevel:
				out.LogLevel = value
			case flagProfile:
				out.Profile = value
			default:
				out.Adapter = value
			}
		default:
			out.Rest = append(out.Rest, arg)
		}
	}
	if out.LogLevel != "" {
		if _, err := adapterlog.ParseLevel(out.LogLevel); err != nil {
			return out, usage("unknown --log-level; expected one of debug, info, warn, error")
		}
	}
	return out, nil
}

// withRaw applies the raw flags the cli could not see (they came after the
// command word) to the invocation: --json and --log-level.
func (inv Invocation) withRaw(raw rawArgs) Invocation {
	if raw.JSON {
		inv.JSON = true
	}
	if raw.LogLevel != "" {
		inv.LogLevel = raw.LogLevel
	}
	return inv
}

// passThrough resolves the adapter for a terminal command (D36, anywhere)
// and runs `<group> <verb> <rest…>` with the caller's own streams,
// forwarding the adapter's exit status as an ExitStatus error. before, when
// non-nil, runs after the adapter is resolved and before it is spawned
// (`profile status` prints its harness line there).
func (inv Invocation) passThrough(group, verb string, raw rawArgs, before func(*target) error) error {
	t, err := inv.terminalTarget(raw.Profile, true)
	if err != nil {
		return err
	}
	return inv.spawnThrough(t, group, verb, raw.Rest, before)
}

// spawnThrough runs the pass-through against an already-resolved target.
func (inv Invocation) spawnThrough(t *target, group, verb string, rest []string, before func(*target) error) error {
	if before != nil {
		if err := before(t); err != nil {
			return err
		}
	}
	exit, err := t.client.PassThrough(context.Background(), group, verb, rest, inv.In, inv.Out, inv.Err)
	if err != nil {
		return err
	}
	if exit != 0 {
		return ExitStatus(exit)
	}
	return nil
}

// verbOf splits the verb off a raw argument vector: the first argument,
// which must be a bare word.
func verbOf(args []string, command string, verbs []string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, usage(command + " needs a verb: " + strings.Join(verbs, ", "))
	}
	if !slices.Contains(verbs, args[0]) {
		return "", nil, usage("unknown " + command + " verb; expected one of " + strings.Join(verbs, ", "))
	}
	return args[0], args[1:], nil
}

// refuseInSession is the `usage` refusal of `team create`/`team join`
// inside a Claude Code session (6.4): the join secret must never pass
// through the chat, so the command is a terminal's only.
func refuseInSession() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUsage,
		Message: RefusalInSession,
		Details: map[string]string{"reason": "in_session"},
	}
}
