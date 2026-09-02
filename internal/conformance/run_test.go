package conformance

import (
	"encoding/json/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// runCases is the fake case list of section 8: a passing, a failing, a
// skipping and a panicking case, plus one that needs the fixture.
func runCases() []Case {
	return []Case{
		{ID: "C-01", Rule: "pass", Tags: []string{TagCore}, Run: func(*T) {}},
		{ID: "C-02", Rule: "fail", Tags: []string{TagCore}, Run: func(t *T) { t.Errorf("wrong") }},
		{ID: "C-03", Rule: "skip", Tags: []string{TagCore}, Run: func(t *T) { t.Skip("no") }},
		{ID: "C-04", Rule: "panic", Tags: []string{TagCore}, Run: func(*T) { panic("boom") }},
		{ID: "C-05", Rule: "fixture", Tags: []string{TagCore}, Run: func(t *T) { t.A() }},
		{ID: "C-14", Rule: "slow", Tags: []string{TagCore, TagSlow}, Run: func(*T) {}},
	}
}

func run(t *testing.T, opts Options, cases []Case) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb strings.Builder
	code = Run(t.Context(), opts, cases, testEnviron, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunExitCodes(t *testing.T) {
	t.Parallel()
	adapter := fakeAdapter(t)
	for name, tc := range map[string]struct {
		opts  Options
		cases []Case
		want  int
	}{
		"pass and skip":     {Options{Adapter: adapter, Only: []string{"C-01", "C-03", "C-14"}}, runCases(), ExitPass},
		"failure":           {Options{Adapter: adapter, Only: []string{"C-01", "C-02"}}, runCases(), ExitFail},
		"panic":             {Options{Adapter: adapter, Only: []string{"C-04"}}, runCases(), ExitFail},
		"unknown id":        {Options{Adapter: adapter, Only: []string{"C-99"}}, runCases(), ExitUsage},
		"unknown skip":      {Options{Adapter: adapter, Skip: []string{"nope"}}, runCases(), ExitUsage},
		"no adapter":        {Options{}, runCases(), ExitUsage},
		"duplicate case id": {Options{Adapter: adapter}, append(runCases(), Case{ID: "c-01", Run: func(*T) {}}), ExitUsage},
		"adapter not found": {Options{Adapter: "/nonexistent/adapter-" + newRunID()}, runCases(), ExitLauncher},
		"bare name not on PATH": {
			Options{Adapter: "brigade-adapter-does-not-exist-" + newRunID()}, runCases(), ExitLauncher,
		},
		"setup exits 1": {
			Options{Adapter: adapter, Only: []string{"C-05"}, Setup: writeScript(t, "setup", "exit 1")}, runCases(), ExitLauncher,
		},
		"no caps and no setup": {Options{Adapter: adapter, Only: []string{"C-05"}}, runCases(), ExitLauncher},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, _, stderr := run(t, tc.opts, tc.cases)
			if code != tc.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, tc.want, stderr)
			}
		})
	}
}

func TestRunDescribeFailuresAreLauncherErrors(t *testing.T) {
	t.Parallel()
	notOK := writeScript(t, "notok", "printf '%s\\n' '"+usageEnvelope+"'; exit 2")
	garbage := writeScript(t, "garbage", "echo hello")
	wrongMajor := writeScript(t, "major", "printf '%s\\n' '"+strings.Replace(fakeDescribe(t), `"protocol_version":"1","adapter"`, `"protocol_version":"2","adapter"`, 1)+"'")
	for name, adapter := range map[string]string{"describe not ok": notOK, "describe unparseable": garbage, "wrong major": wrongMajor} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := run(t, Options{Adapter: adapter, JSON: true}, runCases())
			if code != ExitLauncher || !strings.Contains(stderr, "brigade-conformance: describe:") || stdout != "" {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}

func TestRunReportsOnBothStreams(t *testing.T) {
	t.Parallel()
	adapter := fakeAdapter(t, "team.create")
	code, stdout, stderr := run(t, Options{Adapter: adapter, JSON: true, Only: []string{"C-01", "C-02", "C-03", "C-14"}}, runCases())
	if code != ExitFail {
		t.Fatalf("exit %d", code)
	}
	var rep Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the JSON report: %v\n%s", err, stdout)
	}
	if rep.Adapter.Name != "fake-adapter" || rep.Adapter.Version != "9.9.9" || rep.Adapter.Command != adapter ||
		rep.ProtocolVersion != "1" || len(rep.Capabilities) != 1 || rep.Capabilities[0] != "team.create" {
		t.Fatalf("report header: %+v", rep)
	}
	if rep.Summary != (Summary{Pass: 1, Fail: 1, Skip: 2}) || len(rep.Results) != 4 {
		t.Fatalf("summary: %+v", rep)
	}
	if rep.Results[1].Reason != "wrong" || rep.Results[2].Reason != "no" || !strings.Contains(rep.Results[3].Reason, "--slow") {
		t.Fatalf("reasons: %+v", rep.Results)
	}
	for _, want := range []string{"C-01   PASS", "C-02   FAIL", "C-03   SKIP", "C-14   SKIP", "1 passed, 1 failed, 2 skipped"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("human table lacks %q:\n%s", want, stderr)
		}
	}
	// Without --json, stdout stays empty.
	_, stdout, _ = run(t, Options{Adapter: adapter, Only: []string{"C-01"}}, runCases())
	if stdout != "" {
		t.Fatalf("stdout without --json: %q", stdout)
	}
}

func TestRunDirectoryIsRemovedOrKept(t *testing.T) {
	t.Parallel()
	adapter := fakeAdapter(t)
	var dir string
	capture := []Case{{ID: "C-01", Tags: []string{TagCore}, Run: func(t *T) { dir = t.RunDir() }}}
	if code, _, _ := run(t, Options{Adapter: adapter}, capture); code != ExitPass || dir == "" {
		t.Fatal("run failed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("run directory %s survived: %v", dir, err)
	}
	code, _, stderr := run(t, Options{Adapter: adapter, KeepTemp: true}, capture)
	if code != ExitPass || !strings.Contains(stderr, "run directory kept: "+dir) {
		t.Fatalf("keep-temp: exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("kept directory missing: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRunDirectoryScanFailsC05(t *testing.T) {
	t.Parallel()
	adapter := fakeAdapter(t)
	plant := []Case{{ID: "C-01", Tags: []string{TagCore}, Run: func(t *T) {
		p := t.Scratch("x")
		if err := writeFile(p.StateDir+"/leak.log", "brg1.leak.ed\n"); err != nil {
			t.Fatalf("%v", err)
		}
	}}}
	code, stdout, _ := run(t, Options{Adapter: adapter, JSON: true}, plant)
	var rep Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if code != ExitFail || len(rep.Results) != 2 || rep.Results[1].ID != "C-05" || rep.Results[1].Status != StatusFail ||
		!strings.Contains(rep.Results[1].Reason, "leak.log") || strings.Contains(rep.Results[1].Reason, "leak.ed") {
		t.Fatalf("exit %d results %+v", code, rep.Results)
	}
}

func TestRunSetupProvisionsTheFixture(t *testing.T) {
	t.Parallel()
	// An adapter whose describe says joined once --setup has written a
	// marker, and whose session register answers a record.
	joined := strings.Replace(fakeDescribe(t), `"profile":{"name":"default","state":"unconfigured"}`,
		`"profile":{"name":"default","state":"joined","team_ref":"'"$(cat "$BRIGADE_CONFIG_DIR/team")"'","team_name":"ops","principal_ref":"'"$BRIGADE_CONFIG_DIR"'","human_label":"x@example.com"}`, 1)
	record := `{"ok":true,"protocol_version":"1","result":{"session_id":"'"$BRIGADE_CONFIG_DIR"'","session_name":"fixture","principal_ref":"p","state":"active","activity":"busy","inbound":"accept","last_seen_at":"2026-08-30T12:00:00Z","lease_until":"2026-08-30T12:01:30Z","created_at":"2026-08-30T12:00:00Z","is_self":false,"resumed":false,"lease_seconds":90,"server_time":"2026-08-30T12:00:00Z"}}`
	adapter := writeScript(t, "adapter", `
case "$1 $2" in
  "describe ") if [ -f "$BRIGADE_CONFIG_DIR/team" ]; then printf '%s\n' '`+joined+`'; else printf '%s\n' '`+fakeDescribe(t)+`'; fi ;;
  "session register") printf '%s\n' '`+record+`' ;;
  *) printf '%s\n' '`+usageEnvelope+`'; exit 2 ;;
esac
`)
	// The setup script binds a and b to t1 and c to t2 by directory name.
	setup := writeScript(t, "setup", `
case "$BRIGADE_CONFIG_DIR" in
  */principals/c/*) printf t2 > "$BRIGADE_CONFIG_DIR/team" ;;
  *) printf t1 > "$BRIGADE_CONFIG_DIR/team" ;;
esac
`)
	var a, b, c *Principal
	var secret string
	cases := []Case{{ID: "C-10", Tags: []string{TagCore}, Run: func(t *T) {
		a, b, c = t.A(), t.B(), t.C()
		secret = t.JoinSecret()
		if t.Session(a) != a.ConfigDir {
			t.Errorf("fixture session of a: %q", t.Session(a))
		}
	}}}
	code, _, stderr := run(t, Options{Adapter: adapter, Setup: setup}, cases)
	if code != ExitPass {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if a.TeamRef != "t1" || b.TeamRef != "t1" || c.TeamRef != "t2" || a.PrincipalRef != a.ConfigDir || secret != "" {
		t.Fatalf("fixture: a %+v b %+v c %+v secret %q", a, b, c, secret)
	}
}

// TestDescribeTouchedIsRecorded pins the seam C-01 reads in any case
// order: what the start-of-run describe left behind on its scratch
// principal (here a file under the config directory and the shared
// directory) reaches the case, and a clean describe reports nothing.
func TestDescribeTouchedIsRecorded(t *testing.T) {
	t.Parallel()
	touching := writeScript(t, "touching", `
case "$1" in
  describe) mkdir -p "$BRIGADE_CONFIG_DIR/profiles" "$BRIGADE_FS_ROOT"; printf '%s\n' '`+fakeDescribe(t)+`' ;;
  *) printf '%s\n' '`+usageEnvelope+`'; exit 2 ;;
esac
`)
	var touched []string
	capture := []Case{{ID: "C-01", Tags: []string{TagCore}, Run: func(t *T) { touched = t.DescribeTouched() }}}
	if code, _, stderr := run(t, Options{Adapter: touching, SharedEnv: "BRIGADE_FS_ROOT"}, capture); code != ExitPass {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if len(touched) != 2 || !strings.Contains(touched[0], "config directory") || touched[1] != "created the shared directory" {
		t.Fatalf("DescribeTouched: %v", touched)
	}
	touched = nil
	if code, _, stderr := run(t, Options{Adapter: fakeAdapter(t), SharedEnv: "BRIGADE_FS_ROOT"}, capture); code != ExitPass {
		t.Fatalf("clean describe: exit %d\n%s", code, stderr)
	}
	if len(touched) != 0 {
		t.Fatalf("clean describe reported %v", touched)
	}
}

// TestRunEmptySelectionIsUsage: a run whose selectors matched NO case must
// not read as a pass. `--tags cap:nosuch` used to select nothing and print
// "0 passed, 0 failed, 0 skipped" with exit 0; it is now a usage error
// (exit 2) whose message names the selectors that produced it, decided
// before the adapter is resolved — the adapter here does not exist, so an
// exit of 3 would prove the check came too late. A selection that matches
// only a case that will be reported SKIP is NOT empty and still exits 0.
func TestRunEmptySelectionIsUsage(t *testing.T) {
	t.Parallel()
	missing := "/nonexistent/adapter-" + newRunID()
	for name, tc := range map[string]struct {
		opts  Options
		want  int
		names []string
	}{
		"unknown capability tag": {Options{Adapter: missing, Tags: []string{"cap:nosuch"}}, ExitUsage, []string{"--tags cap:nosuch"}},
		"only cancelled by skip": {
			Options{Adapter: missing, Only: []string{"C-01"}, Skip: []string{"C-01"}}, ExitUsage,
			[]string{"--only C-01", "--skip C-01"},
		},
		"tags cancelled by skip": {
			Options{Adapter: missing, Tags: []string{"slow"}, Skip: []string{"C-14"}}, ExitUsage,
			[]string{"--tags slow", "--skip C-14"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, _, stderr := run(t, tc.opts, runCases())
			if code != tc.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, tc.want, stderr)
			}
			for _, want := range tc.names {
				if !strings.Contains(stderr, want) {
					t.Errorf("the message does not name %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// TestRunSkipOnlySelectionPasses: a selection of one case that is reported
// SKIP is a real selection — `--tags slow` without --slow selects C-14 —
// and exits 0 with that skip counted.
func TestRunSkipOnlySelectionPasses(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, Options{Adapter: fakeAdapter(t), JSON: true, Tags: []string{"slow"}}, runCases())
	if code != ExitPass {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	var rep Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary != (Summary{Skip: 1}) || len(rep.Results) != 1 || rep.Results[0].ID != "C-14" {
		t.Fatalf("summary %+v results %+v", rep.Summary, rep.Results)
	}
}

// orderCases builds n fake cases that append their id to order when they
// run, so a test can read back the order Run executed them in.
func orderCases(n int, order *[]string) []Case {
	cases := make([]Case, 0, n)
	for i := 1; i <= n; i++ {
		id := "C-" + strconv.Itoa(i+10)
		cases = append(cases, Case{ID: id, Rule: "r", Tags: []string{TagCore}, Run: func(*T) {
			*order = append(*order, id)
		}})
	}
	return cases
}

// TestRunShuffleIsDeterministic pins --shuffle: seed 0 is id order, one
// seed always names the same permutation, two seeds name different ones,
// and the seed reaches both reports so a shuffled failure is reproducible.
func TestRunShuffleIsDeterministic(t *testing.T) {
	t.Parallel()
	adapter := fakeAdapter(t)
	runOrder := func(seed int64) ([]string, Report, string) {
		var order []string
		code, stdout, stderr := run(t, Options{Adapter: adapter, JSON: true, Shuffle: seed}, orderCases(10, &order))
		if code != ExitPass {
			t.Fatalf("seed %d: exit %d\n%s", seed, code, stderr)
		}
		var rep Report
		if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		return order, rep, stderr
	}
	idOrder := strings.Join([]string{"C-11", "C-12", "C-13", "C-14", "C-15", "C-16", "C-17", "C-18", "C-19", "C-20"}, " ")

	zero, zeroRep, zeroErr := runOrder(0)
	if strings.Join(zero, " ") != idOrder {
		t.Fatalf("seed 0 ran %v, want id order", zero)
	}
	if zeroRep.Shuffle != 0 || strings.Contains(zeroErr, "shuffled with seed") {
		t.Fatalf("seed 0 reported a shuffle: %d\n%s", zeroRep.Shuffle, zeroErr)
	}

	first, rep, stderr := runOrder(42)
	second, _, _ := runOrder(42)
	if strings.Join(first, " ") != strings.Join(second, " ") {
		t.Fatalf("seed 42 is not deterministic: %v then %v", first, second)
	}
	if strings.Join(first, " ") == idOrder {
		t.Fatalf("seed 42 left the cases in id order: %v", first)
	}
	if len(first) != 10 {
		t.Fatalf("seed 42 ran %d cases, want 10: %v", len(first), first)
	}
	if rep.Shuffle != 42 || !strings.Contains(stderr, "shuffled with seed 42") {
		t.Fatalf("the seed is not reported: %d\n%s", rep.Shuffle, stderr)
	}
	// The report's `results` are in EXECUTION order, which is what makes a
	// shuffled run reproducible from the seed.
	var reported []string
	for _, res := range rep.Results {
		reported = append(reported, res.ID)
	}
	if strings.Join(reported, " ") != strings.Join(first, " ") {
		t.Fatalf("results order %v, ran %v", reported, first)
	}
	// A different seed is a different permutation (10! makes a collision a
	// one-in-3.6-million accident, not a flake).
	other, _, _ := runOrder(43)
	if strings.Join(other, " ") == strings.Join(first, " ") {
		t.Fatalf("seeds 42 and 43 name the same order: %v", other)
	}
	// Every case still ran exactly once.
	seen := map[string]bool{}
	for _, id := range first {
		if seen[id] {
			t.Fatalf("%s ran twice: %v", id, first)
		}
		seen[id] = true
	}
}
