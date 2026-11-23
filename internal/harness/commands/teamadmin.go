package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// What the harness says of a team's administration verb that a backend
// answered `unauthorized` (work item 321).
//
// A backend has one answer for every caller that may not administer a team:
// a team that does not exist, a membership that never was or was revoked,
// and an active member who is not the administrator are the same bytes,
// "not an active member of this team" (protocol 4.5.7, conformance cases
// C-26 and C-43), so that a refusal tells a stranger neither whether a team
// exists nor who administers it. Of an active member those words are
// untrue, and nothing on the wire may say more. So the harness adds a
// sentence of its own, the same whoever the caller is, which therefore
// tells nobody anything the backend did not: what the answer covers, who
// the administrator is, and how a member sees that they still are one.
//
// It does not say that the caller is not the administrator: the same
// answer is given to the administrator for a principal the backend does not
// know, a mistyped reference among them (4.4.10), and the sentence names
// that cause beside the other.
//
// It is said of the verbs that only the administrator may run: `team
// rotate-secret`, `team revoke-member`, `team transfer`, and the record and
// the clear of `team move` where the command cannot read more (ownerOnly
// says more of a Brigade server whose record the caller can read). And it
// is said only after the words the three backends of this repository give,
// notAMemberWords: an adapter of someone else's that says in its own words
// who may act needs nobody to explain them.
const adminRefusalSentence = "A backend gives this one answer to everyone but the team's administrator, a member of the team included, " +
	"and to the administrator for a principal it does not know, " +
	"so that a refusal tells nobody whether a team exists, who is in it or who administers it. " +
	"The administrator is the member who created the team, or its owner on a Brigade server; " +
	"`brigade team members`, asked of the same team, still answers a member"

// notAMemberWords is the uniform refusal of the Supabase adapter, the fs
// adapter and the Brigade server (errNotMember in the two adapters,
// `not_member` in the server's core).
const notAMemberWords = "not an active member of this team"

// adminRefusalLine is the sentence as a line of its own, for a verb whose
// answer is the adapter's envelope, passed through untouched.
func adminRefusalLine(verb string) string {
	return "brigade: team " + verb + " was answered unauthorized. " + adminRefusalSentence + "."
}

// refusedUnauthorized reports whether an adapter's answer is one failing
// envelope whose code is `unauthorized` and whose words are the uniform
// ones.
func refusedUnauthorized(envelope []byte) bool {
	var env struct {
		OK    *bool `json:"ok"`
		Error *struct {
			Code    protocol.Code `json:"code"`
			Message string        `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(envelope), &env) != nil {
		return false
	}
	return env.OK != nil && !*env.OK && env.Error != nil && env.Error.Code == protocol.CodeUnauthorized && env.Error.Message == notAMemberWords
}

// noteAdminRefusal says the sentence on stderr after an administration verb
// whose envelope, already written to stdout as the adapter wrote it, is the
// `unauthorized` one. Nothing of the envelope or of the exit status changes.
func (inv Invocation) noteAdminRefusal(verb string, envelope []byte) {
	if refusedUnauthorized(envelope) {
		// Best effort: the refusal itself is on stdout and in the exit status.
		_, _ = io.WriteString(inv.Err, adminRefusalLine(verb)+"\n")
	}
}

// adminRefused adds the sentence to a refusal the harness reports itself:
// a backend's `unauthorized` in the uniform words, of which the command
// knows no more. Any other error, a refusal in an adapter's own words, and
// one the harness has already put its own reason on, is returned as it is.
func adminRefused(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUnauthorized || perr.Details["reason"] != "" ||
		strings.TrimRight(perr.Message, ". ") != notAMemberWords {
		return err
	}
	out := *perr
	out.Message = notAMemberWords + ". " + adminRefusalSentence
	return &out
}
