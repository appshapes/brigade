package conformance

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func sampleReport() *Report {
	r := &Report{
		Adapter:         AdapterInfo{Name: "brigade-adapter-fs", Version: "0.1.0", Command: "/x/fs --root /y"},
		ProtocolVersion: "1",
		Capabilities:    []string{"message.receive"},
	}
	r.add(CaseResult{ID: "C-01", Rule: "4.2 describe offline", Status: StatusPass, DurationMS: 12})
	r.add(CaseResult{ID: "C-30", Rule: "4.5.3 ack idempotent", Status: StatusFail, DurationMS: 310, Reason: "second ack: acked is empty"})
	r.add(CaseResult{ID: "C-14", Rule: "4.5.8 lease expiry", Status: StatusSkip, Reason: "slow case; run with --slow", Notes: []string{"nothing timed"}})
	r.DurationMS = 1500
	return r
}

func TestReportJSONShape(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := sampleReport().WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.String(), "\n") {
		t.Fatal("report lacks the trailing newline")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"adapter", "protocol_version", "capabilities", "results", "summary"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("report lacks %q", key)
		}
	}
	adapter := doc["adapter"].(map[string]any)
	for _, key := range []string{"name", "version", "command"} {
		if _, ok := adapter[key].(string); !ok {
			t.Errorf("adapter lacks %q", key)
		}
	}
	results := doc["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results: %d", len(results))
	}
	first := results[0].(map[string]any)
	for _, key := range []string{"id", "status", "duration_ms", "reason"} {
		if _, ok := first[key]; !ok {
			t.Errorf("result lacks %q", key)
		}
	}
	if _, ok := first["notes"]; ok {
		t.Errorf("notes emitted when empty")
	}
	if notes, ok := results[2].(map[string]any)["notes"].([]any); !ok || len(notes) != 1 {
		t.Errorf("notes missing on the noted case")
	}
	summary := doc["summary"].(map[string]any)
	if summary["pass"].(float64) != 1 || summary["fail"].(float64) != 1 || summary["skip"].(float64) != 1 {
		t.Errorf("summary: %v", summary)
	}
}

func TestReportJSONEmptyArrays(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := (&Report{}).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"capabilities": []`) || !strings.Contains(b.String(), `"results": []`) {
		t.Fatalf("empty arrays are not []:\n%s", b.String())
	}
}

func TestHumanLineFormat(t *testing.T) {
	t.Parallel()
	got := humanLine(CaseResult{ID: "C-30", Rule: "4.5.3 ack idempotent", Status: StatusFail, DurationMS: 310, Reason: "second ack: acked is empty"})
	want := "C-30   FAIL   0.31s  4.5.3 ack idempotent — second ack: acked is empty"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	got = humanLine(CaseResult{ID: "C-03b", Rule: "4.4.10 binding", Status: StatusPass, DurationMS: 12345})
	want = "C-03b  PASS  12.35s  4.4.10 binding"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestHumanTable(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	sampleReport().WriteHuman(&b)
	out := b.String()
	for _, want := range []string{
		"brigade-conformance: brigade-adapter-fs 0.1.0 (/x/fs --root /y), protocol 1, 3 cases selected.\n",
		"C-01   PASS   0.01s  4.2 describe offline\n",
		"C-14   SKIP   0.00s  4.5.8 lease expiry — slow case; run with --slow\n",
		"note: nothing timed\n",
		"1 passed, 1 failed, 1 skipped in 1.50s.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestReportExitCode(t *testing.T) {
	t.Parallel()
	if sampleReport().exitCode() != ExitFail {
		t.Fatal("a failure must exit 1")
	}
	r := &Report{}
	r.add(CaseResult{ID: "C-01", Status: StatusPass})
	r.add(CaseResult{ID: "C-14", Status: StatusSkip})
	if r.exitCode() != ExitPass {
		t.Fatal("pass plus skip must exit 0")
	}
}

func TestAddC05(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.add(CaseResult{ID: "C-01", Status: StatusPass})
	r.addC05([]string{"C-05: content of run directory file x contains a join secret"})
	if r.Summary.Fail != 1 || len(r.Results) != 2 || r.Results[1].ID != "C-05" || r.Results[1].Status != StatusFail {
		t.Fatalf("synthetic C-05: %+v", r)
	}
	r = &Report{}
	r.add(CaseResult{ID: "C-05", Status: StatusPass})
	r.addC05([]string{"hit"})
	if r.Summary.Pass != 0 || r.Summary.Fail != 1 || r.Results[0].Status != StatusFail || !strings.Contains(r.Results[0].Reason, "hit") {
		t.Fatalf("C-05 flipped: %+v", r)
	}
}
