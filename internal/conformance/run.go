package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// runner is the state of one Run: the launcher, the describe result, the
// fixture and the report.
type runner struct {
	opts     Options
	launcher *launcher
	describe *protocol.DescribeResult
	fixture  fixture
	runID    string
	// describeTouched records what the start-of-run describe left behind:
	// an entry under its scratch principal's config or state directory, or
	// the shared directory itself. That describe is the one spawn of the
	// run guaranteed to precede every case, so C-01 can assert 4.2's "touch
	// nothing" from it whatever order the cases run in.
	describeTouched []string
}

// Run executes the suite: it resolves the adapter, creates the run
// directory, runs describe, selects and runs the cases, writes the report
// (JSON on stdout with opts.JSON; the human table on stderr always) and
// returns the exit status. environ is the suite's own environment (only
// PATH and TMPDIR are read from it); nothing here reads os.Getenv or names
// os.Stdout.
func Run(ctx context.Context, opts Options, cases []Case, environ []string, stdout, stderr io.Writer) int {
	fail := func(msg string) {
		_, _ = io.WriteString(stderr, "brigade-conformance: "+msg+"\n")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Adapter == "" {
		fail("--adapter <cmd> is required")
		return ExitUsage
	}
	if opts.Timeout < 0 {
		fail("--timeout must be positive")
		return ExitUsage
	}
	if err := validateCases(cases); err != nil {
		fail(err.Error())
		return ExitUsage
	}
	if err := validateIDs(cases, opts); err != nil {
		fail(err.Error())
		return ExitUsage
	}
	// The selection is settled BEFORE the adapter is launched, because a
	// run that selected nothing is a usage error, not a pass: it must not
	// spawn anything and must not print "0 passed, 0 failed, 0 skipped".
	selected := filterCases(cases, opts)
	if len(selected) == 0 {
		fail(emptySelection(opts).Error())
		return ExitUsage
	}
	if opts.Shuffle != 0 {
		shuffleCases(selected, opts.Shuffle)
	}

	resolved, err := exec.LookPath(opts.Adapter)
	if err != nil {
		fail("adapter not found: " + opts.Adapter)
		return ExitLauncher
	}
	runDir, err := os.MkdirTemp("", "brigade-conformance-")
	if err != nil {
		fail("run directory: " + err.Error())
		return ExitLauncher
	}
	defer func() {
		if opts.KeepTemp {
			_, _ = io.WriteString(stderr, "brigade-conformance: run directory kept: "+runDir+"\n")
			return
		}
		_ = os.RemoveAll(runDir)
	}()

	r := &runner{
		opts:     opts,
		launcher: newLauncher(resolved, opts, environ, runDir, stderr),
		runID:    newRunID(),
	}
	start := time.Now()
	report := &Report{Adapter: AdapterInfo{Command: strings.Join(r.launcher.argv(nil), " ")}, Shuffle: opts.Shuffle}

	described, err := r.runDescribe(ctx)
	if err != nil {
		fail(err.Error())
		return ExitLauncher
	}
	r.describe = described
	report.Adapter.Name, report.Adapter.Version = described.Adapter.Name, described.Adapter.Version
	report.ProtocolVersion = described.ProtocolVersion
	report.Capabilities = described.Capabilities

	code := ExitPass
	for _, sel := range decideSkips(selected, opts, described.Capabilities) {
		if sel.skip != "" {
			report.add(CaseResult{ID: sel.c.ID, Rule: sel.c.Rule, Status: StatusSkip, Reason: sel.skip})
			continue
		}
		res, launcherErr := r.runCase(ctx, sel.c)
		report.add(res)
		if launcherErr != nil {
			fail(launcherErr.Error())
			code = ExitLauncher
			break
		}
		if ctx.Err() != nil {
			fail("run cancelled")
			code = ExitLauncher
			break
		}
	}

	if hits := r.launcher.scanDir(); len(hits) > 0 {
		report.addC05(hits)
	}
	report.DurationMS = time.Since(start).Milliseconds()

	if opts.JSON {
		if err := report.WriteJSON(stdout); err != nil {
			fail("report: " + err.Error())
			return ExitLauncher
		}
	}
	report.WriteHuman(stderr)
	if code != ExitPass {
		return code
	}
	return report.exitCode()
}

// addC05 attributes the end-of-run directory scan to C-05: appended to
// its result when it ran, otherwise as a synthetic C-05 failure.
func (r *Report) addC05(hits []string) {
	reason := "run-directory scan: " + strings.Join(hits, "; ")
	for i := range r.Results {
		if r.Results[i].ID == "C-05" {
			if r.Results[i].Status != StatusFail {
				r.Summary.Fail++
				if r.Results[i].Status == StatusPass {
					r.Summary.Pass--
				} else {
					r.Summary.Skip--
				}
				r.Results[i].Status = StatusFail
				r.Results[i].Reason = reason
			} else {
				r.Results[i].Reason += "; " + reason
			}
			return
		}
	}
	r.add(CaseResult{ID: "C-05", Rule: "4.5.14 no secret on disk", Status: StatusFail, Reason: reason})
}

// runDescribe runs describe once against a scratch principal and checks
// it: ok, parseable, valid, protocol major "1". Any violation of the
// global checks is a launcher error too, because nothing later could be
// trusted.
func (r *runner) runDescribe(ctx context.Context) (*protocol.DescribeResult, error) {
	p, err := r.newPrincipal("describe", "describe")
	if err != nil {
		return nil, errors.New("run directory: " + err.Error())
	}
	res, violations := r.launcher.spawn(ctx, "describe", p, nil, nil, false, "describe")
	if len(violations) > 0 {
		return nil, errors.New("describe: " + strings.Join(violations, "; "))
	}
	r.describeTouched = touchedBy(p, r.launcher.sharedDir())
	if !res.Envelope.OK {
		return nil, errors.New("describe: " + describeError(res.Envelope.Error) + " (exit " + strconv.Itoa(res.Exit) + ")")
	}
	var d protocol.DescribeResult
	if err := protocol.Decode(res.Envelope.Result, &d); err != nil {
		return nil, errors.New("describe: result does not validate: " + err.Error())
	}
	if major, _, _ := strings.Cut(d.ProtocolVersion, "."); major != protocol.ProtocolVersion {
		return nil, errors.New("describe: protocol_version major is " + strconv.Quote(d.ProtocolVersion) + ", want " + protocol.ProtocolVersion)
	}
	if d.Capabilities == nil {
		d.Capabilities = []string{}
	}
	return &d, nil
}

// touchedBy lists what a describe on the fresh principal p left behind:
// entries under its config or state directory, or the shared directory
// (which the adapter, never the suite, creates) existing at all.
func touchedBy(p *Principal, shared string) []string {
	var out []string
	for _, d := range []struct{ what, dir string }{{"config", p.ConfigDir}, {"state", p.StateDir}} {
		if entries, err := os.ReadDir(d.dir); err == nil && len(entries) > 0 {
			out = append(out, "wrote "+strconv.Itoa(len(entries))+" entries under the "+d.what+" directory")
		}
	}
	if shared != "" {
		if _, err := os.Lstat(shared); err == nil {
			out = append(out, "created the shared directory")
		}
	}
	return out
}

// runCase runs one case body with the recovery of Fatalf, Skip, a
// launcher abort and any other panic, kills its watches and computes its
// result. A launcher abort is returned so Run stops.
func (r *runner) runCase(ctx context.Context, c Case) (res CaseResult, launcherErr error) {
	t := newT(ctx, r, c)
	r.launcher.logf(c.ID, "start: "+c.Rule)
	start := time.Now()
	func() {
		defer func() {
			switch v := recover().(type) {
			case nil, caseAbort, caseSkip:
			case launcherAbort:
				launcherErr = v.err
				t.Errorf("launcher: %v", v.err)
			default:
				t.Errorf("panic: %v\n%s", v, firstLines(string(debug.Stack()), 12))
			}
		}()
		c.Run(t)
	}()
	t.mu.Lock()
	watches := t.watches
	t.mu.Unlock()
	for _, w := range watches {
		w.reap()
	}
	res = t.outcome(time.Since(start))
	r.launcher.logf(c.ID, strings.ToUpper(string(res.Status))+" in "+time.Since(start).Round(time.Millisecond).String())
	return res, launcherErr
}

// firstLines keeps the head of a stack trace for a reason.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// newRunID returns an identifier unique to one run, sortable by time:
// 20260902T142530Z-1f4b9c02.
func newRunID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
