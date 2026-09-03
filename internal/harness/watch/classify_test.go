package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestClassifyIsTheRestartStopMap pins 6.6's map through
// backoff.RetryableExit (U-16, the watch half): exit 8 and 9, a signal
// death, an unowned status, an unasked exit 0 and a missing ready are
// restarted; 4, 5, 10, 11 and every other non-retryable code stop the
// watcher; invalid_input, unauthorized and loop_detected are never
// retried whether they arrive as an exit status, a start error or a fatal
// error event.
func TestClassifyIsTheRestartStopMap(t *testing.T) {
	t.Parallel()
	perr := func(code protocol.Code) error { return &protocol.Error{Code: code, Message: "x"} }
	cases := []struct {
		name string
		r    attemptResult
		stop bool
		code protocol.Code
		why  string
	}{
		{"exit 8 restarts", attemptResult{exitCode: 8}, false, protocol.CodeRateLimited, "child_exit"},
		{"exit 9 restarts", attemptResult{exitCode: 9}, false, protocol.CodeUnavailable, "child_exit"},
		{"exit 4 stops", attemptResult{exitCode: 4}, true, protocol.CodeUnauthenticated, "child_exit"},
		{"exit 5 stops", attemptResult{exitCode: 5}, true, protocol.CodeUnauthorized, "child_exit"},
		{"exit 10 stops", attemptResult{exitCode: 10}, true, protocol.CodeProtocolMismatch, "child_exit"},
		{"exit 11 stops", attemptResult{exitCode: 11}, true, protocol.CodeConfig, "child_exit"},
		{"exit 3 stops", attemptResult{exitCode: 3}, true, protocol.CodeInvalidInput, "child_exit"},
		{"exit 12 stops", attemptResult{exitCode: 12}, true, protocol.CodeLoopDetected, "child_exit"},
		{"exit 1 stops", attemptResult{exitCode: 1}, true, protocol.CodeInternal, "child_exit"},
		{"exit 127 restarts", attemptResult{exitCode: 127}, false, protocol.CodeInternal, "child_exit"},
		{"signal death restarts", attemptResult{exitCode: -1}, false, protocol.CodeUnavailable, "child_signal"},
		{"unasked exit 0 restarts", attemptResult{exitCode: 0}, false, protocol.CodeUnavailable, "child_ended"},
		{"exit 0 after a fatal event stops", attemptResult{exitCode: 0, lastErrorCode: protocol.CodeUnauthorized}, true, protocol.CodeUnauthorized, "error_then_exit"},
		{"exit 0 after a retryable event restarts", attemptResult{exitCode: 0, lastErrorCode: protocol.CodeUnavailable}, false, protocol.CodeUnavailable, "child_ended"},
		{"ready timeout restarts", attemptResult{readyTimeout: true}, false, protocol.CodeUnavailable, "ready_timeout"},
		{"fatal unauthenticated stops", attemptResult{fatalCode: protocol.CodeUnauthenticated, exitCode: 0}, true, protocol.CodeUnauthenticated, "error_event"},
		{"fatal invalid_input stops", attemptResult{fatalCode: protocol.CodeInvalidInput}, true, protocol.CodeInvalidInput, "error_event"},
		{"fatal loop_detected stops", attemptResult{fatalCode: protocol.CodeLoopDetected}, true, protocol.CodeLoopDetected, "error_event"},
		{"fatal rate_limited restarts", attemptResult{fatalCode: protocol.CodeRateLimited}, false, protocol.CodeRateLimited, "error_event"},
		{"start unavailable restarts", attemptResult{startErr: perr(protocol.CodeUnavailable)}, false, protocol.CodeUnavailable, "start_failed"},
		{"start protocol_mismatch stops", attemptResult{startErr: perr(protocol.CodeProtocolMismatch)}, true, protocol.CodeProtocolMismatch, "start_refused"},
		{"start config stops", attemptResult{startErr: perr(protocol.CodeConfig)}, true, protocol.CodeConfig, "start_refused"},
		{"start unclassified error stops as internal", attemptResult{startErr: errBoom}, true, protocol.CodeInternal, "start_refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := classify(tc.r)
			if v.stop != tc.stop || v.code != tc.code || v.why != tc.why {
				t.Fatalf("classify(%+v) = %+v, want stop=%v code=%s why=%s", tc.r, v, tc.stop, tc.code, tc.why)
			}
			// The exit-status rows agree with the library predicate.
			if tc.r.exitCode != 0 && tc.r.fatalCode == "" && tc.r.startErr == nil && !tc.r.readyTimeout {
				if got := backoff.RetryableExit(tc.r.exitCode); got == v.stop {
					t.Fatalf("RetryableExit(%d) = %v but classify said stop=%v", tc.r.exitCode, got, v.stop)
				}
			}
		})
	}
}

type boom struct{}

func (boom) Error() string { return "boom" }

var errBoom = boom{}

func TestStopNoticeWording(t *testing.T) {
	t.Parallel()
	for code, want := range map[protocol.Code]string{
		protocol.CodeUnauthenticated:  "Brigade: watcher stopped: unauthenticated; run `brigade team join` again in a terminal",
		protocol.CodeUnauthorized:     "Brigade: watcher stopped: unauthorized; run `brigade team join` again in a terminal",
		protocol.CodeProtocolMismatch: "Brigade: watcher stopped: protocol_mismatch; the adapter speaks a different protocol version than this plugin; update one of them",
		protocol.CodeConfig:           "Brigade: watcher stopped: config; run `brigade profile status` in a terminal",
		protocol.CodeInternal:         "Brigade: watcher stopped: internal; it will be restarted at your next prompt",
	} {
		if got := stopNotice(code); got != want {
			t.Errorf("stopNotice(%s) = %q, want %q", code, got, want)
		}
		if strings.ContainsAny(stopNotice(code), "\n\r") {
			t.Errorf("stopNotice(%s) is not one line", code)
		}
	}
	if got := giveUpNotice(10, 5*time.Minute); got != "Brigade: watcher gave up after 10 failures within 5m0s; it will be restarted at your next prompt" {
		t.Errorf("giveUpNotice = %q", got)
	}
}

func TestPruneFailuresKeepsTheWindow(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var fs []time.Time
	for i := range 12 {
		fs = append(fs, base.Add(time.Duration(i)*time.Minute))
	}
	kept := pruneFailures(fs, base.Add(7*time.Minute))
	if len(kept) != 5 || !kept[0].Equal(base.Add(7*time.Minute)) {
		t.Fatalf("kept %d from cutoff, first %v", len(kept), kept[0])
	}
	if got := pruneFailures(nil, base); len(got) != 0 {
		t.Fatalf("prune(nil) = %v", got)
	}
}
