// Package fs implements the Brigade filesystem adapter: a second BAP/1
// implementation whose backend is a directory on disk.
//
// It exists to prove the plugin carries no Supabase assumption, to run the
// conformance suite of plan 9.2 in seconds, and to be the harness's test
// fixture. It is INSECURE and TEST-ONLY: every team's state lives in one
// tree with no access control beyond file modes, so any process that can
// read the root can read every team's messages. Nothing ships it.
//
// The wire contract is docs/protocol-v1.md (BAP/1, frozen); section numbers
// in the comments below are its. Everything the protocol leaves to the
// adapter — the store layout, the credential file, identifier shapes — is
// this package's own and is described in README.md.
package fs

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The store layout under <root>. Every directory is 0700 and every file
// 0600; see README.md for the tree.
const (
	lockName        = ".lock"
	teamsDirName    = "teams"
	membersDirName  = "members"
	sessionsDirName = "sessions"
	inboxDirName    = "inbox"
	ackedDirName    = "acked"
	idemDirName     = "idem"
	teamFileName    = "team.json"
	//nolint:gosec // G101: this is a FILE NAME, not a credential; the file's content is the credential
	credentialFileName = "credential.json"
	jsonExt            = ".json"
)

// Membership statuses. `team leave` and an administrator's revocation flip
// a member to revoked; only an active member may touch the team (4.5.7).
const (
	memberActive  = "active"
	memberRevoked = "revoked"
)

// seqWidth is the width of the zero-padded decimal that makes lexical
// order equal send order inside one recipient's inbox. Nineteen digits
// hold any int64, so a padded value never overflows into a wider string
// and the lexical order can never disagree with the numeric one.
const seqWidth = 19

// teamFile is <root>/teams/<team_ref>/team.json. The join secret itself is
// never stored: only the sha256 of the WHOLE `brg1.…` string, so a reader
// of the store cannot rejoin with it (4.5.14).
type teamFile struct {
	TeamRef         string    `json:"team_ref"`
	TeamName        string    `json:"team_name"`
	JoinSecretSHA26 string    `json:"join_secret_sha256"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
}

// memberFile is <root>/teams/<team_ref>/members/<principal_ref>.json.
type memberFile struct {
	PrincipalRef string    `json:"principal_ref"`
	HumanLabel   string    `json:"human_label,omitzero"`
	Status       string    `json:"status"`
	JoinedAt     time.Time `json:"joined_at"`
}

// sessionFile is <root>/teams/<team_ref>/sessions/<session_id>.json. It
// carries no `state`: the state of 4.5.8 is computed at READ time from
// closed_at, lease_until and activity, so a stored value could never go
// stale.
//
// Model and ContextUsedTokens are the two facts the owning harness derives
// from its own transcript (4.4.2, 4.4.4; capabilities session.model and
// session.context_used_tokens, C-44). They are stored exactly as reported
// — unverified text and a count, absent until a harness reports one — and
// the transcript and its path never reach this adapter at all (T10).
type sessionFile struct {
	SessionID          string     `json:"session_id"`
	SessionName        string     `json:"session_name"`
	SessionDescription *string    `json:"session_description,omitzero"`
	PrincipalRef       string     `json:"principal_ref"`
	Activity           string     `json:"activity"`
	Inbound            string     `json:"inbound"`
	LeaseSeconds       int        `json:"lease_seconds"`
	LeaseUntil         time.Time  `json:"lease_until"`
	LastSeenAt         time.Time  `json:"last_seen_at"`
	Harness            string     `json:"harness,omitzero"`
	HarnessVersion     string     `json:"harness_version,omitzero"`
	WorkspaceLabel     *string    `json:"workspace_label,omitzero"`
	Model              *string    `json:"model,omitzero"`
	ContextUsedTokens  *int       `json:"context_used_tokens,omitzero"`
	CreatedAt          time.Time  `json:"created_at"`
	ClosedAt           *time.Time `json:"closed_at,omitzero"`
}

// credentialFile is the adapter's own credential, beside team.json.
// Its PRESENCE is the credential — this adapter authenticates nobody, it
// only remembers who it is. LastTeamRef keeps the team most recently left
// so a repeated `team leave` can still answer a non-empty team_ref, which
// protocol.TeamLeaveResult.Validate requires.
type credentialFile struct {
	PrincipalRef string    `json:"principal_ref"`
	CreatedAt    time.Time `json:"created_at"`
	LastTeamRef  string    `json:"last_team_ref,omitzero"`
}

// idemRecord is <root>/teams/<t>/idem/<sender>/<sha256(key)>.json (4.5.4).
// It carries the whole SendResponse of the original message, not just its
// id: a duplicate send MUST answer with the ORIGINAL response, and the
// message it names may by then have been acknowledged and swept, so the
// answer cannot be reconstructed by reading it back.
type idemRecord struct {
	MessageID          string    `json:"message_id"`
	Fingerprint        string    `json:"fingerprint"`
	RecipientSessionID string    `json:"recipient_session_id"`
	HopCount           int       `json:"hop_count"`
	CreatedAt          time.Time `json:"created_at"`
}

// storedMessage is a protocol.MessageEnvelope plus the acknowledgement
// timestamp the sweep keys on. json/v2 inlines the embedded struct, so the
// file IS the envelope with one extra member.
type storedMessage struct {
	protocol.MessageEnvelope
	AckedAt *time.Time `json:"acked_at,omitzero"`

	// seq is the sequence number from the file name, not a wire member:
	// it is unexported, so the codec never sees it. It is what makes
	// "the most recent message" exact even when two messages share a
	// created_at, which a test with a frozen clock always produces.
	seq int64
}

// A store is the locked view of one root directory. now is a field so
// tests can drive lease expiry and the retention sweep without sleeping.
type store struct {
	root string
	now  func() time.Time
	lock *adapterkit.FileLock
}

// openStore creates the root 0700, takes the exclusive store lock and
// returns the store. Every command except `describe` runs inside one:
// the conformance suite and the harness run adapter processes
// concurrently, and one writer at a time is the whole concurrency design.
func openStore(root string, now func() time.Time) (*store, error) {
	if err := adapterkit.MkdirPrivate(root); err != nil {
		return nil, errInternal("the adapter root could not be created")
	}
	lock, err := adapterkit.LockFile(filepath.Join(root, lockName), adapterkit.DefaultLockTimeout)
	if err != nil {
		return nil, err
	}
	return &store{root: root, now: now, lock: lock}, nil
}

// close releases the store lock. Calling it twice is safe.
func (s *store) close() {
	if s.lock != nil {
		_ = s.lock.Unlock()
	}
}

// safeRef reports whether an opaque identifier may be used as a path
// component. Identifiers are opaque (4.8) and arrive from callers and from
// a rebound team.json, so "..", a separator or a NUL would otherwise
// escape the store. A ref that fails here is simply not found.
func safeRef(ref string) bool {
	if ref == "" || len(ref) > 64 {
		return false
	}
	for i := range len(ref) {
		c := ref[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// newRef mints an opaque identifier: 16 random bytes as lowercase hex.
func newRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errInternal("the adapter could not read random bytes")
	}
	return hex.EncodeToString(b[:]), nil
}

// newJoinSecret mints `brg1.<team_ref>.<32 hex>` (4.4.10, D5).
func newJoinSecret(teamRef string) (string, error) {
	half, err := newRef()
	if err != nil {
		return "", err
	}
	return protocol.JoinSecretPrefix + teamRef + "." + half, nil
}

// sha256hex is the one digest this adapter uses: for the stored join
// secret and for the idempotency key's file name.
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Paths inside the store. Every caller has already run safeRef on the
// identifiers it interpolates.
func (s *store) teamsDir() string            { return filepath.Join(s.root, teamsDirName) }
func (s *store) teamDir(team string) string  { return filepath.Join(s.teamsDir(), team) }
func (s *store) teamPath(team string) string { return filepath.Join(s.teamDir(team), teamFileName) }
func (s *store) membersDir(t string) string  { return filepath.Join(s.teamDir(t), membersDirName) }
func (s *store) sessionsDir(t string) string { return filepath.Join(s.teamDir(t), sessionsDirName) }
func (s *store) inboxDir(t, r string) string { return filepath.Join(s.teamDir(t), inboxDirName, r) }
func (s *store) ackedDir(t, r string) string { return filepath.Join(s.teamDir(t), ackedDirName, r) }
func (s *store) idemDir(t, sn string) string { return filepath.Join(s.teamDir(t), idemDirName, sn) }
func (s *store) memberPath(t, p string) string {
	return filepath.Join(s.membersDir(t), p+jsonExt)
}

func (s *store) sessionPath(t, id string) string {
	return filepath.Join(s.sessionsDir(t), id+jsonExt)
}

// readJSON reads one store file. A missing file comes back as the
// underlying *fs.PathError so errors.Is(err, fs.ErrNotExist) holds.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errInternal("a store file is not a valid JSON document")
	}
	return nil
}

// writeJSON writes one store file atomically, 0600, creating its 0700
// directory chain.
func writeJSON(path string, v any) error {
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return errInternal("a store directory could not be created")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return errInternal("a store record could not be encoded")
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// loadTeam reads a team record. ok is false when the team does not exist.
func (s *store) loadTeam(team string) (*teamFile, bool, error) {
	if !safeRef(team) {
		return nil, false, nil
	}
	var t teamFile
	err := readJSON(s.teamPath(team), &t)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &t, true, nil
}

// loadMember reads a membership record. ok is false when there is none.
func (s *store) loadMember(team, principal string) (*memberFile, bool, error) {
	if !safeRef(team) || !safeRef(principal) {
		return nil, false, nil
	}
	var m memberFile
	err := readJSON(s.memberPath(team, principal), &m)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &m, true, nil
}

// memberActiveIn reports whether the principal is an active member.
func (s *store) memberActiveIn(team, principal string) (bool, error) {
	m, ok, err := s.loadMember(team, principal)
	if err != nil || !ok {
		return false, err
	}
	return m.Status == memberActive, nil
}

// requireMembership is the 4.5.7 authorisation check: the team must exist
// and the principal must be an active member of it. The refusal is one
// fixed message with no details, byte-identical for a missing team, a
// missing membership and a revoked one — there is no team-existence
// oracle (C-26, C-43).
func (s *store) requireMembership(team, principal string) error {
	active, err := s.memberActiveIn(team, principal)
	if err != nil {
		return err
	}
	if !active {
		return errNotMember()
	}
	return nil
}

// loadSession reads a session of a team. ok is false when the id names no
// session of that team.
func (s *store) loadSession(team, id string) (*sessionFile, bool, error) {
	if !safeRef(team) || !safeRef(id) {
		return nil, false, nil
	}
	var f sessionFile
	err := readJSON(s.sessionPath(team, id), &f)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &f, true, nil
}

// ownedSession resolves a session that must exist in this team AND be
// owned by this principal. Every other outcome is the uniform not_found of
// 4.5.6/4.5.7: unknown, foreign-team and not-owned are one answer (C-13,
// C-19, C-24, C-25, C-26, C-29, C-37).
func (s *store) ownedSession(team, principal, id string) (*sessionFile, error) {
	f, ok, err := s.loadSession(team, id)
	if err != nil {
		return nil, err
	}
	if !ok || f.PrincipalRef != principal {
		return nil, errNotFound()
	}
	return f, nil
}

// requireRecipient resolves the session a `message send` names as its
// recipient. It must exist in this team AND be owned by a principal that
// is still an ACTIVE member: 4.5.6 says a session outside the team cannot
// be messaged, and a revoked member's sessions are outside the team by the
// same rule that already hides them from `session list` (C-08) — messaging
// one would park the message in an inbox nobody may ever drain. Every
// refusal is the uniform not_found of 4.5.6/4.5.7, byte-identical to an id
// that never existed, so a caller cannot learn that a session it may no
// longer see is still on disk (C-25, C-26).
func (s *store) requireRecipient(team, id string) error {
	f, ok, err := s.loadSession(team, id)
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound()
	}
	active, err := s.memberActiveIn(team, f.PrincipalRef)
	if err != nil {
		return err
	}
	if !active {
		return errNotFound()
	}
	return nil
}

func (s *store) saveSession(team string, f *sessionFile) error {
	return writeJSON(s.sessionPath(team, f.SessionID), f)
}

// listNames returns the sorted entry names of a directory; a missing
// directory is an empty list, which is what every caller means by it.
func listNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errInternal("a store directory could not be read")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}

// jsonStems returns the entry names of a directory with the .json suffix
// removed, skipping anything else (a lock sidecar, a stray file).
func jsonStems(dir string) ([]string, error) {
	names, err := listNames(dir)
	if err != nil {
		return nil, err
	}
	stems := make([]string, 0, len(names))
	for _, n := range names {
		if stem, ok := strings.CutSuffix(n, jsonExt); ok && safeRef(stem) {
			stems = append(stems, stem)
		}
	}
	return stems, nil
}

// messageIDOf reads the message id out of a `<seq>.<message_id>.json`
// file name. ok is false for anything else.
func messageIDOf(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, jsonExt)
	if !ok {
		return "", false
	}
	_, id, ok := strings.Cut(stem, ".")
	if !ok || !safeRef(id) {
		return "", false
	}
	return id, true
}

// seqOf reads the sequence number out of a message file name.
func seqOf(name string) (int64, bool) {
	seq, _, ok := strings.Cut(name, ".")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(seq, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// nextSeq allocates the next sequence number for one recipient: the wall
// clock in nanoseconds, or one past the highest already used, whichever is
// larger. Under the store lock that is monotonic across acknowledgements
// (an acked file keeps its seq) and across deletions.
func (s *store) nextSeq(team, recipient string) (string, error) {
	high := s.now().UnixNano()
	for _, dir := range []string{s.inboxDir(team, recipient), s.ackedDir(team, recipient)} {
		names, err := listNames(dir)
		if err != nil {
			return "", err
		}
		for _, n := range names {
			if v, ok := seqOf(n); ok && v >= high {
				high = v + 1
			}
		}
	}
	digits := strconv.FormatInt(high, 10)
	if len(digits) < seqWidth {
		digits = strings.Repeat("0", seqWidth-len(digits)) + digits
	}
	return digits, nil
}

// inboxMessages reads one recipient's pending messages, oldest first
// (4.5.10: adapters SHOULD return catch-up batches oldest first).
func (s *store) inboxMessages(team, recipient string) ([]storedMessage, error) {
	return s.messagesIn(s.inboxDir(team, recipient))
}

func (s *store) messagesIn(dir string) ([]storedMessage, error) {
	names, err := listNames(dir)
	if err != nil {
		return nil, err
	}
	out := make([]storedMessage, 0, len(names))
	for _, n := range names {
		if _, ok := messageIDOf(n); !ok {
			continue
		}
		var m storedMessage
		if err := readJSON(filepath.Join(dir, n), &m); err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		m.seq, _ = seqOf(n)
		out = append(out, m)
	}
	return out, nil
}

// findMessageFile locates one message id in a recipient's inbox or acked
// directory. It is shared by the real ack routine and its mutant twin.
func (s *store) findMessageFile(team, recipient, id string) (dir, name string, ok bool, err error) {
	if !safeRef(id) {
		return "", "", false, nil
	}
	for _, d := range []string{s.inboxDir(team, recipient), s.ackedDir(team, recipient)} {
		names, lerr := listNames(d)
		if lerr != nil {
			return "", "", false, lerr
		}
		for _, n := range names {
			if got, valid := messageIDOf(n); valid && got == id {
				return d, n, true, nil
			}
		}
	}
	return "", "", false, nil
}

// sessionState computes the 4.5.8 state at READ time: offline when the
// session is closed or its lease has run out, active when the lease is
// valid and the harness last reported busy, idle otherwise.
func sessionState(f *sessionFile, now time.Time) string {
	if f.ClosedAt != nil || now.After(f.LeaseUntil) {
		return protocol.SessionStateOffline
	}
	if f.Activity == protocol.ActivityBusy {
		return protocol.SessionStateActive
	}
	return protocol.SessionStateIdle
}

// record renders a session as the SessionRecord of 4.4.3. The human label
// is read from the member file at read time, so a label changed by a later
// `team join` shows up everywhere at once. model and context_used_tokens
// pass straight through and stay absent until the owning harness reports
// them (C-44); this adapter never derives, checks or ages them.
func (s *store) record(team string, f *sessionFile, isSelf bool) (protocol.SessionRecord, error) {
	label := ""
	if m, ok, err := s.loadMember(team, f.PrincipalRef); err != nil {
		return protocol.SessionRecord{}, err
	} else if ok {
		label = m.HumanLabel
	}
	return protocol.SessionRecord{
		SessionID:          f.SessionID,
		SessionName:        f.SessionName,
		SessionDescription: f.SessionDescription,
		PrincipalRef:       f.PrincipalRef,
		HumanLabel:         label,
		State:              sessionState(f, s.now()),
		Activity:           f.Activity,
		Inbound:            f.Inbound,
		LastSeenAt:         f.LastSeenAt,
		LeaseUntil:         f.LeaseUntil,
		Harness:            f.Harness,
		HarnessVersion:     f.HarnessVersion,
		WorkspaceLabel:     f.WorkspaceLabel,
		Model:              f.Model,
		ContextUsedTokens:  f.ContextUsedTokens,
		CreatedAt:          f.CreatedAt,
		IsSelf:             isSelf,
	}, nil
}

// sweep applies the retention floors of 4.5.9 to the whole store. It runs
// under the lock at the start of every command except `describe`, and does
// nothing at all when no team has ever been created — `describe` must not
// create the root, and neither may a sweep that has nothing to sweep.
func (s *store) sweep(ret protocol.Retention) error {
	teams, err := listNames(s.teamsDir())
	if err != nil || len(teams) == 0 {
		return err
	}
	now := s.now()
	for _, team := range teams {
		if !safeRef(team) {
			continue
		}
		if err := s.sweepSessions(team, now, ret); err != nil {
			return err
		}
		if err := s.sweepMessages(team, now, ret); err != nil {
			return err
		}
	}
	return nil
}

// sweepSessions deletes a session that has been offline for longer than
// retention.closed_session_seconds, together with its inbox, its acked
// messages and its idempotency records.
func (s *store) sweepSessions(team string, now time.Time, ret protocol.Retention) error {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return err
	}
	horizon := time.Duration(ret.ClosedSessionSeconds) * time.Second
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil || !ok {
			continue
		}
		since := f.LeaseUntil
		if f.ClosedAt != nil {
			since = *f.ClosedAt
		}
		if sessionState(f, now) != protocol.SessionStateOffline || now.Sub(since) <= horizon {
			continue
		}
		for _, dir := range []string{s.inboxDir(team, id), s.ackedDir(team, id), s.idemDir(team, id)} {
			if err := os.RemoveAll(dir); err != nil {
				return errInternal("a retention sweep could not remove a directory")
			}
		}
		if err := os.Remove(s.sessionPath(team, id)); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return errInternal("a retention sweep could not remove a session")
		}
	}
	return nil
}

// sweepMessages deletes acknowledged messages older than
// retention.acked_message_seconds (a MAY of 4.5.9) and unacknowledged ones
// older than retention.unacked_message_seconds (the floor, so this deletes
// nothing a conforming consumer could still be waiting for).
func (s *store) sweepMessages(team string, now time.Time, ret protocol.Retention) error {
	for _, spec := range []struct {
		dir     string
		horizon time.Duration
		acked   bool
	}{
		{ackedDirName, time.Duration(ret.AckedMessageSeconds) * time.Second, true},
		{inboxDirName, time.Duration(ret.UnackedMessageSeconds) * time.Second, false},
	} {
		base := filepath.Join(s.teamDir(team), spec.dir)
		recipients, err := listNames(base)
		if err != nil {
			return err
		}
		for _, r := range recipients {
			if err := s.sweepOneBox(filepath.Join(base, r), now, spec.horizon, spec.acked); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *store) sweepOneBox(dir string, now time.Time, horizon time.Duration, acked bool) error {
	names, err := listNames(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := messageIDOf(n); !ok {
			continue
		}
		path := filepath.Join(dir, n)
		var m storedMessage
		if err := readJSON(path, &m); err != nil {
			continue
		}
		since := m.CreatedAt
		if acked {
			if m.AckedAt == nil {
				continue
			}
			since = *m.AckedAt
		}
		if now.Sub(since) <= horizon {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return errInternal("a retention sweep could not remove a message")
		}
	}
	return nil
}

// sessionsOfTeam reads every session of one team whose principal is still
// an active member. Both halves of the store_list.go twin pair call it, so
// the mutation between them is the TEAM SCOPE and nothing else.
func (s *store) sessionsOfTeam(team string) ([]teamSession, error) {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return nil, err
	}
	out := make([]teamSession, 0, len(ids))
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		active, err := s.memberActiveIn(team, f.PrincipalRef)
		if err != nil {
			return nil, err
		}
		if !active {
			continue
		}
		out = append(out, teamSession{teamRef: team, file: f})
	}
	return out, nil
}

// teamMessages reads every message the team still holds, pending and
// acknowledged. The rate limits of 4.5.12 are counted over it.
func (s *store) teamMessages(team string) ([]storedMessage, error) {
	var out []storedMessage
	for _, box := range []string{inboxDirName, ackedDirName} {
		base := filepath.Join(s.teamDir(team), box)
		recipients, err := listNames(base)
		if err != nil {
			return nil, err
		}
		for _, r := range recipients {
			found, err := s.messagesIn(filepath.Join(base, r))
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	return out, nil
}

// receivedBy reads every message one session has received: its pending
// inbox and everything it has already acknowledged. `reply_to` must name a
// message the sender RECEIVED (4.5.12), and an acknowledged message still
// counts.
func (s *store) receivedBy(team, session string) ([]storedMessage, error) {
	pending, err := s.inboxMessages(team, session)
	if err != nil {
		return nil, err
	}
	acked, err := s.messagesIn(s.ackedDir(team, session))
	if err != nil {
		return nil, err
	}
	all := slices.Concat(pending, acked)
	slices.SortFunc(all, func(a, b storedMessage) int { return cmp.Compare(a.seq, b.seq) })
	return all, nil
}
