package syncthing

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

// The store layout (README, "What it stores"). The root holds one
// directory per team — the Syncthing folder — and the lock beside them,
// outside every folder: Syncthing replaces a file it syncs by rename, which
// would break a flock's identity, and the lock is this machine's alone.
const (
	lockName           = ".lock"
	membersDirName     = "members"
	revokedDirName     = "revoked"
	sessionsDirName    = "sessions"
	inboxDirName       = "inbox"
	ackedDirName       = "acked"
	idemDirName        = "idem"
	teamFileName       = "team.json"
	ignoreFileName     = ".stignore"
	credentialFileName = "credential.json" //nolint:gosec // G101: a file name, not a credential
	jsonExt            = ".json"
)

// The two statuses a member writes into its OWN file. A revocation is not
// a status: it is the creator's marker in revoked/ (revokedFile), so the
// creator never rewrites a member's file and no sync conflict can arise.
const (
	memberActive = "active"
	memberLeft   = "left"
)

const seqWidth = 19

// ignoreRules is the team folder's .stignore: the temporary files of the
// atomic writer, which Syncthing must never carry half-written, and the
// macOS Finder's sidecar.
const ignoreRules = "// Brigade's team folder: written by the syncthing adapter, read by every member's.\n.*.tmp-*\n.DS_Store\n"

// teamFile is <root>/<team_ref>/team.json. The creator writes it, at
// create, rotate and transfer, and nobody else. Only the secret's SHA-256
// is stored: it is what a join is checked against, and it is the key of
// the joining proof (proof.go), so a member — anyone holding the folder —
// can verify a joiner, and nobody outside can forge one.
type teamFile struct {
	TeamRef          string     `json:"team_ref"`
	TeamName         string     `json:"team_name"`
	JoinSecretSHA256 string     `json:"join_secret_sha256"`
	SecretVersion    int        `json:"secret_version"`
	SecretRotatedAt  *time.Time `json:"secret_rotated_at,omitzero"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
}

// memberFile is <root>/<team_ref>/members/<principal_ref>.json, written by
// that member alone. DeviceID is the member's Syncthing device, which every
// other member's watch adds to its own copy of the team folder.
// SecretVersion is the team's secret version when the join completed, 0
// while the team file has not yet reached this machine (completeJoin).
type memberFile struct {
	PrincipalRef  string    `json:"principal_ref"`
	HumanLabel    string    `json:"human_label,omitzero"`
	Status        string    `json:"status"`
	JoinedAt      time.Time `json:"joined_at"`
	DeviceID      string    `json:"device_id,omitzero"`
	SecretVersion int       `json:"secret_version"`
}

// revokedFile is <root>/<team_ref>/revoked/<principal_ref>.json: the
// creator's revocation of one member (team revoke-member). Ban keeps the
// member out of a rejoin with the current secret. Every member's reads
// filter on it at once; the member's device is dropped from the folder
// once the marker is revokeGrace old (engine.go), and a rejoin — a `team
// join` with the secret, or the member's device reconnecting with a proof
// made after RevokedAt — deletes a marker that is not a ban.
type revokedFile struct {
	PrincipalRef string    `json:"principal_ref"`
	DeviceID     string    `json:"device_id,omitzero"`
	RevokedAt    time.Time `json:"revoked_at"`
	Ban          bool      `json:"ban"`
	By           string    `json:"by"`
}

// sessionFile is <root>/<team_ref>/sessions/<session_id>.json, written by
// the owning principal alone. It carries no `state`: the state of 4.5.8 is
// computed at READ time from closed_at, lease_until and activity.
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
	BrigadeVersion     *string    `json:"brigade_version,omitzero"`
	SyncPeer           *string    `json:"sync_peer,omitzero"`
	CreatedAt          time.Time  `json:"created_at"`
	ClosedAt           *time.Time `json:"closed_at,omitzero"`
}

// credentialFile is the adapter's own credential in the profile
// directory. Its PRESENCE is the credential — this adapter authenticates
// nobody, it only remembers who it is. LastTeamRef keeps the team most
// recently left so a repeated `team leave` can still answer a non-empty
// team_ref. JoinDigest is the SHA-256 of the secret the last join
// presented, kept until the team file arrives and completeJoin checks it;
// never the secret itself (4.5.14).
type credentialFile struct {
	PrincipalRef string    `json:"principal_ref"`
	CreatedAt    time.Time `json:"created_at"`
	LastTeamRef  string    `json:"last_team_ref,omitzero"`
	JoinDigest   string    `json:"join_digest,omitzero"`
}

// idemRecord is <root>/<t>/idem/<sender>/<sha256(key)>.json (4.5.4).
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

	// seq is the sequence number from the file name, not a wire member.
	seq int64
}

// A store is the locked view of the teams root. now is a field so tests
// can drive lease expiry and the retention sweep without sleeping.
type store struct {
	root string
	now  func() time.Time
	lock *adapterkit.FileLock
}

// openStore creates the root 0700, takes the exclusive store lock and
// returns the store. Every command except `describe` runs inside one: the
// sessions of this machine run adapter processes concurrently, and one
// writer at a time on this machine is the whole local concurrency design.
// Across machines there is no lock: every path has one writer.
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
// component. Identifiers arrive from callers and from a rebound profile,
// so "..", a separator or a NUL would otherwise escape the store. A ref
// that fails here is simply not found.
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

// newJoinSecret mints `brg1.<team_ref>.<32 hex>` (4.4.10).
func newJoinSecret(teamRef string) (string, error) {
	half, err := newRef()
	if err != nil {
		return "", err
	}
	return protocol.JoinSecretPrefix + teamRef + "." + half, nil
}

// sha256hex is the one digest this adapter uses: for the stored join
// secret, the joining proof's key and the idempotency key's file name.
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Paths inside the store. Every caller has already run safeRef on the
// identifiers it interpolates.
func (s *store) teamDir(team string) string    { return filepath.Join(s.root, team) }
func (s *store) teamPath(team string) string   { return filepath.Join(s.teamDir(team), teamFileName) }
func (s *store) membersDir(t string) string    { return filepath.Join(s.teamDir(t), membersDirName) }
func (s *store) revokedDir(t string) string    { return filepath.Join(s.teamDir(t), revokedDirName) }
func (s *store) sessionsDir(t string) string   { return filepath.Join(s.teamDir(t), sessionsDirName) }
func (s *store) inboxDir(t, r string) string   { return filepath.Join(s.teamDir(t), inboxDirName, r) }
func (s *store) ackedDir(t, r string) string   { return filepath.Join(s.teamDir(t), ackedDirName, r) }
func (s *store) idemDir(t, sn string) string   { return filepath.Join(s.teamDir(t), idemDirName, sn) }
func (s *store) memberPath(t, p string) string { return filepath.Join(s.membersDir(t), p+jsonExt) }
func (s *store) revokedPath(t, p string) string {
	return filepath.Join(s.revokedDir(t), p+jsonExt)
}

func (s *store) sessionPath(t, id string) string {
	return filepath.Join(s.sessionsDir(t), id+jsonExt)
}

// teamExists reports whether the team's directory holds a team file: the
// folder has reached this machine (or was created here).
func (s *store) teamExists(team string) bool {
	if !safeRef(team) {
		return false
	}
	_, err := os.Lstat(s.teamPath(team))
	return err == nil
}

// readJSON reads one store file. A missing file comes back as the
// underlying *fs.PathError so errors.Is(err, fs.ErrNotExist) holds.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path built from safeRef-checked identifiers under the store
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errInternal("a store file is not a valid JSON document")
	}
	return nil
}

// writeJSON writes one store file atomically, 0600, creating its 0700
// directory chain. The atomic writer's temporary file matches the team
// folder's .stignore, so Syncthing never carries a half-written file.
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

// ensureIgnore writes the team folder's .stignore when it is missing.
func (s *store) ensureIgnore(team string) error {
	path := filepath.Join(s.teamDir(team), ignoreFileName)
	if _, err := os.Lstat(path); err == nil {
		return nil
	}
	if err := adapterkit.MkdirPrivate(s.teamDir(team)); err != nil {
		return errInternal("a store directory could not be created")
	}
	return adapterkit.WriteAtomic(path, []byte(ignoreRules))
}

// loadTeam reads a team record. ok is false when the team file is not
// here: the team does not exist, or its folder has not arrived yet.
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

// loadRevoked reads the creator's revocation marker for a principal. ok is
// false when there is none.
func (s *store) loadRevoked(team, principal string) (*revokedFile, bool, error) {
	if !safeRef(team) || !safeRef(principal) {
		return nil, false, nil
	}
	var r revokedFile
	err := readJSON(s.revokedPath(team, principal), &r)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// revokedMarkers reads every revocation marker of a team.
func (s *store) revokedMarkers(team string) ([]revokedFile, error) {
	principals, err := jsonStems(s.revokedDir(team))
	if err != nil {
		return nil, err
	}
	out := make([]revokedFile, 0, len(principals))
	for _, p := range principals {
		r, ok, err := s.loadRevoked(team, p)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *r)
		}
	}
	return out, nil
}

// activeMembers reads every active membership of a team: the member's own
// file says active and the creator holds no revocation marker for it.
func (s *store) activeMembers(team string) ([]memberFile, error) {
	principals, err := jsonStems(s.membersDir(team))
	if err != nil {
		return nil, err
	}
	out := make([]memberFile, 0, len(principals))
	for _, p := range principals {
		m, ok, err := s.loadMember(team, p)
		if err != nil {
			return nil, err
		}
		if !ok || m.Status != memberActive {
			continue
		}
		if _, revoked, err := s.loadRevoked(team, p); err != nil {
			return nil, err
		} else if revoked {
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}

// adoptHumanLabel fills an EMPTY membership label from the label a
// registration carried (4.4.2, capability session.human_label, C-45). A
// membership that already has a label keeps it.
func (s *store) adoptHumanLabel(team, principal string, offered *string) error {
	m, ok, err := s.loadMember(team, principal)
	if err != nil || !ok {
		return err
	}
	if m.HumanLabel != "" || offered == nil || *offered == "" {
		return nil
	}
	m.HumanLabel = *offered
	return writeJSON(s.memberPath(team, principal), m)
}

// memberActiveIn reports whether the principal is an active member: its
// own file says active and the creator has not revoked it.
func (s *store) memberActiveIn(team, principal string) (bool, error) {
	m, ok, err := s.loadMember(team, principal)
	if err != nil || !ok {
		return false, err
	}
	if m.Status != memberActive {
		return false, nil
	}
	_, revoked, err := s.loadRevoked(team, principal)
	if err != nil {
		return false, err
	}
	return !revoked, nil
}

// requireMembership is the 4.5.7 authorisation check: the principal must
// be an active member of the team. The refusal is one fixed message with
// no details, byte-identical for a missing team, a missing membership and
// a revoked one — there is no team-existence oracle (C-26, C-43).
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
// 4.5.6/4.5.7.
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
// recipient: it must exist in this team AND be owned by a principal that
// is still an ACTIVE member (4.5.6; a revoked member's sessions are
// outside the team). Every refusal is the uniform not_found.
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
// removed, skipping anything else: the lock, Syncthing's marker, a
// conflict copy (its stem carries dots, which safeRef refuses).
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

// teamRefs lists the team directories of the root: every entry that is a
// usable identifier (the lock and Syncthing's files are not).
func (s *store) teamRefs() ([]string, error) {
	names, err := listNames(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if safeRef(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// messageIDOf reads the message id out of a `<seq>.<message_id>.json`
// file name. ok is false for anything else, a sync-conflict copy included.
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
// larger. Under the store lock that is monotonic on this machine; two
// senders on two machines may pick the same number for one recipient, and
// the random message id after it keeps their files apart.
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
// (4.5.10).
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
// directory.
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

// sessionState computes the 4.5.8 state at READ time, on this machine's
// clock: offline when the session is closed or its lease has run out,
// active when the lease is valid and the harness last reported busy, idle
// otherwise.
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
// is read from the member file at read time.
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
		BrigadeVersion:     f.BrigadeVersion,
		SyncPeer:           f.SyncPeer,
		CreatedAt:          f.CreatedAt,
		IsSelf:             isSelf,
	}, nil
}

// sweep applies the retention floors of 4.5.9 to the whole root. It runs
// under the lock at the start of every command except `describe`, and does
// nothing at all when no team has ever been created. Every member sweeps
// its own replica; a deletion reaches the others through Syncthing, and
// two deletions of one file never conflict.
func (s *store) sweep(ret protocol.Retention) error {
	teams, err := s.teamRefs()
	if err != nil || len(teams) == 0 {
		return err
	}
	now := s.now()
	for _, team := range teams {
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
// retention.acked_message_seconds and unacknowledged ones older than
// retention.unacked_message_seconds.
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
			if !safeRef(r) {
				continue
			}
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
// an active member.
func (s *store) sessionsOfTeam(team string) ([]*sessionFile, error) {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return nil, err
	}
	out := make([]*sessionFile, 0, len(ids))
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
		out = append(out, f)
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
			if !safeRef(r) {
				continue
			}
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
// inbox and everything it has already acknowledged, in send order.
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

// closeOwnSessions sets closed_at on every open session of a principal:
// the member's own files, for `team leave` and `profile reset`.
func (s *store) closeOwnSessions(team, principal string) (int, error) {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return 0, err
	}
	now := s.now().UTC()
	closed := 0
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil {
			return 0, err
		}
		if !ok || f.PrincipalRef != principal || f.ClosedAt != nil {
			continue
		}
		f.ClosedAt = &now
		if err := s.saveSession(team, f); err != nil {
			return 0, err
		}
		closed++
	}
	return closed, nil
}

// memberActivity reports the most recent last_seen_at over a principal's
// sessions in this team, in ANY state (nil when it has none), and how many
// of those sessions are not offline (4.4.10 `team members`).
func (s *store) memberActivity(team, principal string, now time.Time) (*time.Time, int, error) {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return nil, 0, err
	}
	var last *time.Time
	count := 0
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil {
			return nil, 0, err
		}
		if !ok || f.PrincipalRef != principal {
			continue
		}
		if last == nil || f.LastSeenAt.After(*last) {
			seen := f.LastSeenAt
			last = &seen
		}
		if sessionState(f, now) != protocol.SessionStateOffline {
			count++
		}
	}
	return last, count, nil
}

// ackMessages implements 4.4.8: an owned pending message moves to acked
// with its timestamp, an already-acknowledged one is acked again, and an
// id that is not there is unknown, never an error (C-30).
func ackMessages(s *store, team, session string, ids []string) (*protocol.AckResult, error) {
	out := &protocol.AckResult{Acked: []string{}, Unknown: []string{}}
	for _, id := range ids {
		dir, name, ok, err := s.findMessageFile(team, session, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			out.Unknown = append(out.Unknown, id)
			continue
		}
		if dir == s.inboxDir(team, session) {
			if err := s.moveToAcked(team, session, name); err != nil {
				return nil, err
			}
		}
		out.Acked = append(out.Acked, id)
	}
	return out, nil
}

// moveToAcked rewrites the message under acked/ with acked_at and removes
// it from the inbox: the recipient's move, which the sender never touches
// again, so the two replicas agree on it.
func (s *store) moveToAcked(team, session, name string) error {
	src := filepath.Join(s.inboxDir(team, session), name)
	var m storedMessage
	if err := readJSON(src, &m); err != nil {
		return err
	}
	now := s.now().UTC()
	m.AckedAt = &now
	if err := writeJSON(filepath.Join(s.ackedDir(team, session), name), &m); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return errInternal("an acknowledged message could not be removed from the inbox")
	}
	return nil
}

// checkUnackedCaps applies the two unacknowledged caps of 4.5.12 in the
// order the spec fixes: the per-pair cap before the recipient-wide one, so
// one sender cannot exhaust a recipient's inbox for everyone else (C-28).
func checkUnackedCaps(pending []storedMessage, senderSessionID string, limits protocol.Limits) error {
	n := 0
	for i := range pending {
		if pending[i].Sender.SessionID == senderSessionID {
			n++
		}
	}
	if n >= limits.MaxUnackedPerSenderRecipient {
		return errRateLimited(reasonSenderQuotaForRecipient, capRetryAfterMS)
	}
	if len(pending) >= limits.MaxUnackedPerRecipient {
		return errRateLimited(reasonRecipientInboxFull, capRetryAfterMS)
	}
	return nil
}
