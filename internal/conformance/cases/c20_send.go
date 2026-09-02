package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c20Send: A → B `message send` answers `status accepted`, a message_id,
// created_at, `duplicate false`, `hop_count 0`; B's `message receive`
// returns exactly that message with the sender stamped from the
// adapter's authority (A's session id, principal_ref and session_name),
// team_ref, hop_count 0, delivery_state accepted, kind text,
// protocol_version "1" (4.4.5, 4.4.7, 4.5.1, 4.5.5).
func c20Send() conformance.Case {
	return conformance.Case{
		ID:    "C-20",
		Rule:  "4.5.1 durable acceptance",
		Title: "send is accepted and receive returns it with the adapter-stamped sender",
		Tags:  []string{conformance.TagCore},
		Run:   runC20,
	}
}

func runC20(t *conformance.T) {
	a, b := t.A(), t.B()
	senderName := "c20-a-" + t.RunID()
	_, sa := t.Register(a, senderName, nil)
	_, sb := t.Register(b, "c20-b-"+t.RunID(), nil)
	const body = "c20: hello from A"
	res := t.Send(a, sa, sb, body, nil)
	if res.Status != protocol.SendStatusAccepted {
		t.Errorf("message send: status %q, want accepted (4.4.7)", res.Status)
	}
	if res.MessageID == "" {
		t.Errorf("message send: message_id is empty (4.4.7)")
	}
	if res.CreatedAt.IsZero() {
		t.Errorf("message send: created_at is missing (4.4.7)")
	}
	if res.Duplicate {
		t.Errorf("message send: duplicate true on a first send (4.4.7)")
	}
	if res.HopCount != 0 {
		t.Errorf("message send: hop_count %d, want 0 (4.5.12)", res.HopCount)
	}
	if res.RecipientSessionID != sb {
		t.Errorf("message send: recipient_session_id differs from the request (4.4.7)")
	}

	messages := t.Receive(b, sb, 0)
	if len(messages) != 1 {
		t.Fatalf("message receive: %d messages, want exactly the one sent (4.5.1)", len(messages))
	}
	m := messages[0]
	if m.MessageID != res.MessageID {
		t.Errorf("message receive: message_id differs from the send's (4.5.1)")
	}
	if m.Body != body {
		t.Errorf("message receive: body differs from the one sent (4.4.5)")
	}
	if m.Sender.SessionID != sa {
		t.Errorf("message receive: sender.session_id is not A's session (4.5.5)")
	}
	if m.Sender.PrincipalRef != a.PrincipalRef {
		t.Errorf("message receive: sender.principal_ref is not A's (4.5.5)")
	}
	if m.Sender.SessionName != senderName {
		t.Errorf("message receive: sender.session_name %q, want %q (4.5.5)", m.Sender.SessionName, senderName)
	}
	if m.TeamRef == "" || (a.TeamRef != "" && m.TeamRef != a.TeamRef) {
		t.Errorf("message receive: team_ref %q, want the profile's team (4.4.5)", m.TeamRef)
	}
	if m.RecipientSessionID != sb {
		t.Errorf("message receive: recipient_session_id is not B's session (4.4.5)")
	}
	if m.HopCount != 0 {
		t.Errorf("message receive: hop_count %d, want 0 (4.5.12)", m.HopCount)
	}
	if m.DeliveryState != protocol.DeliveryStateAccepted {
		t.Errorf("message receive: delivery_state %q, want accepted (4.4.5)", m.DeliveryState)
	}
	if m.Kind != protocol.KindText {
		t.Errorf("message receive: kind %q, want text (4.4.5)", m.Kind)
	}
	if m.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("message receive: protocol_version %q, want %q (4.4.5)", m.ProtocolVersion, protocol.ProtocolVersion)
	}
	if m.CreatedAt.IsZero() {
		t.Errorf("message receive: created_at is missing (4.4.5)")
	}
}
