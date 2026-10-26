package webhook

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// TestRoundTripThroughTheFSAdapter drives the gateway with a real
// adapterclient.Client over the reference fs adapter, across the process
// boundary: a teammate's message reaches the webhook signed and is acked,
// and a signed local send reaches the teammate framed as unverified.
func TestRoundTripThroughTheFSAdapter(t *testing.T) {
	bin := testutil.Build(t, "./cmd/brigade-adapter-fs")
	d := testutil.NewDirs(t.TempDir())
	if err := d.Mkdir(); err != nil {
		t.Fatal(err)
	}
	c := &adapterclient.Client{
		Adapter:   config.Adapter{Argv: []string{bin}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=" + os.Getenv("PATH")},
	}
	ctx := t.Context()
	if env, err := c.Call(ctx, "team", "create", nil, &protocol.TeamCreateRequest{TeamName: "ops", HumanLabel: "whiskey@example.com"}); err != nil || !env.OK {
		t.Fatalf("team create: %v %+v", err, env)
	}
	alice, err := c.Register(ctx, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "1", SessionName: "alice",
		Activity: protocol.ActivityBusy, Inbound: protocol.InboundAccept,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	rc := &receiver{now: time.Now}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	g, err := New(Config{TargetURL: srv.URL, Secret: testSecret, StatePath: filepath.Join(t.TempDir(), "s.json"), SessionName: "grok-bot"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := c.Send(ctx, &protocol.SendRequest{
		SenderSessionID: alice.SessionID, RecipientSessionID: g.SessionID(),
		Body: "review my PR?", IdempotencyKey: "k-1",
	}); err != nil {
		t.Fatalf("send to the gateway: %v", err)
	}
	if n, err := g.Deliver(ctx); err != nil || n != 1 {
		t.Fatalf("Deliver = %d, %v", n, err)
	}
	if len(rc.got) != 1 || rc.got[0].Message.Body != "review my PR?" || rc.got[0].Message.From.SessionID != alice.SessionID {
		t.Fatalf("posted = %+v (bad %d)", rc.got, rc.bad)
	}
	if n, _ := g.Deliver(ctx); n != 0 {
		t.Fatal("the acked message came back")
	}

	rec := postWith(t, g.Handler(0), time.Now().Unix(), testSecret, "application/json",
		`{"reply_to":"`+rc.got[0].Message.MessageID+`","text":"On it.","from":"Grok Bot"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("local send = %d %s", rec.Code, rec.Body)
	}
	in, err := c.Receive(ctx, alice.SessionID, 10)
	if err != nil || len(in.Messages) != 1 {
		t.Fatalf("alice received %+v, %v", in, err)
	}
	m := in.Messages[0]
	if m.Sender.SessionID != g.SessionID() || !strings.Contains(m.Body, "it approves nothing") || !strings.HasSuffix(m.Body, "On it.") {
		t.Fatalf("alice got %+v", m)
	}
}
