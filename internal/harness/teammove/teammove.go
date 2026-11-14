// Package teammove follows a team that moved to another backend (work
// item 273, part 2; ADR 0003; docs/security.md).
//
// A checkout's pin is its member's consent to one team at one backend. When
// the project's team file names another backend for the same team, the pin
// no longer matches, and until this package a session attached to nothing
// until the member ran `team join` with the secret file. Follow replaces
// that command in one case only: the backend the member consented to, and
// still holds a session on, says that the team's administrator recorded a
// move to exactly the backend the team file names. Then the machine joins
// the new backend with the move ticket the record carries, under the label
// the member had, and the pin is rewritten.
//
// The trust is the old backend's and never the file's. Nothing is sent to
// the backend the file names until the old backend has named it too:
// adapter, url, certificate and team reference, each compared for
// equality. A file that names anything else is the drifted file it always
// was, and the caller prints the drift line.
//
// This is the one writer of the team store that a hook reaches. The hook
// package itself still may not import teamstore/write (the depguard rule
// hook-reads-the-store): what it calls is Follow, which writes a pin only
// behind the comparison above.
package teammove

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// lockTimeout bounds the wait for another session of this machine that is
// following a move at the same moment. The one that waited finds the pin
// written and joins nothing.
const lockTimeout = 10 * time.Second

// lockName is the lock file's name under the config directory.
const lockName = "team-move.lock"

// ticketPrefix names the file the old backend's adapter writes the ticket
// to, in the config directory, for the moment between its answer and the
// join.
const ticketPrefix = ".move-ticket-"

// Capability is the 4.7 capability of the two commands a move's record is
// kept and read with. An adapter that does not advertise it is asked
// nothing about a move.
const Capability = "team.move"

// The reasons a move was not followed, for the caller's log. Each is a
// fixed word: nothing of a team file or of a backend's answer is in one.
const (
	ReasonNotAMove          = "not_a_move"
	ReasonNoOldBinding      = "no_old_binding"
	ReasonOldBindingDrifted = "old_binding_mismatch"
	ReasonAdapterUnresolved = "adapter_unresolved"
	ReasonLeft              = "left_the_new_backend"
	ReasonNoCapability      = "no_capability"
	ReasonOldBackendFailed  = "old_backend_failed"
	ReasonNoRecord          = "no_record"
	ReasonRecordElsewhere   = "record_names_another_backend"
	ReasonNoTicket          = "no_ticket"
	ReasonJoinRefused       = "join_refused"
	ReasonJoinedOther       = "joined_another_team"
	ReasonBusy              = "busy"
	ReasonStore             = "store"
)

// A Caller is what the follow uses of an adapter client
// (adapterclient.Client has both): its describe, which is answered from
// local files, and one command at a time.
type Caller interface {
	Describe(ctx context.Context) (*protocol.DescribeResult, error)
	Call(ctx context.Context, group, verb string, flags []string, stdin any) (*protocol.Envelope, error)
}

// Inputs is everything a follow is decided on.
type Inputs struct {
	// ConfigDir is the member's store: the pins, the bindings and the
	// adapters' credentials.
	ConfigDir string
	// Checkout is the canonical directory of the checkout, the key of its
	// pin.
	Checkout string
	// File is the project's team file as it is now, parsed.
	File *teamfile.File
	// Pin is the checkout's pin: what its member consented to.
	Pin *teamstore.Pin
	// Client builds an adapter client for an adapter's name and a profile,
	// which is a team key. An error means this machine cannot resolve that
	// adapter, and nothing is followed.
	Client func(adapter, profile string) (Caller, error)
	// Now is the clock.
	Now func() time.Time
	// Log takes the reasons and the adapters' errors, never a secret.
	Log *slog.Logger
}

// An Outcome is what Follow did.
type Outcome struct {
	// Followed is true when the checkout's pin now names the backend the
	// team file names.
	Followed bool
	// Joined is true when this call joined the new backend with the move
	// ticket, and false when the machine held a membership there already
	// (another checkout of the team followed or joined before this one).
	Joined bool
	// Reason says why a move was not followed: one of the Reason words.
	Reason string
}

// Candidate reports whether a pin and a team file are the two ends of a
// move, from what is on this machine alone: the same team, at another
// backend. A file that names another team, or the same backend with
// another key or certificate, is not a move and is never followed.
func Candidate(pin *teamstore.Pin, f *teamfile.File) bool {
	return pin != nil && f != nil && pin.TeamRef == f.TeamRef && (pin.Adapter != f.Adapter || pin.URL != f.URL)
}

// JoinBackend is the adapter-specific `backend` member of a `team join`
// request (4.4.10) as a team file names it: {url, publishable_key}, and for
// a Brigade server {url}, with {certificate} when the file pins one.
func JoinBackend(f *teamfile.File) map[string]string {
	if f.Adapter != config.BundledRestName {
		return map[string]string{"url": f.URL, "publishable_key": f.PublishableKey}
	}
	backend := map[string]string{"url": f.URL}
	if f.Certificate != "" {
		backend["certificate"] = f.Certificate
	}
	return backend
}

// RestBackendFlags are the `profile init` flags that name a Brigade
// server: its url, and the pin of its certificate when the team file
// carries one.
func RestBackendFlags(url, certificate string) []string {
	flags := []string{"--url", url}
	if certificate != "" {
		flags = append(flags, "--certificate", certificate)
	}
	return flags
}

// Host shows a backend's url as its host, for a line a person reads and
// for the `--from` a person types: what stands between `://` and the next
// `/`, and the text itself when it has no scheme. It is the one rule for
// both, so the host a command prints is the host it accepts.
func Host(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		host := raw[i+3:]
		if j := strings.IndexByte(host, '/'); j >= 0 {
			host = host[:j]
		}
		return host
	}
	return raw
}

// Follow follows the move in.File names, when the backend in.Pin names
// says the team's administrator recorded exactly that move. It returns
// Followed false with a Reason whenever it did not, having changed nothing
// that matters: at most this machine joined the new backend and the pin was
// not written, which the next call completes. An error is returned only
// with Followed false, for the caller's log beside the Reason.
func Follow(ctx context.Context, in Inputs) (Outcome, error) {
	if !Candidate(in.Pin, in.File) {
		return Outcome{Reason: ReasonNotAMove}, nil
	}
	oldKey := teamstore.Key(in.Pin.Adapter, in.Pin.URL, in.Pin.TeamRef)
	old, err := teamstore.LoadBinding(in.ConfigDir, oldKey)
	if err != nil {
		return Outcome{Reason: ReasonNoOldBinding}, err
	}
	// The binding of the team this checkout joined has to be the one the
	// pin describes, member by member: the key is never trusted alone.
	consented := &teamfile.File{
		Adapter: in.Pin.Adapter, URL: in.Pin.URL, PublishableKey: in.Pin.PublishableKey,
		Certificate: in.Pin.Certificate, TeamRef: in.Pin.TeamRef,
	}
	if err := teamstore.VerifyBinding(old, consented); err != nil {
		return Outcome{Reason: ReasonOldBindingDrifted}, err
	}
	oldClient, err := in.Client(in.Pin.Adapter, oldKey)
	if err != nil {
		return Outcome{Reason: ReasonAdapterUnresolved}, err
	}
	newKey := teamstore.Key(in.File.Adapter, in.File.URL, in.File.TeamRef)
	newClient, err := in.Client(in.File.Adapter, newKey)
	if err != nil {
		return Outcome{Reason: ReasonAdapterUnresolved}, err
	}
	// A machine that was a member at the new backend and left it stays out:
	// its profile there names a principal and no team. `team leave` is the
	// way out the follow's own line names, and a session in another checkout
	// must not undo it. The member comes back with `team join`.
	existing, berr := teamstore.LoadBinding(in.ConfigDir, newKey)
	if berr == nil && existing.TeamRef == "" && existing.PrincipalRef != "" {
		return Outcome{Reason: ReasonLeft}, nil
	}
	// An adapter that does not carry the record's two commands is asked
	// nothing: its describe is answered from local files.
	desc, err := oldClient.Describe(ctx)
	if err != nil {
		return Outcome{Reason: ReasonOldBackendFailed}, err
	}
	if !slices.Contains(desc.Capabilities, Capability) {
		return Outcome{Reason: ReasonNoCapability}, nil
	}

	lock, err := adapterkit.LockFile(filepath.Join(in.ConfigDir, lockName), lockWait(ctx))
	if err != nil {
		return Outcome{Reason: ReasonBusy}, err
	}
	defer func() { _ = lock.Unlock() }()
	// Another session of this machine may have followed while this one
	// waited: the pin then matches, and there is nothing left to do.
	if pin, pinned, perr := teamstore.LookupPin(in.ConfigDir, in.Checkout); perr == nil && pinned && pin.Matches(in.File) {
		return Outcome{Followed: true}, nil
	}
	// A follow that was killed between its adapter's answer and its own end
	// left its ticket file behind. Under the lock no other follow of this
	// machine is in that moment, so whatever is there is such a leftover.
	sweepTickets(in.ConfigDir)

	// The one question the follow asks, of the backend the member consented
	// to: did this team move, and where. The ticket comes back in a file of
	// the store's, never on a stream, and the file is gone before Follow
	// returns whatever happens.
	ticketFile, err := ticketPath(in.ConfigDir)
	if err != nil {
		return Outcome{Reason: ReasonStore}, err
	}
	defer func() { _ = os.Remove(ticketFile) }()
	env, err := oldClient.Call(ctx, "team", "moved", []string{"--ticket-file", ticketFile}, nil)
	if err != nil {
		return Outcome{Reason: ReasonOldBackendFailed}, err
	}
	var record protocol.TeamMoveResult
	if uerr := json.Unmarshal(env.Result, &record); uerr != nil || record.Validate() != nil {
		return Outcome{Reason: ReasonOldBackendFailed}, errors.New("teammove: team moved answered a shape this build does not read")
	}
	if !record.Moved {
		return Outcome{Reason: ReasonNoRecord}, nil
	}
	// The whole decision: the record names exactly what the file names. A
	// file that points anywhere else is sent nothing.
	if record.TeamRef != in.File.TeamRef || record.Adapter != in.File.Adapter ||
		record.URL != in.File.URL || record.Certificate != in.File.Certificate {
		return Outcome{Reason: ReasonRecordElsewhere}, nil
	}

	joined := false
	if berr != nil || teamstore.VerifyBinding(existing, in.File) != nil {
		// This machine holds no membership at the new backend that the file
		// describes: it joins with the ticket, under the label the member had.
		if record.TicketFile == "" {
			return Outcome{Reason: ReasonNoTicket}, nil
		}
		ticket, terr := adapterkit.ReadTicketFile(ticketFile)
		if terr != nil || adapterkit.CheckMoveTicket(ticket, in.File.TeamRef) != nil {
			return Outcome{Reason: ReasonNoTicket}, terr
		}
		if berr == nil {
			// A profile is there that does not describe the file's backend:
			// one a refused join left, or one made under a certificate the
			// server has since replaced. The record named this backend, so
			// the profile is moved to it before the join, as a terminal join
			// moves one the member consented to.
			if reason, aerr := align(ctx, in, newClient, newKey, existing); reason != "" {
				return Outcome{Reason: reason}, aerr
			}
		}
		if reason, jerr := join(ctx, in, newClient, newKey, ticket, old.HumanLabel); reason != "" {
			return Outcome{Reason: reason}, jerr
		}
		carryLabelOrigin(in, oldKey, newKey, old.HumanLabel)
		joined = true
	}
	if err := write.Pin(in.ConfigDir, in.Checkout, teamstore.Pin{
		Adapter: in.File.Adapter, URL: in.File.URL, PublishableKey: in.File.PublishableKey, Certificate: in.File.Certificate,
		TeamRef: in.File.TeamRef, ConsentedAt: in.Now().UTC(), MovedFrom: in.Pin.URL,
	}); err != nil {
		return Outcome{Reason: ReasonStore}, err
	}
	return Outcome{Followed: true, Joined: joined}, nil
}

// lockWait is how long a follow waits for another of this machine: the
// lock's own bound, and never more than half of what the caller's deadline
// leaves, so a hook that is short of time gives up and says so instead of
// being stopped from outside in the middle of a follow.
func lockWait(ctx context.Context) time.Duration {
	wait := lockTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline) / 2; left < wait {
			wait = max(left, 0)
		}
	}
	return wait
}

// sweepTickets removes ticket files an earlier follow left in the config
// directory. Best effort: a file that cannot be removed is tried again by
// the next follow.
func sweepTickets(configDir string) {
	left, err := filepath.Glob(filepath.Join(configDir, ticketPrefix+"*"))
	if err != nil {
		return
	}
	for _, path := range left {
		_ = os.Remove(path)
	}
}

// align moves a Brigade server's profile that is already there to the url
// and the certificate the team file names, in the adapter's profile and in
// the binding together, before a join is sent to it: the adapter takes a
// join's backend only on a profile that names none, and answers `conflict`
// to another certificate. Another adapter's profile is left as it is.
func align(ctx context.Context, in Inputs, client Caller, key string, existing *adapterkit.Profile) (string, error) {
	if in.File.Adapter != config.BundledRestName || (existing.URL == in.File.URL && existing.Certificate == in.File.Certificate) {
		return "", nil
	}
	flags := append([]string{"--force"}, RestBackendFlags(in.File.URL, in.File.Certificate)...)
	if _, err := client.Call(ctx, "profile", "init", flags, nil); err != nil {
		return ReasonJoinRefused, err
	}
	if err := write.PatchBindingBackend(in.ConfigDir, key, in.File.Adapter, in.File.URL, in.File.PublishableKey, in.File.Certificate); err != nil {
		return ReasonStore, err
	}
	return "", nil
}

// join sends the one `team join` of a follow to the new backend and writes
// the binding. It answers a Reason when the machine did not become a
// member the file describes, "" when it did.
func join(ctx context.Context, in Inputs, client Caller, key, ticket, label string) (string, error) {
	backend, err := json.Marshal(JoinBackend(in.File))
	if err != nil {
		return ReasonStore, err
	}
	req := &protocol.TeamJoinRequest{JoinSecret: ticket, HumanLabel: label, Backend: backend}
	env, err := client.Call(ctx, "team", "join", nil, req)
	if err != nil {
		return ReasonJoinRefused, err
	}
	var answer protocol.TeamJoinResult
	if uerr := json.Unmarshal(env.Result, &answer); uerr != nil || answer.Validate() != nil {
		return ReasonJoinRefused, errors.New("teammove: team join answered a shape this build does not read")
	}
	if answer.TeamRef != in.File.TeamRef {
		return ReasonJoinedOther, errors.New("teammove: the backend answered a team other than the one the team file names")
	}
	if err := write.EnsureBinding(in.ConfigDir, key, in.File.Adapter, in.File.URL, in.File.PublishableKey, in.File.Certificate,
		answer.TeamRef, answer.TeamName, answer.PrincipalRef); err != nil {
		return ReasonStore, err
	}
	return "", nil
}

// carryLabelOrigin copies the record of where the member's label came from
// to the new credential directory, when the old one describes the label
// that was just sent: the label is the same text, from the same source,
// and `whoami` goes on saying where it came from. Best effort, as the
// record is everywhere: without it `whoami` says the origin is unrecorded.
func carryLabelOrigin(in Inputs, oldKey, newKey, label string) {
	if label == "" {
		return
	}
	origin, err := teamstore.LoadLabelOrigin(in.ConfigDir, oldKey)
	if err != nil || !origin.Describes(label) {
		return
	}
	carried := *origin
	carried.SentAt = in.Now().UTC()
	if err := write.LabelOrigin(in.ConfigDir, newKey, carried); err != nil && in.Log != nil {
		in.Log.Debug("the label's record was not carried to the new backend's directory", slog.String("error", err.Error()))
	}
}

// ticketPath is a path under the store for the old backend's adapter to
// write the ticket to: a name no other file has, in a directory that is
// the member's alone.
func ticketPath(configDir string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.Join(configDir, ticketPrefix+hex.EncodeToString(b[:])), nil
}
