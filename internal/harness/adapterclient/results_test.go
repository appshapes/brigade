package adapterclient

import (
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// registration builds a minimal valid SessionRegistration.
func registration(name string) *protocol.SessionRegistration {
	return &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "1",
		SessionName: name, Activity: protocol.ActivityBusy, Inbound: protocol.InboundAccept,
	}
}

// setupTeam binds c's profile to a fresh team through the fs adapter.
func setupTeam(t *testing.T, c *Client) {
	t.Helper()
	env, err := c.Call(t.Context(), "team", "create", nil, &protocol.TeamCreateRequest{
		TeamName: "ops", HumanLabel: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("team create: %v", err)
	}
	if !env.OK {
		t.Fatalf("team create envelope not ok: %+v", env)
	}
}

// TestFSRoundTrip drives every typed helper through the real fs adapter:
// register, list, members, send, receive, ack and close all round-trip
// across a genuine process boundary.
func TestFSRoundTrip(t *testing.T) {
	t.Parallel()
	c := fsClient(t)
	ctx := t.Context()

	setupTeam(t, c)

	alpha, err := c.Register(ctx, registration("alpha"))
	if err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if alpha.SessionID == "" || alpha.SessionName != "alpha" {
		t.Fatalf("register alpha result = %+v", alpha)
	}
	bravo, err := c.Register(ctx, registration("bravo"))
	if err != nil {
		t.Fatalf("register bravo: %v", err)
	}

	list, err := c.ListSessions(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("list returned %d sessions, want 2: %+v", len(list.Sessions), list.Sessions)
	}
	if list.TeamName != "ops" {
		t.Errorf("list team = %q, want ops", list.TeamName)
	}

	members, err := c.TeamMembers(ctx)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members.Members) != 1 {
		t.Fatalf("members = %d, want 1: %+v", len(members.Members), members.Members)
	}

	sent, err := c.Send(ctx, &protocol.SendRequest{
		SenderSessionID: alpha.SessionID, RecipientSessionID: bravo.SessionID, Body: "hello bravo",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Status != protocol.SendStatusAccepted || sent.MessageID == "" {
		t.Fatalf("send response = %+v", sent)
	}

	recv, err := c.Receive(ctx, bravo.SessionID, 0)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(recv.Messages) != 1 {
		t.Fatalf("receive returned %d messages, want 1", len(recv.Messages))
	}
	if got := recv.Messages[0].Body; got != "hello bravo" {
		t.Errorf("received body = %q, want %q", got, "hello bravo")
	}

	ack, err := c.Ack(ctx, bravo.SessionID, &protocol.AckRequest{MessageIDs: []string{sent.MessageID}})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if len(ack.Acked) != 1 || ack.Acked[0] != sent.MessageID {
		t.Fatalf("ack result = %+v, want the sent id acked", ack)
	}

	closed, err := c.Close(ctx, alpha.SessionID)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.SessionID != alpha.SessionID || closed.State != protocol.SessionStateOffline {
		t.Errorf("close result = %+v, want %s offline", closed, alpha.SessionID)
	}
}

// TestReceiveHonoursLimit proves the --limit flag reaches the adapter: a
// limit of 1 returns one message even when two are pending.
func TestReceiveHonoursLimit(t *testing.T) {
	t.Parallel()
	c := fsClient(t)
	ctx := t.Context()
	setupTeam(t, c)

	alpha, err := c.Register(ctx, registration("alpha"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	bravo, err := c.Register(ctx, registration("bravo"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, body := range []string{"one", "two"} {
		if _, err := c.Send(ctx, &protocol.SendRequest{
			SenderSessionID: alpha.SessionID, RecipientSessionID: bravo.SessionID, Body: body,
		}); err != nil {
			t.Fatalf("send %q: %v", body, err)
		}
	}
	recv, err := c.Receive(ctx, bravo.SessionID, 1)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(recv.Messages) != 1 {
		t.Fatalf("receive --limit 1 returned %d messages, want 1", len(recv.Messages))
	}
}
