// Package teammove follows a team that moved to another backend (work
// item 273, part 2; ADR 0003; work item 327, ADR 0008; docs/security.md).
// The backend a team leaves is whichever keeps the record: a Supabase
// project, the filesystem store, and from 0.57.0 a Brigade server (work
// item 341, ADR 0010). Nothing below asks which.
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
// behind the comparison above, and beside the pin only the record of a
// join the new backend refused (RejoinPause). A session's start calls it
// in its own run; a prompt, which has no time for it, through the child it
// starts, `brigade hook follow` (work item 327, ADR 0008).
package teammove

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"log/slog"
	"net/url"
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
// written, or the membership another checkout's follow made, and joins
// nothing.
const lockTimeout = 10 * time.Second

// lockName is the lock file's name under the config directory.
const lockName = "team-move.lock"

// newBackendWait bounds the one question a follow asks of the backend the
// team moved to (Recorded), each time it asks it. The question is asked
// under the lock above, which every follow and every join of the machine
// takes, and a server that accepts the connection and says nothing would
// otherwise hold that lock for as long as the follow's caller gives the
// whole follow. It is the lock's own bound: no longer than a follow or a
// member's join waits for the lock, and never longer than what the
// caller's deadline leaves, which is all the old backend's question has.
// A variable only so that a test can shorten it.
var newBackendWait = lockTimeout

// RejoinPause is how long a follow presents no ticket to a backend that
// refused the join it made under the membership this machine holds there
// (work item 341). A backend counts the joins a principal fails and stops
// hearing it after a few, the right secret included: the Brigade server
// after five in fifteen minutes. A follow runs at every session's start and
// once a minute while a session is prompted, so without the pause a member
// who was put out of a backend, and whose ticket no longer admits, spent
// that count without a command of theirs and was then refused their own
// `brigade team join` with the right secret. With it a follow fails one
// join in the backend's window, and the member's own join is heard. The
// pause ends early when a join there succeeds.
const RejoinPause = 15 * time.Minute

// Lock waits for the follow's lock for a caller that joins this machine to
// a team by another road: a member's own `brigade team join`. A follow
// reads the machine's membership under this lock and joins only when it
// finds none, so a join made beside it, before either has a credential,
// leaves the backend two memberships of one machine. The caller holds the
// lock from its own reading of the membership to its pin, and never while
// it waits for a person. It waits for wait, or as long as a follow does
// when wait is zero, and the directory is made first: a machine's first
// join has no store yet.
//
// The lock is one for the whole store, and a follow holds it across its
// question to the old backend, which can hang for as long as that
// backend's adapter waits. So an error here is not the caller's to fail on:
// a join is its member's own command, for this team or any other, and goes
// on without the lock (firstJoin). Follow reads the pin and the membership
// again once its question is answered, and finds what such a join made.
func Lock(configDir string, wait time.Duration) (*adapterkit.FileLock, error) {
	if wait <= 0 {
		wait = lockTimeout
	}
	if err := adapterkit.MkdirPrivate(configDir); err != nil {
		return nil, err
	}
	return adapterkit.LockFile(filepath.Join(configDir, lockName), wait)
}

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
	// ReasonOldBackendFailed: the backend the member joined did not answer
	// the one question a follow asks of it, whether the team moved: it could
	// not be reached, did not answer in time, or said to wait (unanswered);
	// or its adapter could not say what it offers, and the question was
	// never asked. The follow learned nothing, of that backend or of the one
	// the team file names.
	ReasonOldBackendFailed = "old_backend_failed"
	// ReasonOldBackendRefused: the backend the member joined answered that
	// question, and its answer is no record: a refusal, above all that the
	// caller is no active member of the team there (a member who left it or
	// was revoked), or a shape this build does not read. That is an answer,
	// as ReasonNewBackendRefused is on the other side: waiting mends
	// nothing, and what helps is the member's own join where the team is.
	ReasonOldBackendRefused = "old_backend_refused"
	ReasonNoRecord          = "no_record"
	ReasonRecordElsewhere   = "record_names_another_backend"
	ReasonNoTicket          = "no_ticket"
	ReasonJoinRefused       = "join_refused"
	ReasonJoinedOther       = "joined_another_team"
	ReasonBusy              = "busy"
	ReasonStore             = "store"
	// ReasonNewBackendFailed: the backend the team file names did not answer
	// what a follow asks of it: the question asked with the membership this
	// machine holds there, or the join made with the move's ticket, which is
	// the first thing asked of a backend the machine was never a member of.
	// It could not be reached, did not answer in time, or said to wait
	// (unanswered). Nothing is pinned and no refusal is put on record, and
	// the next follow asks again.
	ReasonNewBackendFailed = "new_backend_failed"
	// ReasonNewBackendRefused: the backend the team file names answered that
	// question, and its answer is a refusal other than that the caller is no
	// active member: above all `unauthenticated`, a credential it does not
	// know, which is what a server started again from an older copy of its
	// data answers a member who joined since. Nothing is changed. Waiting
	// mends nothing here: the member joins there with the team's secret.
	ReasonNewBackendRefused = "new_backend_refused"
	// ReasonRejoinRefused: the backend the team file names does not honour
	// the membership this machine holds there, and refused the join made
	// with the move's ticket under that membership's credential: this follow
	// was refused, or one was within RejoinPause, and this one presented
	// nothing. Nothing is changed. What helps is the member's own join with
	// the team's current secret.
	ReasonRejoinRefused = "rejoin_refused"
	// ReasonEachOther: the backend the team file names itself records that
	// the team moved to the backend the checkout is pinned to. The two name
	// each other, and a session follows neither (work item 341).
	ReasonEachOther = "backends_name_each_other"
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
	// BeforePin, when set, is called once, when this call is about to write
	// the checkout's pin, with whether it joined the new backend. A follow
	// that runs with nobody to tell (the child a prompt hook starts, work
	// item 327) leaves word for the next hook of the checkout here: the pin
	// is what says the checkout followed, so whatever is to be found beside
	// it is in place first. It is not called by a follow that finds the pin
	// written: another follow of this machine got there, and the word is
	// that one's to leave.
	BeforePin func(joined bool)
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

// SameAddress reports whether two addresses are trivially one server's:
// they are equal once the scheme and the host are in lower case, a port
// that is the scheme's own is left out and the slashes at the end are
// dropped. `https://Brigade.example.com:443/` and
// `https://brigade.example.com` are one server; `localhost` and
// `127.0.0.1` are not known to be, and neither are two names of one
// machine. An address that carries credentials or a query is the same as
// nothing but itself.
func SameAddress(a, b string) bool {
	if a == b {
		return true
	}
	ka := addressKey(a)
	return ka != "" && ka == addressKey(b)
}

// addressKey is an address in the one spelling SameAddress compares, ""
// for text that is no such address.
func addressKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

// FollowsFromVersion is the first Brigade version whose sessions follow a
// recorded move (0.55.0, work item 273): a session of an older one says
// that its team file does not match, and its member joins with the secret
// file as before.
const FollowsFromVersion = "0.55.0"

// FollowsFromServerVersion is the first Brigade version whose sessions
// follow a move that a Brigade server recorded (0.57.0, work item 341, ADR
// 0010). The record is read by the adapter of the backend a team leaves,
// and until that version the `rest` adapter carried no such command: a
// session of an older plugin whose team is on a server asks that server
// nothing, says that its team file does not match, and its member joins
// with the secret file.
const FollowsFromServerVersion = "0.57.0"

// FollowsFrom is the first Brigade version whose sessions follow a move
// recorded on a backend of the adapter named: FollowsFromServerVersion for
// a Brigade server, FollowsFromVersion for every other.
func FollowsFrom(adapter string) string {
	if adapter == config.BundledRestName {
		return FollowsFromServerVersion
	}
	return FollowsFromVersion
}

// OlderThanFollow reports whether a session that reports the Brigade
// version v is older than the first one that follows a recorded move
// (FollowsFromVersion).
func OlderThanFollow(v string) bool { return olderThan(v, FollowsFromVersion) }

// OlderThanFollowFrom is OlderThanFollow for a move recorded on a backend
// of the adapter named: a session whose team is on a Brigade server
// follows from FollowsFromServerVersion on.
func OlderThanFollowFrom(adapter, v string) bool { return olderThan(v, FollowsFrom(adapter)) }

// olderThan reports whether the Brigade version v is older than first,
// which is one of the two constants above. Only a version this build can
// read counts: three dot-separated runs of decimal digits, with whatever
// follows a `-` or a `+` left out (a development build reports
// `0.55.0-dev`). Anything else, "" and "unknown" included, is false:
// nothing is said about a session whose version cannot be read.
func olderThan(v, first string) bool {
	followsFrom, ok := versionNumbers(first)
	if !ok {
		return false
	}
	got, ok := versionNumbers(v)
	if !ok {
		return false
	}
	for i := range got {
		if got[i] != followsFrom[i] {
			return got[i] < followsFrom[i]
		}
	}
	return false
}

// versionNumbers reads a version as its three numbers.
func versionNumbers(v string) ([3]int, bool) {
	var got [3]int
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != len(got) {
		return got, false
	}
	for i, part := range parts {
		// Nine digits at most: a run that long is no version, and it keeps
		// the number inside an int on every platform.
		if part == "" || len(part) > 9 || strings.Trim(part, "0123456789") != "" {
			return got, false
		}
		for _, digit := range part {
			got[i] = got[i]*10 + int(digit-'0')
		}
	}
	return got, true
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
	// nothing: its describe is answered from local files. A describe that
	// fails has asked no backend anything, so it is no refusal whatever its
	// code: only an answer to the question below can be one. Nothing was
	// learned, and what an earlier follow left stays (work item 341).
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
	// And a follow of ANOTHER checkout of the team may have joined while this
	// one waited: the pin above is that checkout's, not this one's, so the
	// membership is what shows it. It was read before the lock, to send a
	// machine that left away without a question; under the lock no follow is
	// between its join and its binding, so it is read again here, and this
	// one joins only if the machine still holds no membership the file
	// describes. Without this, two checkouts that followed in one instant
	// each presented the move's ticket (work item 327).
	existing, berr = teamstore.LoadBinding(in.ConfigDir, newKey)
	if berr == nil && existing.TeamRef == "" && existing.PrincipalRef != "" {
		return Outcome{Reason: ReasonLeft}, nil
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
	// A question that fails is told apart as the other backend's is below: a
	// backend that did not answer has said nothing, and a refusal is an
	// answer (oldBackendReason). Until this both were one reason, and a
	// member the backend had put out was taken for one whose backend was
	// only out of reach (work item 341).
	env, err := oldClient.Call(ctx, "team", "moved", []string{"--ticket-file", ticketFile}, nil)
	if err != nil {
		return Outcome{Reason: oldBackendReason(err)}, err
	}
	var record protocol.TeamMoveResult
	if uerr := json.Unmarshal(env.Result, &record); uerr != nil || record.Validate() != nil {
		return Outcome{Reason: ReasonOldBackendRefused}, errors.New("teammove: team moved answered a shape this build does not read")
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

	// The question above is asked under the lock and has no bound of its
	// own: it can take as long as the caller gives the whole follow. The
	// questions to the backend the team moved to, below, are under the lock
	// too, and each has a bound (newBackendWait). A member's own `brigade
	// team join` waits ten seconds for that lock and then goes on without
	// it (Lock), so the store can have changed since it was read above: what
	// this follow is about to act on is read again here, just before it
	// would join. A pin that
	// matches the file is the member's own join in this checkout, and is
	// theirs: nothing is written over it and no word is left. A membership
	// is the one that join made, in this checkout or in another, and this
	// follow presents no ticket for a second one.
	if pin, pinned, perr := teamstore.LookupPin(in.ConfigDir, in.Checkout); perr == nil && pinned && pin.Matches(in.File) {
		return Outcome{Followed: true}, nil
	}
	existing, berr = teamstore.LoadBinding(in.ConfigDir, newKey)
	if berr == nil && existing.TeamRef == "" && existing.PrincipalRef != "" {
		return Outcome{Reason: ReasonLeft}, nil
	}

	// A membership this machine holds there is one the backend may no longer
	// honour: the team was on that backend before, its administrator ended
	// the memberships there once everyone had followed away, and the team
	// has come back (work item 341). So the backend is asked with it before
	// the pin is written on its strength. `unauthorized` is that backend's
	// word that the caller is no active member: the machine then joins with
	// the move's ticket ON THE PROFILE IT HOLDS, so the adapter presents the
	// credential it has and the backend acts for the same principal
	// (4.4.10: "the same principal_ref, rejoined: true"). Nothing is ever
	// set aside. A member who was revoked comes back as who they were; one
	// who was banned is refused, because a ban is by principal, and stays
	// out; and the team's owner, answered `unauthorized` once by something
	// in front of the backend, joins as the principal that owns the team
	// and owns it still. A backend that did not answer has said nothing
	// about the membership: nothing is changed, and the next follow asks
	// again. One that answered any other refusal, a credential it does not
	// know above all, is not one to wait for: nothing is changed either, and
	// the caller tells the member to join there (ReasonNewBackendRefused).
	//
	// And where the backend refuses the join itself, the refusal is put on
	// record and no follow presents a ticket there for RejoinPause: the
	// backend counts the joins a principal fails, and the member's own join
	// with the right secret must still be heard (ReasonRejoinRefused). A join
	// that was not answered is no refusal and is not put on record: the
	// backend did not answer (join), and the next follow presents the ticket
	// again.
	held := berr == nil && teamstore.VerifyBinding(existing, in.File) == nil
	rejoin := false
	if held {
		theirs, aerr := askNew(ctx, newClient)
		switch {
		case unauthorized(aerr):
			rejoin = true
		case unanswered(aerr):
			return Outcome{Reason: ReasonNewBackendFailed}, aerr
		case aerr != nil:
			return Outcome{Reason: ReasonNewBackendRefused}, aerr
		case namesBack(theirs, in.Pin, in.File.TeamRef):
			return Outcome{Reason: ReasonEachOther}, nil
		default:
			// The backend honours the membership: a refusal on record is over.
			forgetRefusal(in, newKey)
		}
	}
	joined := false
	if !held || rejoin {
		// A join this machine made there under the membership it holds was
		// refused a short while ago: no ticket is presented to that backend
		// for this team until RejoinPause has passed, whichever checkout or
		// session asks. The backend counts every join a principal fails.
		if refusedLately(in, newKey) {
			return Outcome{Reason: ReasonRejoinRefused}, nil
		}
		// This machine holds no membership at the new backend that the file
		// describes, or one the backend does not honour: it joins with the
		// ticket, under the label the member had.
		ticket, reason, terr := readTicket(record, ticketFile, in.File.TeamRef)
		if reason != "" {
			return Outcome{Reason: reason}, terr
		}
		if berr == nil && !held {
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
			if rejoin && unauthorized(jerr) {
				// The backend refused the principal this machine is there: a
				// member who is banned, or one who was revoked and whose
				// ticket no longer admits. It is on record, so that the next
				// follows present nothing (RejoinPause).
				if werr := write.RejoinRefusal(in.ConfigDir, newKey, in.Now().UTC()); werr != nil && in.Log != nil {
					in.Log.Warn("the record of a refused join was not written; the next follow presents the ticket again", slog.String("error", werr.Error()))
				}
				return Outcome{Reason: ReasonRejoinRefused}, jerr
			}
			return Outcome{Reason: reason}, jerr
		}
		forgetRefusal(in, newKey)
		CarryLabelOrigin(in.ConfigDir, oldKey, newKey, old.HumanLabel, in.Now().UTC(), in.Log)
		joined = true
		// Now that this machine may ask: what the new backend itself records
		// of the team. A question that fails here is left at that, the join
		// having been made on the old backend's record. It is asked before
		// the pin is written and cannot move behind it: the pin is what moves
		// the checkout, and the rule is that a checkout between two backends
		// that name each other does not move. A pin written first would have
		// to be taken back, and every session that started in between would
		// have followed. So it stays under the lock, with its own bound.
		if theirs, aerr := askNew(ctx, newClient); aerr == nil && namesBack(theirs, in.Pin, in.File.TeamRef) {
			return Outcome{Reason: ReasonEachOther}, nil
		}
	}
	if in.BeforePin != nil {
		in.BeforePin(joined)
	}
	if err := write.Pin(in.ConfigDir, in.Checkout, teamstore.Pin{
		Adapter: in.File.Adapter, URL: in.File.URL, PublishableKey: in.File.PublishableKey, Certificate: in.File.Certificate,
		TeamRef: in.File.TeamRef, ConsentedAt: in.Now().UTC(), MovedFrom: in.Pin.URL,
	}); err != nil {
		return Outcome{Reason: ReasonStore}, err
	}
	return Outcome{Followed: true, Joined: joined}, nil
}

// refusedLately reports whether a join this machine made at the backend key
// names, under the membership it holds there, was refused within
// RejoinPause. A record from the future is a clock that was set back, and
// bounds nothing.
func refusedLately(in Inputs, key string) bool {
	refusal, err := teamstore.LoadRejoinRefusal(in.ConfigDir, key)
	if err != nil {
		return false
	}
	since := in.Now().Sub(refusal.RefusedAt)
	return since >= 0 && since < RejoinPause
}

// forgetRefusal removes the record of a refused join at the backend key
// names: a join there succeeded, or the backend honours the membership.
// Best effort: a record that stays keeps a follow from presenting a ticket
// for at most RejoinPause more.
func forgetRefusal(in Inputs, key string) {
	if err := write.ClearRejoinRefusal(in.ConfigDir, key); err != nil && in.Log != nil {
		in.Log.Debug("the record of a refused join was not removed", slog.String("error", err.Error()))
	}
}

// readTicket reads the move's ticket from the file the old backend's
// adapter wrote it to, and holds it to the team. It answers a Reason when
// there is no ticket to join with.
func readTicket(record protocol.TeamMoveResult, ticketFile, teamRef string) (string, string, error) {
	if record.TicketFile == "" {
		return "", ReasonNoTicket, nil
	}
	ticket, err := adapterkit.ReadTicketFile(ticketFile)
	if err != nil || adapterkit.CheckMoveTicket(ticket, teamRef) != nil {
		return "", ReasonNoTicket, err
	}
	return ticket, "", nil
}

// askNew asks the backend the team file names what it records of the team
// itself, with the membership this machine holds there: the one command of
// a follow that goes to that backend beside the join. It answers nil for a
// backend whose adapter keeps no such record, which is asked nothing. The
// question is asked for two answers: whether the backend still honours the
// membership (an `unauthorized` says that it does not), and whether it
// records that the team moved on (namesBack). It waits newBackendWait for
// the answer and no longer: it is asked under the follow's lock.
func askNew(ctx context.Context, client Caller) (*protocol.TeamMoveResult, error) {
	ctx, cancel := context.WithTimeout(ctx, newBackendWait)
	defer cancel()
	record, _, err := Recorded(ctx, client)
	return record, err
}

// Recorded asks a backend what it records of where its team went: the one
// way every part of Brigade asks it (a follow, the one command's check of
// the server a team returns to, the record's own form, a command that is
// run again). keeps is false for a backend whose adapter does not carry
// capability team.move: it keeps no such record and is asked nothing, its
// describe being answered from local files. Otherwise the record is what
// `team moved` answered, with no ticket asked for, and an error is the
// adapter's own, or says that the answer is of a shape this build does not
// read. A backend that could not be asked is not one that records nothing:
// a caller tells the two apart by err.
func Recorded(ctx context.Context, client Caller) (record *protocol.TeamMoveResult, keeps bool, err error) {
	desc, err := client.Describe(ctx)
	if err != nil {
		return nil, false, err
	}
	if !slices.Contains(desc.Capabilities, Capability) {
		return nil, false, nil
	}
	env, err := client.Call(ctx, "team", "moved", nil, nil)
	if err != nil {
		return nil, true, err
	}
	var theirs protocol.TeamMoveResult
	if uerr := json.Unmarshal(env.Result, &theirs); uerr != nil || theirs.Validate() != nil {
		return nil, true, &protocol.Error{
			Code:    protocol.CodeProtocolMismatch,
			Message: "team moved answered a shape this build does not read",
			Details: map[string]string{"reason": "bad_result"},
		}
	}
	return &theirs, true, nil
}

// A ClearOutcome is what became of a clear of a backend's record.
type ClearOutcome int

const (
	// Cleared: the backend answered that it holds no record now.
	Cleared ClearOutcome = iota
	// ClearRefused: the record is known to stand. The backend refused the
	// clear before it acted, or answered a record.
	ClearRefused
	// ClearUnanswered: the request may have reached the backend and been
	// carried out, with its answer lost on the way back, or may never have
	// arrived. Whether the record stands is not known, and `team move
	// --show` reads it.
	ClearUnanswered
)

// ClearRecord asks a backend to clear its record of where the team went,
// and says which of three things is true afterwards: the one way every
// part of Brigade clears one, so that each says the same of a clear that
// was refused and of one that was not answered. The error is the cause of
// an outcome that is not Cleared.
func ClearRecord(ctx context.Context, client Caller) (ClearOutcome, error) {
	env, err := client.Call(ctx, "team", "move", []string{"--clear"}, nil)
	if err != nil {
		// A clear the backend refused before it acted leaves a record that is
		// known to stand. One that was not answered does not (unanswered: a
		// backend out of reach and one that says to wait, which until this
		// was told as a refusal, work item 341), and neither does a failure
		// of the backend or an answer that is no envelope.
		var perr *protocol.Error
		if !unanswered(err) && errors.As(err, &perr) && perr.Code != protocol.CodeInternal && perr.Code != protocol.CodeProtocolMismatch {
			return ClearRefused, err
		}
		return ClearUnanswered, err
	}
	var after protocol.TeamMoveResult
	if uerr := json.Unmarshal(env.Result, &after); uerr != nil || after.Validate() != nil {
		return ClearUnanswered, &protocol.Error{
			Code:    protocol.CodeProtocolMismatch,
			Message: "team move --clear answered a shape this build does not read",
			Details: map[string]string{"reason": "bad_result"},
		}
	}
	if after.Moved {
		return ClearRefused, &protocol.Error{
			Code:    protocol.CodeProtocolMismatch,
			Message: "team move --clear answered a record",
			Details: map[string]string{"reason": "bad_result"},
		}
	}
	return Cleared, nil
}

// unauthorized reports whether a backend's answer is its `unauthorized`:
// the caller is no active member of the team there.
func unauthorized(err error) bool {
	var perr *protocol.Error
	return errors.As(err, &perr) && perr.Code == protocol.CodeUnauthorized
}

// unanswered reports whether a question to a backend failed without an
// answer about the member or the team: a deadline of the caller's own that
// ran out, and every failure the protocol's own table calls retryable
// (protocol.Code.Retryable, 4.6), which is the one list of them. That is
// `unavailable`, a backend that could not be reached, one that failed and
// an adapter that did not finish within its deadline; and `rate_limited`,
// a backend that takes no more requests just now and says how long to
// wait. Until the table was asked, only the first of the two counted, and a
// backend that said to wait was taken for one that had refused the member
// (work item 341). Any other failure is an answer: the backend, or its
// adapter, said no, and so did whatever stands in front of it and answered
// with something that is no envelope.
func unanswered(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var perr *protocol.Error
	return errors.As(err, &perr) && perr.Code.Retryable()
}

// RefusesCaller reports whether err is a backend's answer that the caller
// may not ask what it asked: `unauthorized`, the caller is no active member
// of the team there, or `unauthenticated`, a credential the backend does
// not know. It is an answer about the caller, and so never true of a
// question that was not answered (unanswered), nor of an answer that says
// nothing of the caller: a failure of the backend, or something that is no
// envelope.
func RefusesCaller(err error) bool {
	if unanswered(err) {
		return false
	}
	var perr *protocol.Error
	return errors.As(err, &perr) && (perr.Code == protocol.CodeUnauthorized || perr.Code == protocol.CodeUnauthenticated)
}

// oldBackendReason is the reason of a follow whose question to the backend
// the member joined, `team moved`, failed with err: that backend did not
// answer (unanswered), or it answered, and its answer was a refusal.
func oldBackendReason(err error) string {
	if unanswered(err) {
		return ReasonOldBackendFailed
	}
	return ReasonOldBackendRefused
}

// namesBack reports whether what the new backend records of the team is a
// move to the backend the checkout is pinned to. Then the two backends
// name each other: the one the member joined says that the team went to
// the one the file names, and that one says that the team went back. One
// of the two records is out of date, and nothing on this machine says
// which, so a session follows neither: a team file flipped between the
// two, by a commit or a branch, would otherwise send it back and forth.
// No command of Brigade's leaves two backends so (ADR 0010); this holds
// for records made any other way.
func namesBack(theirs *protocol.TeamMoveResult, pin *teamstore.Pin, teamRef string) bool {
	return theirs != nil && theirs.Moved && theirs.TeamRef == teamRef && theirs.Adapter == pin.Adapter && SameAddress(theirs.URL, pin.URL)
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
// member the file describes, "" when it did. A join that was not answered
// (unanswered) is no refusal: the backend said nothing of the ticket or of
// the member, so it is the backend that did not answer
// (ReasonNewBackendFailed), on a machine's first join there and on the join
// made under a membership it holds alike. Until this it was a join the
// backend refused, and a member who never held a membership on a server
// that was only down was told that the team file does not match (work item
// 341). Any other failure is the backend's answer, and stays a refusal.
func join(ctx context.Context, in Inputs, client Caller, key, ticket, label string) (string, error) {
	backend, err := json.Marshal(JoinBackend(in.File))
	if err != nil {
		return ReasonStore, err
	}
	req := &protocol.TeamJoinRequest{JoinSecret: ticket, HumanLabel: label, Backend: backend}
	env, err := client.Call(ctx, "team", "join", nil, req)
	if unanswered(err) {
		return ReasonNewBackendFailed, err
	}
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

// CarryLabelOrigin copies the record of where the member's label came from
// to the new credential directory, when the old one describes the label
// that was just sent: the label is the same text, from the same source,
// and `whoami` goes on saying where it came from. Best effort, as the
// record is everywhere: without it `whoami` says the origin is unrecorded.
// The follow calls it after its join, and `team move --to` after the
// administrator's take-in (work item 328), which sends the same label.
func CarryLabelOrigin(configDir, oldKey, newKey, label string, now time.Time, log *slog.Logger) {
	if label == "" {
		return
	}
	origin, err := teamstore.LoadLabelOrigin(configDir, oldKey)
	if err != nil || !origin.Describes(label) {
		return
	}
	carried := *origin
	carried.SentAt = now
	if err := write.LabelOrigin(configDir, newKey, carried); err != nil && log != nil {
		log.Debug("the label's record was not carried to the new backend's directory", slog.String("error", err.Error()))
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
