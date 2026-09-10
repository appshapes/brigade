package protocol

// The protocol v1 constants of plan 4.4.1. Byte caps are measured with
// len(); code-point caps with utf8.RuneCountInString — a 16 KiB byte cap
// is NOT 16384 characters, and conflating the two is a real bug the
// validation tests pin in both directions.
const (
	// MaxBodyBytes caps a message body, in BYTES of UTF-8.
	MaxBodyBytes = 16384
	// MaxSummaryChars caps a message summary, in Unicode code points.
	MaxSummaryChars = 200
	// MaxSessionNameCodepoints caps a session name, in code points.
	MaxSessionNameCodepoints = 64
	// MaxTeamNameCodepoints caps a team name, in code points. A team name
	// is a frame tag attribute (6.7 `team="…"`), and 6.7 rule 4 caps tag
	// attribute values at 64 code points; it is its own limit, not a
	// borrowed session-name cap (P1-4 decision 1).
	MaxTeamNameCodepoints = 64
	// MaxDescriptionChars caps a session description, in code points.
	MaxDescriptionChars = 256
	// MaxHumanLabelChars caps a human label, in code points.
	MaxHumanLabelChars = 128
	// MaxWorkspaceLabelChars caps a workspace label, in code points. It is
	// a label like human_label and shares its size, but is its own limit
	// and its own `limits` member (P1-4 decision 1).
	MaxWorkspaceLabelChars = 128
	// MaxModelChars caps a harness-reported model identity (`model`:
	// 4.4.2, 4.4.3, 4.4.4 and the watch heartbeat command of 4.4.9;
	// capability session.model, C-44), in code points. It is label-sized
	// like human_label, but is its own limit and its own `limits` member,
	// max_model_chars (the rule of P1-4 decision 1: no member borrows
	// another member's cap).
	MaxModelChars = 128
	// MaxIdempotencyKeyChars caps an idempotency key, in code points.
	MaxIdempotencyKeyChars = 128
	// SendRatePerMinute and SendRatePerHour bound one sender session.
	SendRatePerMinute = 20
	// SendRatePerHour is the hourly half of the per-session send rate.
	SendRatePerHour = 200
	// PrincipalSendRatePerMinute bounds all sessions of one principal.
	PrincipalSendRatePerMinute = 60
	// PrincipalSendRatePerHour is the hourly half of the principal rate.
	PrincipalSendRatePerHour = 600
	// MaxUnackedPerRecipient caps a recipient session's unacknowledged inbox.
	MaxUnackedPerRecipient = 60
	// MaxUnackedPerSenderRecipient caps one sender-recipient pair, checked
	// before the recipient-wide cap (4.5.12).
	MaxUnackedPerSenderRecipient = 15
	// MaxHopCount is the reply-chain bound behind loop_detected.
	MaxHopCount = 32
	// ImplicitReplyWindowSeconds is the window in which an unlabelled
	// answer still counts as a reply for hop counting (4.5.12).
	ImplicitReplyWindowSeconds = 600
)

// MaxContextUsedTokens bounds `context_used_tokens` (4.4.2, 4.4.3, 4.4.4
// and the watch heartbeat command of 4.4.9; capability
// session.context_used_tokens, C-44): 2^53 - 1, the largest integer JSON
// carries exactly. A consumer whose JSON number is an IEEE 754 double
// (JavaScript, and every language that follows it) rounds anything
// larger, so a count above the bound could not round-trip as sent; the
// harness reports a measured count and nothing real comes within orders
// of magnitude of it. It is a wire-format bound, not an adapter's cap,
// so unlike MaxModelChars it has no `limits` member (4.4.1).
const MaxContextUsedTokens = 1<<53 - 1

// The protocol v1 lease bounds of plan 4.4.1, in seconds.
const (
	// LeaseDefaultSeconds is the lease granted when the caller names none.
	LeaseDefaultSeconds = 90
	// LeaseMinSeconds is the shortest lease the DEFAULT range accepts.
	// The range is per adapter (4.4.1: "the range of lease_seconds an
	// adapter accepts"), advertised in describe.lease and enforced by the
	// adapter with Lease.CheckSeconds; these three are the 4.4.1 example
	// values, which the Supabase adapter uses and DefaultLease returns.
	// The request shapes' Validate requires only a positive value.
	LeaseMinSeconds = 30
	// LeaseMaxSeconds is the longest lease the default range accepts.
	LeaseMaxSeconds = 600
)

// The protocol v1 retention floors of plan 4.4.1, in seconds.
const (
	// RetentionUnackedMessageSeconds is how long an unacknowledged message
	// is retained at least.
	RetentionUnackedMessageSeconds = 604800
	// RetentionAckedMessageSeconds is how soon an acknowledged message may
	// be deleted.
	RetentionAckedMessageSeconds = 86400
	// RetentionClosedSessionSeconds is how soon a closed or expired
	// session may be deleted together with its messages.
	RetentionClosedSessionSeconds = 604800
)

// SendRate is one per-minute/per-hour rate pair of `limits` (4.4.1).
type SendRate struct {
	PerMinute int `json:"per_minute"`
	PerHour   int `json:"per_hour"`
}

// Limits is the `limits` member of DescribeResult (4.4.1).
type Limits struct {
	MaxBodyBytes                 int      `json:"max_body_bytes"`
	MaxSummaryChars              int      `json:"max_summary_chars"`
	MaxSessionNameCodepoints     int      `json:"max_session_name_codepoints"`
	MaxTeamNameCodepoints        int      `json:"max_team_name_codepoints"`
	MaxDescriptionChars          int      `json:"max_description_chars"`
	MaxHumanLabelChars           int      `json:"max_human_label_chars"`
	MaxWorkspaceLabelChars       int      `json:"max_workspace_label_chars"`
	MaxModelChars                int      `json:"max_model_chars"`
	MaxIdempotencyKeyChars       int      `json:"max_idempotency_key_chars"`
	SendRate                     SendRate `json:"send_rate"`
	PrincipalSendRate            SendRate `json:"principal_send_rate"`
	MaxUnackedPerRecipient       int      `json:"max_unacked_per_recipient"`
	MaxUnackedPerSenderRecipient int      `json:"max_unacked_per_sender_recipient"`
	MaxHopCount                  int      `json:"max_hop_count"`
	ImplicitReplyWindowSeconds   int      `json:"implicit_reply_window_seconds"`
}

// Lease is the `lease` member of DescribeResult (4.4.1).
type Lease struct {
	DefaultSeconds int `json:"default_seconds"`
	MinSeconds     int `json:"min_seconds"`
	MaxSeconds     int `json:"max_seconds"`
}

// Retention is the `retention` member of DescribeResult (4.4.1).
type Retention struct {
	UnackedMessageSeconds int `json:"unacked_message_seconds"`
	AckedMessageSeconds   int `json:"acked_message_seconds"`
	ClosedSessionSeconds  int `json:"closed_session_seconds"`
}

// DefaultLimits returns the v1 limits with the exact 4.4.1 values.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:                 MaxBodyBytes,
		MaxSummaryChars:              MaxSummaryChars,
		MaxSessionNameCodepoints:     MaxSessionNameCodepoints,
		MaxTeamNameCodepoints:        MaxTeamNameCodepoints,
		MaxDescriptionChars:          MaxDescriptionChars,
		MaxHumanLabelChars:           MaxHumanLabelChars,
		MaxWorkspaceLabelChars:       MaxWorkspaceLabelChars,
		MaxModelChars:                MaxModelChars,
		MaxIdempotencyKeyChars:       MaxIdempotencyKeyChars,
		SendRate:                     SendRate{PerMinute: SendRatePerMinute, PerHour: SendRatePerHour},
		PrincipalSendRate:            SendRate{PerMinute: PrincipalSendRatePerMinute, PerHour: PrincipalSendRatePerHour},
		MaxUnackedPerRecipient:       MaxUnackedPerRecipient,
		MaxUnackedPerSenderRecipient: MaxUnackedPerSenderRecipient,
		MaxHopCount:                  MaxHopCount,
		ImplicitReplyWindowSeconds:   ImplicitReplyWindowSeconds,
	}
}

// DefaultLease returns the v1 lease bounds with the exact 4.4.1 values.
func DefaultLease() Lease {
	return Lease{DefaultSeconds: LeaseDefaultSeconds, MinSeconds: LeaseMinSeconds, MaxSeconds: LeaseMaxSeconds}
}

// DefaultRetention returns the v1 retention floors with the exact 4.4.1 values.
func DefaultRetention() Retention {
	return Retention{
		UnackedMessageSeconds: RetentionUnackedMessageSeconds,
		AckedMessageSeconds:   RetentionAckedMessageSeconds,
		ClosedSessionSeconds:  RetentionClosedSessionSeconds,
	}
}
