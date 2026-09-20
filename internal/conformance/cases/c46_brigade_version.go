package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c46BrigadeVersion (cap session.brigade_version): `brigade_version` set
// at registration comes back exactly in the register result and in
// `session list`; a heartbeat carrying it applies the new value — a
// harness can be replaced by a newer one while its session lives, and the
// roster names the one that is running; a heartbeat without it leaves it
// standing (absent means unchanged, JSON convention 4 — the harness never
// clears it); a value of 65 code points made of 2-byte runes is
// `invalid_input` naming `brigade_version` while one of 64 passes (so an
// adapter counting bytes fails, as in C-16). The bound is a wire-format
// one with no `limits` member, which is why the case names 64 rather than
// reading it from `describe` (4.4.2, 4.4.3, 4.4.4, 4.5.11, 4.7). The value
// is harness-reported and unverified: the case asserts storage and echo,
// never that the text is a version.
func c46BrigadeVersion() conformance.Case {
	return conformance.Case{
		ID:    "C-46",
		Rule:  "4.4.2 brigade_version",
		Title: "brigade_version round-trips through register, list and heartbeat; absent leaves it unchanged; over the bound → invalid_input",
		Tags: []string{
			conformance.TagCore,
			conformance.TagCap("session.brigade_version"),
		},
		Run: runC46,
	}
}

func runC46(t *conformance.T) {
	a := t.A()

	// Registration: echoed by the register result and by the list.
	version := "0.10.0"
	rec, s := t.Register(a, nameFor(t, "C-46", "version"), func(reg *protocol.SessionRegistration) {
		reg.BrigadeVersion = &version
	})
	if got, _ := rec["brigade_version"].(string); got != version {
		t.Errorf("session register: brigade_version %q, want %q (4.4.2)", got, version)
	}
	sessions, _ := t.List(a, "", false)
	checkBrigadeVersion(t, "session list", recordOf(t, sessions, s), version)

	// A heartbeat with it applies it (4.4.4): the harness was replaced.
	newer := "0.10.1"
	t.Heartbeat(a, s, &protocol.HeartbeatRequest{BrigadeVersion: &newer})
	sessions, _ = t.List(a, "", false)
	checkBrigadeVersion(t, "session list after a heartbeat with it", recordOf(t, sessions, s), newer)

	// A heartbeat without it is a pure renewal: the stored value stands.
	t.Heartbeat(a, s, nil)
	sessions, _ = t.List(a, "", false)
	checkBrigadeVersion(t, "session list after a heartbeat without it", recordOf(t, sessions, s), newer)

	// The bound (4.5.11): code points, not bytes.
	over := strings.Repeat("é", protocol.MaxBrigadeVersionChars+1)
	e := t.Fail(t.Exec(a, registrationWith(t, nameFor(t, "C-46", "over"), func(r *protocol.SessionRegistration) {
		r.BrigadeVersion = &over
	}), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "brigade_version" {
		t.Errorf("brigade_version over the bound: details.field %q, want brigade_version (4.3.1)", e.Details["field"])
	}
	atCap := strings.Repeat("é", protocol.MaxBrigadeVersionChars)
	rec, _ = t.Register(a, nameFor(t, "C-46", "cap"), func(r *protocol.SessionRegistration) { r.BrigadeVersion = &atCap })
	if got, _ := rec["brigade_version"].(string); got != atCap {
		t.Errorf("brigade_version at the bound: the record's value differs from the one registered (4.4.3)")
	}
}

// checkBrigadeVersion asserts one listed record carries exactly the
// version given; an absent member is reported as such.
func checkBrigadeVersion(t *conformance.T, what string, rec *protocol.SessionRecord, version string) {
	switch {
	case rec.BrigadeVersion == nil:
		t.Errorf("%s: brigade_version absent, want %q (4.4.3)", what, version)
	case *rec.BrigadeVersion != version:
		t.Errorf("%s: brigade_version %q, want %q (4.4.3)", what, *rec.BrigadeVersion, version)
	}
}
