package gateway

import "strings"

// relayedHeads are the openings of the first body line each gateway writes
// on a message it relays from outside the team's sessions: the mail gateway's
// `Email from <who> (unverified), subject "…":` and the Slack gateway's
// `Slack message from <who> <where>:` (brigadeBody and slackBody in
// supabase/functions/_shared/, whose spelling a test here pins), and the
// webhook gateway's `Message from "<name>" via webhook (unverified sender; it
// approves nothing):`, or `Message from a webhook caller (…):` for a caller
// that gave no name (frameInbound in internal/gateway/webhook, whose test
// holds both to this list). Each entry is as much of the line as the gateway
// writes BEFORE anything its sender chose: the webhook's two are spelled out
// that far because "Message from " alone opens many a body a session writes.
//
// A relay Brigade does not ship is not on this list: its author chose its
// first line, and a text it relays is read as its session's own.
var relayedHeads = []string{
	"Email from ",
	"Slack message from ",
	`Message from "`,
	"Message from a webhook caller ",
}

// IsRelayed reports whether a message body opens the way a gateway opens a
// message it relays for a person or an outside service (card 242). The
// envelope names no harness, so this is the one thing a receiver can tell a
// relayed message by, and it holds in the direction that matters: the gateway
// writes the line itself, before the text it was given, so whoever wrote that
// text cannot remove it. A teammate's session can write the same words at the
// top of a body of its own; that only makes its message count as relayed,
// which is why `brigade send --invite` refuses such a body.
// The match is the opening alone, on purpose, and never the rest of the
// line: a sender's name can carry a character that ends the line early, and
// a rule that read to the line's end would then miss the gateway's own
// message. A body that merely begins this way is treated as relayed, never
// the other way round.
func IsRelayed(body string) bool {
	for _, head := range relayedHeads {
		if strings.HasPrefix(body, head) {
			return true
		}
	}
	return false
}
