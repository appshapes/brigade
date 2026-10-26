// Package webhook is the Brigade webhook gateway: an ordinary team member
// that forwards its inbox to an HTTPS endpoint and turns signed HTTP POSTs
// back into Brigade messages.
//
// It speaks to the backend ONLY through BAP/1 (docs/protocol-v1.md), via
// the [Backend] interface that *adapterclient.Client satisfies, so the
// same gateway runs over the filesystem adapter (a Syncthing-shared
// folder), the bundled Supabase adapter, or any conforming adapter. It
// never reads a store directly.
//
// Outbound: every message addressed to the gateway's session is POSTed as
// JSON to the configured URL, signed with HMAC-SHA256 over
// "v1:<unix seconds>:<body>" ([Sign]). The message is acknowledged only
// after a 2xx answer; a retryable failure (transport error, 408, 425, 429,
// 5xx) leaves it unacknowledged for the next poll with a growing backoff;
// a permanent failure (any other status, or too many attempts) tells the
// sender once, then acknowledges.
//
// Inbound: POST /v1/messages accepts a signed JSON document
// {to|member|reply_to, text, ...} ([InboundRequest]); the signature and
// timestamp window are checked with a constant-time compare BEFORE the
// body is parsed, unknown members and oversized bodies are refused, and
// the message is sent from the gateway's own session with an idempotency
// key so a retried or replayed POST is delivered once.
//
// Nothing that arrives through this gateway approves anything
// (docs/security.md): an outbound payload is untrusted text from a
// teammate's session, and an inbound one becomes an ordinary message that
// the recipient's harness frames as untrusted.
package webhook
