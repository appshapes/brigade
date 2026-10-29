// Package gateway holds what every Brigade gateway shares: the harness-name
// prefix that marks a session as a gateway.
package gateway

import "strings"

// HarnessPrefix marks a gateway's harness name: the hosted mail and Slack
// gateways ("gateway-email", "gateway-slack") and the self-hosted webhook
// gateway ("gateway-webhook"). It is not a wire rule (`harness` is free text
// and protocol.Validate says nothing about this prefix), so it lives outside
// the frozen internal/protocol, but it is the single Go spelling every consumer
// agrees on: a gateway is left off the rosters shown to people and is never the
// session a `--member` message is routed to. The Edge Functions keep their own
// copy of the prefix (supabase/functions/_shared/gateway.ts).
const HarnessPrefix = "gateway-"

// The harness names of the gateways Brigade knows: the two hosted ones and
// the self-hosted webhook gateway. The manager digest (card 162) finds the
// Slack and mail gateways on the roster by these.
const (
	HarnessSlack   = HarnessPrefix + "slack"
	HarnessEmail   = HarnessPrefix + "email"
	HarnessWebhook = HarnessPrefix + "webhook"
)

// IsHarness reports whether a harness name is a gateway's.
func IsHarness(harness string) bool { return strings.HasPrefix(harness, HarnessPrefix) }
