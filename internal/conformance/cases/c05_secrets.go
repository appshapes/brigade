package cases

import (
	"bytes"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c05Secrets: `--join-secret` on argv is `usage` on a core command and on
// `team join` (4.1, 4.5.14) and its value reaches neither stdout nor
// stderr; A's `describe` at debug carries no secret. The launcher scans
// every spawn of the run for the known secrets and the `brg1.` prefix and
// the run directory at the end (brief section 5); those checks are
// attributed to whichever case was running, this one included.
func c05Secrets() conformance.Case {
	return conformance.Case{
		ID:    "C-05",
		Rule:  "4.5.14 no secret on argv or in output",
		Title: "--join-secret → usage without echo; describe and stderr at debug carry no secret",
		Tags:  []string{conformance.TagCore},
		Run:   runC05,
	}
}

func runC05(t *conformance.T) {
	a := t.A()
	const planted = "brg1.x.y"
	r := t.Exec(a, nil, "session", "list", "--join-secret", planted)
	t.Fail(r, protocol.CodeUsage)
	assertNoEcho(t, r, planted)
	if t.HasCap("team.join") {
		r = t.Exec(a, nil, "team", "join", "--join-secret", planted)
		t.Fail(r, protocol.CodeUsage)
		assertNoEcho(t, r, planted)
	}

	rd := t.Exec(a, nil, "describe")
	var d protocol.DescribeResult
	t.OK(rd, &d)
	if secret := t.JoinSecret(); secret != "" {
		if bytes.Contains(rd.Stdout, []byte(secret)) || bytes.Contains(rd.Stderr, []byte(secret)) {
			t.Errorf("describe of A: stdout or stderr carries the team's join secret (4.5.14)")
		}
	}
	if bytes.Contains(rd.Stdout, []byte(protocol.JoinSecretPrefix)) || bytes.Contains(rd.Stderr, []byte(protocol.JoinSecretPrefix)) {
		t.Errorf("describe of A: stdout or stderr carries the join-secret prefix (4.5.14)")
	}
	t.Note("every spawn's stderr and the run directory are scanned for known secrets by the launcher; a hit anywhere in the run is reported as C-05")
}

// assertNoEcho records a failure when the planted argv value appears on
// either stream of r.
func assertNoEcho(t *conformance.T, r *conformance.Result, planted string) {
	if bytes.Contains(r.Stdout, []byte(planted)) {
		t.Errorf("--join-secret: the argv value is echoed on stdout (4.5.14)")
	}
	if bytes.Contains(r.Stderr, []byte(planted)) {
		t.Errorf("--join-secret: the argv value is echoed on stderr (4.5.14)")
	}
}
