package protocol

// The `profile.state` values of DescribeResult (4.4.1), computed from
// local files only.
const (
	ProfileStateUnconfigured    = "unconfigured"
	ProfileStateUnauthenticated = "unauthenticated"
	ProfileStateNotMember       = "not_member"
	ProfileStateJoined          = "joined"
)

// GuaranteeAtLeastOnce is the only delivery guarantee of protocol v1
// (freeze list item 4).
const GuaranteeAtLeastOnce = "at_least_once"

// The `delivery.ack_state` values: `injected` is the terminal adapter
// state in v1; `processed` is reserved (4.5.3).
const (
	AckStateInjected  = "injected"
	AckStateProcessed = "processed"
)

// AdapterInfo is the `adapter` member of DescribeResult (4.4.1).
type AdapterInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DeliveryInfo is the `delivery` member of DescribeResult (4.4.1).
type DeliveryInfo struct {
	Guarantee string `json:"guarantee"`
	Ordering  string `json:"ordering"`
	AckState  string `json:"ack_state"`
}

// ProfileInfo is the `profile` member of DescribeResult (4.4.1). The team
// members are present only when State is joined.
type ProfileInfo struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	TeamRef      string `json:"team_ref,omitzero"`
	TeamName     string `json:"team_name,omitzero"`
	PrincipalRef string `json:"principal_ref,omitzero"`
	HumanLabel   string `json:"human_label,omitzero"`
}

// DescribeResult is the `describe` result (4.4.1), answered from local
// state only.
type DescribeResult struct {
	ProtocolVersion string       `json:"protocol_version"`
	Adapter         AdapterInfo  `json:"adapter"`
	Delivery        DeliveryInfo `json:"delivery"`
	Capabilities    []string     `json:"capabilities"`
	Limits          Limits       `json:"limits"`
	Lease           Lease        `json:"lease"`
	Retention       Retention    `json:"retention"`
	Profile         ProfileInfo  `json:"profile"`
}

// Validate implements Validator. It deliberately does NOT require
// protocol_version to equal ProtocolVersion: version comparison belongs
// to the harness's negotiation (4.5.13, protocol_mismatch), not to input
// validation. Capability strings are not checked at all — unknown
// capabilities are ignored by design (4.7).
func (d *DescribeResult) Validate() error {
	if err := requireString("protocol_version", d.ProtocolVersion); err != nil {
		return err
	}
	if err := requireString("adapter.name", d.Adapter.Name); err != nil {
		return err
	}
	if err := requireString("adapter.version", d.Adapter.Version); err != nil {
		return err
	}
	if err := oneOf("delivery.guarantee", d.Delivery.Guarantee, GuaranteeAtLeastOnce); err != nil {
		return err
	}
	if err := requireString("delivery.ordering", d.Delivery.Ordering); err != nil {
		return err
	}
	if err := oneOf("delivery.ack_state", d.Delivery.AckState, AckStateInjected, AckStateProcessed); err != nil {
		return err
	}
	if err := d.Limits.validate(); err != nil {
		return err
	}
	if err := d.Lease.validate(); err != nil {
		return err
	}
	if err := d.Retention.validate(); err != nil {
		return err
	}
	return d.Profile.validate()
}

// validate checks that every advertised limit is positive. It does not
// pin the 4.4.1 defaults: describe REPORTS an adapter's limits, and the
// caps this package enforces are the protocol constants.
func (l *Limits) validate() error {
	for _, c := range []struct {
		field string
		value int
	}{
		{"limits.max_body_bytes", l.MaxBodyBytes},
		{"limits.max_summary_chars", l.MaxSummaryChars},
		{"limits.max_session_name_codepoints", l.MaxSessionNameCodepoints},
		{"limits.max_description_chars", l.MaxDescriptionChars},
		{"limits.max_human_label_chars", l.MaxHumanLabelChars},
		{"limits.max_idempotency_key_chars", l.MaxIdempotencyKeyChars},
		{"limits.send_rate.per_minute", l.SendRate.PerMinute},
		{"limits.send_rate.per_hour", l.SendRate.PerHour},
		{"limits.principal_send_rate.per_minute", l.PrincipalSendRate.PerMinute},
		{"limits.principal_send_rate.per_hour", l.PrincipalSendRate.PerHour},
		{"limits.max_unacked_per_recipient", l.MaxUnackedPerRecipient},
		{"limits.max_unacked_per_sender_recipient", l.MaxUnackedPerSenderRecipient},
		{"limits.max_hop_count", l.MaxHopCount},
		{"limits.implicit_reply_window_seconds", l.ImplicitReplyWindowSeconds},
	} {
		if c.value <= 0 {
			return errRequired(c.field)
		}
	}
	return nil
}

// validate checks the lease bounds are positive and ordered.
func (l *Lease) validate() error {
	if l.DefaultSeconds <= 0 {
		return errRequired("lease.default_seconds")
	}
	if l.MinSeconds <= 0 {
		return errRequired("lease.min_seconds")
	}
	if l.MaxSeconds <= 0 {
		return errRequired("lease.max_seconds")
	}
	if l.MinSeconds > l.DefaultSeconds || l.DefaultSeconds > l.MaxSeconds {
		return errOutOfRange("lease.default_seconds", l.MinSeconds, l.MaxSeconds)
	}
	return nil
}

// validate checks the retention floors are positive.
func (r *Retention) validate() error {
	if r.UnackedMessageSeconds <= 0 {
		return errRequired("retention.unacked_message_seconds")
	}
	if r.AckedMessageSeconds <= 0 {
		return errRequired("retention.acked_message_seconds")
	}
	if r.ClosedSessionSeconds <= 0 {
		return errRequired("retention.closed_session_seconds")
	}
	return nil
}

// validate checks the profile block: a valid state, the team members
// present exactly when joined (4.4.1: "team_ref and friends are present
// only for joined").
func (p *ProfileInfo) validate() error {
	if err := requireString("profile.name", p.Name); err != nil {
		return err
	}
	if err := oneOf("profile.state", p.State,
		ProfileStateUnconfigured, ProfileStateUnauthenticated,
		ProfileStateNotMember, ProfileStateJoined); err != nil {
		return err
	}
	if p.State == ProfileStateJoined {
		if err := requireString("profile.team_ref", p.TeamRef); err != nil {
			return err
		}
		if err := requireString("profile.team_name", p.TeamName); err != nil {
			return err
		}
		if err := requireString("profile.principal_ref", p.PrincipalRef); err != nil {
			return err
		}
		return optionalText("profile.human_label", p.HumanLabel, MaxHumanLabelChars)
	}
	for _, c := range []struct{ field, value string }{
		{"profile.team_ref", p.TeamRef},
		{"profile.team_name", p.TeamName},
		{"profile.principal_ref", p.PrincipalRef},
		{"profile.human_label", p.HumanLabel},
	} {
		if c.value != "" {
			return &Error{
				Code:    CodeInvalidInput,
				Message: c.field + " is present only when profile.state is joined",
				Details: map[string]string{"field": c.field, "reason": reasonInvalidValue},
			}
		}
	}
	return nil
}
