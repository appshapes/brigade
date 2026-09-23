package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c47SyncPeer (cap session.sync_peer): `sync_peer` set at registration
// comes back exactly in the register result and in `session list`; a
// heartbeat carrying it applies the new value — a session's sync adapter
// usually attaches after registration, so the watcher's first heartbeat
// after `attach` is the one that names it (plan folder-sync.md 4.2); a
// heartbeat without it leaves it standing (absent means unchanged, JSON
// convention 4 — the harness never clears it); a value of 257 code points
// made of 2-byte runes is `invalid_input` naming `sync_peer` while one of
// 256 passes (so an adapter counting bytes fails, as in C-16). The bound
// is a wire-format one with no `limits` member, which is why the case
// names 256 rather than reading it from `describe` (4.4.2, 4.4.3, 4.4.4,
// 4.5.11, 4.7). The value is harness-reported, unverified and opaque: the
// case asserts storage and echo, never that the text names an adapter.
func c47SyncPeer() conformance.Case {
	return conformance.Case{
		ID:    "C-47",
		Rule:  "4.4.2 sync_peer",
		Title: "sync_peer round-trips through register, list and heartbeat; absent leaves it unchanged; over the bound → invalid_input",
		Tags: []string{
			conformance.TagCore,
			conformance.TagCap("session.sync_peer"),
		},
		Run: runC47,
	}
}

func runC47(t *conformance.T) {
	a := t.A()

	// Registration: echoed by the register result and by the list.
	peer := "syncthing:C47AAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
	rec, s := t.Register(a, nameFor(t, "C-47", "peer"), func(reg *protocol.SessionRegistration) {
		reg.SyncPeer = &peer
	})
	if got, _ := rec["sync_peer"].(string); got != peer {
		t.Errorf("session register: sync_peer %q, want %q (4.4.2)", got, peer)
	}
	sessions, _ := t.List(a, "", false)
	checkSyncPeer(t, "session list", recordOf(t, sessions, s), peer)

	// A heartbeat with it applies it (4.4.4): the sync adapter attached
	// after the registration.
	attached := "syncthing:C47BBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
	t.Heartbeat(a, s, &protocol.HeartbeatRequest{SyncPeer: &attached})
	sessions, _ = t.List(a, "", false)
	checkSyncPeer(t, "session list after a heartbeat with it", recordOf(t, sessions, s), attached)

	// A heartbeat without it is a pure renewal: the stored value stands.
	t.Heartbeat(a, s, nil)
	sessions, _ = t.List(a, "", false)
	checkSyncPeer(t, "session list after a heartbeat without it", recordOf(t, sessions, s), attached)

	// The bound (4.5.11): code points, not bytes.
	over := strings.Repeat("é", protocol.MaxSyncPeerChars+1)
	e := t.Fail(t.Exec(a, registrationWith(t, nameFor(t, "C-47", "over"), func(r *protocol.SessionRegistration) {
		r.SyncPeer = &over
	}), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "sync_peer" {
		t.Errorf("sync_peer over the bound: details.field %q, want sync_peer (4.3.1)", e.Details["field"])
	}
	atCap := strings.Repeat("é", protocol.MaxSyncPeerChars)
	rec, _ = t.Register(a, nameFor(t, "C-47", "cap"), func(r *protocol.SessionRegistration) { r.SyncPeer = &atCap })
	if got, _ := rec["sync_peer"].(string); got != atCap {
		t.Errorf("sync_peer at the bound: the record's value differs from the one registered (4.4.3)")
	}
}

// checkSyncPeer asserts one listed record carries exactly the peer given;
// an absent member is reported as such.
func checkSyncPeer(t *conformance.T, what string, rec *protocol.SessionRecord, peer string) {
	switch {
	case rec.SyncPeer == nil:
		t.Errorf("%s: sync_peer absent, want %q (4.4.3)", what, peer)
	case *rec.SyncPeer != peer:
		t.Errorf("%s: sync_peer %q, want %q (4.4.3)", what, *rec.SyncPeer, peer)
	}
}
