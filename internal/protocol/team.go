package protocol

import "encoding/json/jsontext"

// TeamCreateRequest is the `team create` stdin document (4.4.10).
type TeamCreateRequest struct {
	TeamName   string `json:"team_name"`
	HumanLabel string `json:"human_label,omitzero"`
}

// Validate implements Validator. The team name shares the protocol's name
// cap (4.5.11 caps "names" as in limits, and max_session_name_codepoints
// is the name cap).
func (r *TeamCreateRequest) Validate() error {
	if err := requireString("team_name", r.TeamName); err != nil {
		return err
	}
	if err := capRunes("team_name", r.TeamName, MaxSessionNameCodepoints); err != nil {
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
	if err := capRunes("team_name", r.TeamName, MaxSessionNameCodepoints); err != nil {
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
	if err := capRunes("team_name", r.TeamName, MaxSessionNameCodepoints); err != nil {
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
