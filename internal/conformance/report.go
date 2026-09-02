package conformance

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"strconv"
	"strings"
)

// Status is a case outcome in the report.
type Status string

// The three outcomes of plan 9.2.
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// CaseResult is one entry of the report's `results` array.
type CaseResult struct {
	ID         string   `json:"id"`
	Rule       string   `json:"rule"`
	Status     Status   `json:"status"`
	DurationMS int64    `json:"duration_ms"`
	Reason     string   `json:"reason"`
	Notes      []string `json:"notes,omitzero"`
}

// AdapterInfo is the report's `adapter` object: name and version from
// describe, and the resolved command line that was launched.
type AdapterInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Command string `json:"command"`
}

// Summary counts the outcomes.
type Summary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Report is the --json document of 9.2:
//
//	{"adapter":{name,version,command},"protocol_version","capabilities",
//	 "results":[{id,rule,status,duration_ms,reason,notes?}],"summary":{pass,fail,skip},"duration_ms"}
//
// `rule`, `notes` and the top-level `duration_ms` are additions to the
// P1-1 stub's shape; every member the stub emitted is still emitted.
type Report struct {
	Adapter         AdapterInfo  `json:"adapter"`
	ProtocolVersion string       `json:"protocol_version"`
	Capabilities    []string     `json:"capabilities"`
	Results         []CaseResult `json:"results"`
	Summary         Summary      `json:"summary"`
	DurationMS      int64        `json:"duration_ms"`
	// Shuffle is the --shuffle seed the cases ran in, omitted when they ran
	// in id order. `results` is in execution order, so this is what makes a
	// shuffled run reproducible.
	Shuffle int64 `json:"shuffle,omitzero"`
}

// add appends one result and counts it.
func (r *Report) add(res CaseResult) {
	r.Results = append(r.Results, res)
	switch res.Status {
	case StatusPass:
		r.Summary.Pass++
	case StatusFail:
		r.Summary.Fail++
	case StatusSkip:
		r.Summary.Skip++
	}
}

// exitCode is the run's exit status from the counts alone: 1 on any
// failure, else 0. Launcher and usage errors are decided by Run.
func (r *Report) exitCode() int {
	if r.Summary.Fail > 0 {
		return ExitFail
	}
	return ExitPass
}

// WriteJSON writes the report as one indented JSON document followed by a
// newline. Required arrays are emitted as [] when empty (JSON convention
// 3), so a consumer never sees null.
func (r *Report) WriteJSON(w io.Writer) error {
	out := *r
	if out.Capabilities == nil {
		out.Capabilities = []string{}
	}
	if out.Results == nil {
		out.Results = []CaseResult{}
	}
	b, err := json.Marshal(&out, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// humanLine formats one result as the 9.2 table line:
//
//	C-30  FAIL  0.31s  4.5.3 ack idempotent — second ack: …
func humanLine(res CaseResult) string {
	var b strings.Builder
	b.WriteString(padRight(res.ID, 5))
	b.WriteString("  ")
	b.WriteString(padRight(strings.ToUpper(string(res.Status)), 4))
	b.WriteString("  ")
	b.WriteString(padLeft(strconv.FormatFloat(float64(res.DurationMS)/1000, 'f', 2, 64)+"s", 6))
	b.WriteString("  ")
	b.WriteString(res.Rule)
	if res.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(res.Reason)
	}
	return b.String()
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func padLeft(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(s)) + s
}

// WriteHuman writes the human table: a header naming the adapter, one line
// per case with its notes indented beneath it, and the totals line.
func (r *Report) WriteHuman(w io.Writer) {
	var b strings.Builder
	b.WriteString("brigade-conformance: ")
	b.WriteString(r.Adapter.Name)
	if r.Adapter.Version != "" {
		b.WriteString(" ")
		b.WriteString(r.Adapter.Version)
	}
	b.WriteString(" (")
	b.WriteString(r.Adapter.Command)
	b.WriteString("), protocol ")
	b.WriteString(r.ProtocolVersion)
	b.WriteString(", ")
	b.WriteString(strconv.Itoa(len(r.Results)))
	b.WriteString(" cases selected")
	if r.Shuffle != 0 {
		b.WriteString(", shuffled with seed ")
		b.WriteString(strconv.FormatInt(r.Shuffle, 10))
	}
	b.WriteString(".\n")
	for _, res := range r.Results {
		b.WriteString(humanLine(res))
		b.WriteString("\n")
		for _, note := range res.Notes {
			b.WriteString("               note: ")
			b.WriteString(note)
			b.WriteString("\n")
		}
	}
	b.WriteString(strconv.Itoa(r.Summary.Pass))
	b.WriteString(" passed, ")
	b.WriteString(strconv.Itoa(r.Summary.Fail))
	b.WriteString(" failed, ")
	b.WriteString(strconv.Itoa(r.Summary.Skip))
	b.WriteString(" skipped in ")
	b.WriteString(strconv.FormatFloat(float64(r.DurationMS)/1000, 'f', 2, 64))
	b.WriteString("s.\n")
	_, _ = io.WriteString(w, b.String())
}
