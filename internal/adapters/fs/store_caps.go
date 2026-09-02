//go:build !mutant_caporder

package fs

import "github.com/appshapes/brigade/internal/protocol"

// checkUnackedCaps applies the two unacknowledged-message caps of 4.5.12
// in the ORDER the spec fixes: the per-pair cap
// (max_unacked_per_sender_recipient, `sender_quota_for_recipient`) BEFORE
// the recipient-wide one (max_unacked_per_recipient,
// `recipient_inbox_full`). The order is observable and it matters: with
// both caps at their limit at once, a sender that has filled its own quota
// must be told THAT, not that the recipient's inbox is full — one sender
// can never make a recipient read as full for everybody else, and a sender
// that stops sending is told exactly what it has to do. pending is the
// recipient's unacknowledged inbox.
//
// Its twin in store_caps_mutant.go swaps the two checks. That mutant must
// fail exactly C-28.
func checkUnackedCaps(pending []storedMessage, senderSessionID string, limits protocol.Limits) error {
	if unackedFrom(pending, senderSessionID) >= limits.MaxUnackedPerSenderRecipient {
		return errRateLimited(reasonSenderQuotaForRecipient, capRetryAfterMS)
	}
	if len(pending) >= limits.MaxUnackedPerRecipient {
		return errRateLimited(reasonRecipientInboxFull, capRetryAfterMS)
	}
	return nil
}

// unackedFrom counts the messages of pending that one sender session sent.
func unackedFrom(pending []storedMessage, senderSessionID string) int {
	n := 0
	for i := range pending {
		if pending[i].Sender.SessionID == senderSessionID {
			n++
		}
	}
	return n
}
