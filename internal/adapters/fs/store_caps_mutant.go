//go:build mutant_caporder

package fs

import "github.com/appshapes/brigade/internal/protocol"

// This file is a DELIBERATE DEFECT, compiled only with -tags
// mutant_caporder. P1-6's mutants_test.go asserts that this build fails
// exactly C-28 (4.5.12: the per-pair unacknowledged cap is checked BEFORE
// the recipient-wide one) and no other case. The normal build contains
// none of this code — see store_caps.go.
//
// It exists because the ORDER of these two checks was the decisive
// untested defect in both P1-5 and P1-6: every other property of the caps
// — the codes, the reasons, retry_after_ms, the thresholds — holds under
// the swap, and only a case that puts BOTH caps at their limit at once can
// see the difference.

// checkUnackedCaps checks the recipient-wide cap first, so a recipient
// whose inbox is full answers `recipient_inbox_full` even to a sender that
// has filled its own per-pair quota. Everything else the real twin does is
// kept verbatim: the mutation is the order and nothing but the order.
func checkUnackedCaps(pending []storedMessage, senderSessionID string, limits protocol.Limits) error {
	if len(pending) >= limits.MaxUnackedPerRecipient {
		return errRateLimited(reasonRecipientInboxFull, capRetryAfterMS)
	}
	if unackedFrom(pending, senderSessionID) >= limits.MaxUnackedPerSenderRecipient {
		return errRateLimited(reasonSenderQuotaForRecipient, capRetryAfterMS)
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
