package protocol

import (
	"encoding/json/jsontext"
	"strconv"
	"time"
	"unicode/utf8"
)

// TeamCreateRequest is the `team create` stdin document (4.4.10).
type TeamCreateRequest struct {
	TeamName   string `json:"team_name"`
	HumanLabel string `json:"human_label,omitzero"`
}

// Validate implements Validator. The team name has its own cap,
// MaxTeamNameCodepoints, published as limits.max_team_name_codepoints
// (P1-4 decision 1); it is not the session-name cap.
func (r *TeamCreateRequest) Validate() error {
	if err := requireString("team_name", r.TeamName); err != nil {
		return err
	}
	if err := capRunes("team_name", r.TeamName, MaxTeamNameCodepoints); err != nil {
		return err
	}
	return optionalText("human_label", r.HumanLabel, MaxHumanLabelChars)
}

// TeamCreateResult is the `team create` result (4.4.10): the only command
// output in the whole protocol that carries a secret, shown once (4.5.14).
type TeamCreateResult struct {
	TeamRef      string `json:"team_ref"`
	TeamName     string `json:"team_name"`
	JoinSecret   string `json:"join_secret"`
	PrincipalRef string `json:"principal_ref"`
}

// Validate implements Validator.
func (r *TeamCreateResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("team_name", r.TeamName); err != nil {
		return err
	}
	if err := capRunes("team_name", r.TeamName, MaxTeamNameCodepoints); err != nil {
		return err
	}
	if err := requireString("join_secret", r.JoinSecret); err != nil {
		return err
	}
	return requireString("principal_ref", r.PrincipalRef)
}

// TeamJoinRequest is the `team join` stdin document (4.4.10). Backend is
// adapter-specific (Supabase: {url, publishable_key}) and accepted only
// when the profile has no backend configured; the protocol layer checks
// only that it is a JSON object.
type TeamJoinRequest struct {
	JoinSecret string         `json:"join_secret"`
	HumanLabel string         `json:"human_label,omitzero"`
	Backend    jsontext.Value `json:"backend,omitzero"`
}

// Validate implements Validator. Whether the secret itself is well-formed
// (`brg1.<team_ref>.…`) is the join-secret parser's business, not this
// shape's; a malformed secret still maps to invalid_input there (4.6).
func (r *TeamJoinRequest) Validate() error {
	if err := requireString("join_secret", r.JoinSecret); err != nil {
		return err
	}
	if err := optionalText("human_label", r.HumanLabel, MaxHumanLabelChars); err != nil {
		return err
	}
	if len(r.Backend) > 0 && r.Backend.Kind() != '{' {
		return &Error{
			Code:    CodeInvalidInput,
			Message: "backend must be a JSON object",
			Details: map[string]string{"field": "backend", "reason": reasonInvalidValue},
		}
	}
	return nil
}

// TeamJoinResult is the `team join` result (4.4.10). Rejoined true means
// the secret named the team the profile was already bound to and the same
// membership was re-activated (C-08).
type TeamJoinResult struct {
	TeamRef      string `json:"team_ref"`
	TeamName     string `json:"team_name"`
	PrincipalRef string `json:"principal_ref"`
	Rejoined     bool   `json:"rejoined"`
}

// Validate implements Validator.
func (r *TeamJoinResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("team_name", r.TeamName); err != nil {
		return err
	}
	if err := capRunes("team_name", r.TeamName, MaxTeamNameCodepoints); err != nil {
		return err
	}
	return requireString("principal_ref", r.PrincipalRef)
}

// TeamLeaveResult is the `team leave` result (4.4.10). Left is always
// true — the command is idempotent and success is the only non-error
// outcome.
type TeamLeaveResult struct {
	TeamRef      string `json:"team_ref"`
	PrincipalRef string `json:"principal_ref"`
	Left         bool   `json:"left"`
}

// Validate implements Validator.
func (r *TeamLeaveResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("principal_ref", r.PrincipalRef); err != nil {
		return err
	}
	if !r.Left {
		return errInvalidValue("left", "true")
	}
	return nil
}

// Team administration (4.4.10, capability team.admin; shapes fixed
// 2026-11-12, work item 274): `team rotate-secret`, `team revoke-member`
// and `team transfer`, which answer the team's owner only.

// MaxSecretVersion bounds joined_secret_version_lte and the max_version
// it is answered with (4.4.10): a secret version fits a 32-bit signed
// integer on every backend.
const MaxSecretVersion = 2147483647

// The two statuses a `team revoke-member` result of the principal form
// answers (4.4.10): what the request asked for.
const (
	MembershipRevoked = "revoked"
	MembershipBanned  = "banned"
)

// TeamRotateSecretResult is the `team rotate-secret` result (4.4.10). It
// has no join_secret member: the new secret goes to the file SecretFile
// names and to no output, because `team create` is the only command whose
// output may carry a secret (4.5.14).
type TeamRotateSecretResult struct {
	TeamRef       string    `json:"team_ref"`
	TeamName      string    `json:"team_name"`
	SecretVersion int       `json:"secret_version"`
	RotatedAt     time.Time `json:"rotated_at"`
	SecretFile    string    `json:"secret_file"`
}

// Validate implements Validator. SecretVersion counts the team's secrets
// from 1, the secret `team create` printed, so a rotation answers 2 or
// more.
func (r *TeamRotateSecretResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("team_name", r.TeamName); err != nil {
		return err
	}
	if err := capRunes("team_name", r.TeamName, MaxTeamNameCodepoints); err != nil {
		return err
	}
	if r.SecretVersion < 2 || r.SecretVersion > MaxSecretVersion {
		return errOutOfRange("secret_version", 2, MaxSecretVersion)
	}
	if err := requireTime("rotated_at", r.RotatedAt); err != nil {
		return err
	}
	return requireString("secret_file", r.SecretFile)
}

// TeamRevokeMemberRequest is the `team revoke-member` stdin document
// (4.4.10), one of two forms: {principal_ref, ban?} ends one member's
// membership, and with ban blocks its rejoin; {joined_secret_version_lte}
// revokes every active member that joined with a secret of that version
// or a lower one, except the owner, and never bans. The version is a
// pointer because 0 is a value: absent, or null, means the first form.
type TeamRevokeMemberRequest struct {
	PrincipalRef           string `json:"principal_ref,omitzero"`
	Ban                    bool   `json:"ban,omitzero"`
	JoinedSecretVersionLTE *int   `json:"joined_secret_version_lte,omitzero"`
}

// Validate implements Validator: exactly one form, ban with the first
// form only, and the version within 0..MaxSecretVersion.
func (r *TeamRevokeMemberRequest) Validate() error {
	switch {
	case r.PrincipalRef != "" && r.JoinedSecretVersionLTE != nil:
		return errTeamAdminField("principal_ref", reasonInvalidValue,
			"principal_ref and joined_secret_version_lte are two forms of one request; give one")
	case r.JoinedSecretVersionLTE != nil:
		if r.Ban {
			return errTeamAdminField("ban", reasonInvalidValue,
				"ban applies to principal_ref only; a version revoke never bans")
		}
		if *r.JoinedSecretVersionLTE < 0 || *r.JoinedSecretVersionLTE > MaxSecretVersion {
			return errTeamAdminField("joined_secret_version_lte", reasonOutOfRange,
				"joined_secret_version_lte must be between 0 and "+strconv.Itoa(MaxSecretVersion))
		}
	case r.PrincipalRef == "":
		return errTeamAdminField("principal_ref", reasonRequired,
			"one of principal_ref or joined_secret_version_lte is required")
	case !utf8.ValidString(r.PrincipalRef):
		return errNotUTF8("principal_ref")
	}
	return nil
}

// TeamRevokeMemberResult is the `team revoke-member` result of the
// principal form (4.4.10). Status is what the request asked for, Changed
// says whether the membership's status moved, and SessionsClosed counts
// the sessions this request closed.
type TeamRevokeMemberResult struct {
	TeamRef        string `json:"team_ref"`
	PrincipalRef   string `json:"principal_ref"`
	Status         string `json:"status"`
	Changed        bool   `json:"changed"`
	SessionsClosed int    `json:"sessions_closed"`
}

// Validate implements Validator.
func (r *TeamRevokeMemberResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("principal_ref", r.PrincipalRef); err != nil {
		return err
	}
	if err := oneOf("status", r.Status, MembershipRevoked, MembershipBanned); err != nil {
		return err
	}
	return countNotNegative("sessions_closed", r.SessionsClosed)
}

// TeamRevokeByVersionResult is the `team revoke-member` result of the
// version form (4.4.10). It names no principal: `team members` afterwards
// is the record.
type TeamRevokeByVersionResult struct {
	TeamRef        string `json:"team_ref"`
	MaxVersion     int    `json:"max_version"`
	Revoked        int    `json:"revoked"`
	SessionsClosed int    `json:"sessions_closed"`
}

// Validate implements Validator.
func (r *TeamRevokeByVersionResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if r.MaxVersion < 0 || r.MaxVersion > MaxSecretVersion {
		return errOutOfRange("max_version", 0, MaxSecretVersion)
	}
	if err := countNotNegative("revoked", r.Revoked); err != nil {
		return err
	}
	return countNotNegative("sessions_closed", r.SessionsClosed)
}

// TeamTransferRequest is the `team transfer` stdin document (4.4.10): the
// active member that becomes the team's owner.
type TeamTransferRequest struct {
	PrincipalRef string `json:"principal_ref"`
}

// Validate implements Validator.
func (r *TeamTransferRequest) Validate() error {
	if r.PrincipalRef == "" {
		return errTeamAdminField("principal_ref", reasonRequired, "principal_ref is required")
	}
	if !utf8.ValidString(r.PrincipalRef) {
		return errNotUTF8("principal_ref")
	}
	return nil
}

// TeamTransferResult is the `team transfer` result (4.4.10). Transferred
// is always true: success is the command's only outcome that is not an
// error.
type TeamTransferResult struct {
	TeamRef      string `json:"team_ref"`
	PrincipalRef string `json:"principal_ref"`
	Transferred  bool   `json:"transferred"`
}

// Validate implements Validator.
func (r *TeamTransferResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if err := requireString("principal_ref", r.PrincipalRef); err != nil {
		return err
	}
	if !r.Transferred {
		return errInvalidValue("transferred", "true")
	}
	return nil
}

// Moving a team (4.4.10, capability team.move; added 2026-11-13, work item
// 273, ADR 0003): `team move` and `team moved`. A team that leaves this
// backend for another has the move recorded here by its owner, and an
// active member's adapter reads the record: where the team went, and the
// move ticket that admits a member there.

// The caps of a move's destination (4.4.10), in code points: an adapter's
// name, a backend's URL and a certificate pin.
const (
	MaxMoveAdapterChars     = 64
	MaxMoveURLChars         = 2048
	MaxMoveCertificateChars = 128
)

// TeamMoveRequest is the `team move` stdin document (4.4.10): where the
// team went. The move ticket is not a member of it: the adapter reads the
// ticket from the file --ticket-file names, so it is never on argv and
// never in a document a caller could log.
type TeamMoveRequest struct {
	Adapter     string `json:"adapter"`
	URL         string `json:"url"`
	Certificate string `json:"certificate,omitzero"`
}

// Validate implements Validator. Whether the URL is one a backend can be
// reached at, and whether the certificate is a pin, is the business of the
// harness that follows the record: it follows only a record that names
// exactly what a team file names, and a team file is parsed strictly.
func (r *TeamMoveRequest) Validate() error {
	if err := requireString("adapter", r.Adapter); err != nil {
		return err
	}
	if err := capRunes("adapter", r.Adapter, MaxMoveAdapterChars); err != nil {
		return err
	}
	if err := requireString("url", r.URL); err != nil {
		return err
	}
	if err := capRunes("url", r.URL, MaxMoveURLChars); err != nil {
		return err
	}
	return optionalText("certificate", r.Certificate, MaxMoveCertificateChars)
}

// TeamMoveResult is the result of `team move` and of `team moved`
// (4.4.10). Moved false is a team with no move recorded, and then only
// team_ref goes with it. Moved true carries where the team went and when
// that was recorded. It has no ticket member: `team moved` writes the
// ticket to the file --ticket-file names and answers that path as
// ticket_file, because `team create` is the only command whose output may
// carry a secret (4.5.14).
type TeamMoveResult struct {
	TeamRef     string    `json:"team_ref"`
	Moved       bool      `json:"moved"`
	Adapter     string    `json:"adapter,omitzero"`
	URL         string    `json:"url,omitzero"`
	Certificate string    `json:"certificate,omitzero"`
	RecordedAt  time.Time `json:"recorded_at,omitzero"`
	TicketFile  string    `json:"ticket_file,omitzero"`
}

// Validate implements Validator.
func (r *TeamMoveResult) Validate() error {
	if err := requireString("team_ref", r.TeamRef); err != nil {
		return err
	}
	if !r.Moved {
		// A team that has not moved went nowhere: a destination beside
		// moved false is two answers in one document.
		for _, m := range []struct{ field, value string }{
			{"adapter", r.Adapter}, {"url", r.URL}, {"certificate", r.Certificate}, {"ticket_file", r.TicketFile},
		} {
			if m.value != "" {
				return errInvalidValue(m.field, "absent unless moved is true")
			}
		}
		if !r.RecordedAt.IsZero() {
			return errInvalidValue("recorded_at", "absent unless moved is true")
		}
		return nil
	}
	destination := TeamMoveRequest{Adapter: r.Adapter, URL: r.URL, Certificate: r.Certificate}
	if err := destination.Validate(); err != nil {
		return err
	}
	if err := requireTime("recorded_at", r.RecordedAt); err != nil {
		return err
	}
	if r.TicketFile != "" && !utf8.ValidString(r.TicketFile) {
		return errNotUTF8("ticket_file")
	}
	return nil
}

// errTeamAdminField refuses one member of a team-administration request,
// naming the member and the reason and never its value. The messages are
// the ones the commands have always answered with, which is why they are
// spelled here and not by the generic helpers.
func errTeamAdminField(field, reason, message string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: message,
		Details: map[string]string{"field": field, "reason": reason},
	}
}

// countNotNegative checks a count a result carries: zero or more.
func countNotNegative(field string, v int) error {
	if v < 0 {
		return errOutOfRange(field, 0, int(^uint(0)>>1))
	}
	return nil
}
