//go:build darwin || linux

package adapterkit_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

func TestSidecarPath(t *testing.T) {
	t.Parallel()
	if got := adapterkit.SidecarPath("/a/session.json"); got != "/a/session.json.lock" {
		t.Fatalf("SidecarPath = %q", got)
	}
}

func TestLockFileCreatesSidecar0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.json.lock")
	l, err := adapterkit.LockFile(path, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Unlock() }()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestLockFileTimeoutSameProcess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "l.lock")
	held, err := adapterkit.LockFile(path, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = adapterkit.LockFile(path, 50*time.Millisecond)
	elapsed := time.Since(start)
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeUnavailable {
		t.Fatalf("code = %q, want unavailable", perr.Code)
	}
	if exit := perr.Code.Exit(); exit != 9 {
		t.Fatalf("exit = %d, want 9", exit)
	}
	if perr.Details["reason"] != "lock_timeout" {
		t.Fatalf("details = %v, want reason=lock_timeout", perr.Details)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("bounded wait took %v, want roughly the 50ms timeout", elapsed)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	// After release the lock must be immediately acquirable again.
	start = time.Now()
	l2, err := adapterkit.LockFile(path, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if e := time.Since(start); e > time.Second {
		t.Fatalf("uncontended acquisition took %v", e)
	}
	_ = l2.Unlock()
}

func TestUnlockIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "l.lock")
	l, err := adapterkit.LockFile(path, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
	var zero adapterkit.FileLock
	if err := zero.Unlock(); err != nil {
		t.Fatalf("zero-value Unlock: %v", err)
	}
}

// TestHelperChildFlock is not a test: it is the child half of the
// two-process lock tests, selected by ADAPTERKIT_FLOCK_CHILD.
func TestHelperChildFlock(t *testing.T) {
	mode := os.Getenv("ADAPTERKIT_FLOCK_CHILD")
	if mode == "" {
		t.Skip("child-process helper; run by the two-process lock tests")
	}
	lockPath := os.Getenv("ADAPTERKIT_FLOCK_PATH")
	outPath := os.Getenv("ADAPTERKIT_FLOCK_OUT")
	switch mode {
	case "hold":
		holdMS, err := strconv.Atoi(os.Getenv("ADAPTERKIT_FLOCK_HOLD_MS"))
		if err != nil {
			t.Fatalf("bad ADAPTERKIT_FLOCK_HOLD_MS: %v", err)
		}
		l, err := adapterkit.LockFile(lockPath, adapterkit.DefaultLockTimeout)
		if err != nil {
			t.Fatalf("child hold: %v", err)
		}
		if err := adapterkit.WriteAtomic(outPath, []byte("locked\n")); err != nil {
			t.Fatalf("child hold ready: %v", err)
		}
		time.Sleep(time.Duration(holdMS) * time.Millisecond)
		if err := l.Unlock(); err != nil {
			t.Fatalf("child hold unlock: %v", err)
		}
	case "race":
		iters, err := strconv.Atoi(os.Getenv("ADAPTERKIT_FLOCK_ITERS"))
		if err != nil {
			t.Fatalf("bad ADAPTERKIT_FLOCK_ITERS: %v", err)
		}
		seed, err := strconv.Atoi(os.Getenv("ADAPTERKIT_FLOCK_SEED"))
		if err != nil {
			t.Fatalf("bad ADAPTERKIT_FLOCK_SEED: %v", err)
		}
		// Deterministic per-seed jitter, not math/rand: it only has to
		// desynchronise the two children enough to contend.
		var b strings.Builder
		for i := 0; i < iters; i++ {
			time.Sleep(time.Duration((i*seed+3)%10) * time.Millisecond)
			t0 := time.Now()
			l, err := adapterkit.LockFile(lockPath, adapterkit.DefaultLockTimeout)
			if err != nil {
				t.Fatalf("child race iteration %d: %v", i, err)
			}
			acq := time.Now()
			time.Sleep(time.Duration(10+(i*seed+7)%10) * time.Millisecond)
			rel := time.Now()
			if err := l.Unlock(); err != nil {
				t.Fatalf("child race unlock %d: %v", i, err)
			}
			fmt.Fprintf(&b, "%d %d %d\n", acq.Sub(t0).Nanoseconds(), acq.UnixNano(), rel.UnixNano())
		}
		if err := adapterkit.WriteAtomic(outPath, []byte(b.String())); err != nil {
			t.Fatalf("child race out: %v", err)
		}
	default:
		t.Fatalf("unknown child mode %q", mode)
	}
}

type lockSpan struct {
	wait     time.Duration
	acq, rel int64
	child    int
}

// TestFlockExclusiveBetweenTwoProcesses races two REAL child processes
// over one sidecar. Two goroutines would prove nothing: flock is
// per-open-file-description, so only a process boundary exercises the
// property the credential file depends on. The parsed acquisition spans
// must never overlap, and the measured contended wait must show E0-6's
// fix (a 5 ms retry, not the 100 ms poll that rounded every contention up
// to ≥ 100.2 ms).
func TestFlockExclusiveBetweenTwoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock := filepath.Join(dir, "session.json.lock")
	const iters = 40

	outs := []string{filepath.Join(dir, "a.spans"), filepath.Join(dir, "b.spans")}
	cmds := make([]*exec.Cmd, len(outs))
	stderrs := make([]*bytes.Buffer, len(outs))
	for i, out := range outs {
		//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildFlock$")
		cmd.Env = append(os.Environ(),
			"ADAPTERKIT_FLOCK_CHILD=race",
			"ADAPTERKIT_FLOCK_PATH="+lock,
			"ADAPTERKIT_FLOCK_OUT="+out,
			"ADAPTERKIT_FLOCK_ITERS="+strconv.Itoa(iters),
			"ADAPTERKIT_FLOCK_SEED="+strconv.Itoa(7919+i*104729),
		)
		stderrs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = stderrs[i], stderrs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, stderrs[i].String())
		}
	}

	var spans []lockSpan
	for child, out := range outs {
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != iters {
			t.Fatalf("child %d recorded %d spans, want %d", child, len(lines), iters)
		}
		for _, line := range lines {
			var wait, acq, rel int64
			if _, err := fmt.Sscanf(line, "%d %d %d", &wait, &acq, &rel); err != nil {
				t.Fatalf("child %d span %q: %v", child, line, err)
			}
			spans = append(spans, lockSpan{wait: time.Duration(wait), acq: acq, rel: rel, child: child})
		}
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].acq < spans[j].acq })
	for i := 1; i < len(spans); i++ {
		if spans[i].acq < spans[i-1].rel {
			t.Fatalf("mutual exclusion broken: child %d acquired at %d while child %d held until %d (overlap %v)",
				spans[i].child, spans[i].acq, spans[i-1].child, spans[i-1].rel,
				time.Duration(spans[i-1].rel-spans[i].acq))
		}
	}

	// The latency half. Without contention the test measured nothing —
	// refuse to pass vacuously (the P1-2 lesson).
	var contended []time.Duration
	for _, s := range spans {
		if s.wait > 2*time.Millisecond {
			contended = append(contended, s.wait)
		}
	}
	if len(contended) < 5 {
		t.Fatalf("only %d of %d acquisitions contended; the latency claim was not exercised", len(contended), len(spans))
	}
	sort.Slice(contended, func(i, j int) bool { return contended[i] < contended[j] })
	var sum time.Duration
	for _, d := range contended {
		sum += d
	}
	median := contended[len(contended)/2]
	t.Logf("flock contention over two processes: %d/%d contended, min %v median %v max %v mean %v",
		len(contended), len(spans), contended[0], median, contended[len(contended)-1], sum/time.Duration(len(contended)))

	// E0-6's signature was min 100.2 ms / median 101.1 ms: every
	// contended acquisition rounded up to the 100 ms poll quantum. With
	// the 5 ms retry and 10-20 ms holds the median must sit far below
	// that quantum; the generous bound keeps a loaded CI honest without
	// flaking.
	if median >= 50*time.Millisecond {
		t.Fatalf("median contended wait %v has the poll-quantum signature E0-6 forbids (≥ 50ms)", median)
	}
}

// TestFlockTimeoutBoundBetweenTwoProcesses proves the 10 s `unavailable`
// bound of 5.1 against a REAL holder in another process, the way E0-6
// proved it live with a 13 s holder.
func TestFlockTimeoutBoundBetweenTwoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock := filepath.Join(dir, "session.json.lock")
	ready := filepath.Join(dir, "ready")

	var childOut bytes.Buffer
	//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildFlock$")
	cmd.Env = append(os.Environ(),
		"ADAPTERKIT_FLOCK_CHILD=hold",
		"ADAPTERKIT_FLOCK_PATH="+lock,
		"ADAPTERKIT_FLOCK_OUT="+ready,
		"ADAPTERKIT_FLOCK_HOLD_MS=13000",
	)
	cmd.Stdout, cmd.Stderr = &childOut, &childOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()

	waitStart := time.Now()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Since(waitStart) > 10*time.Second {
			t.Fatalf("holder child never signalled ready:\n%s", childOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	_, err := adapterkit.LockFile(lock, adapterkit.DefaultLockTimeout)
	elapsed := time.Since(start)
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeUnavailable {
		t.Fatalf("code = %q, want unavailable", perr.Code)
	}
	if exit := perr.Code.Exit(); exit != 9 {
		t.Fatalf("exit = %d, want 9", exit)
	}
	if elapsed < adapterkit.DefaultLockTimeout-100*time.Millisecond || elapsed > adapterkit.DefaultLockTimeout+2500*time.Millisecond {
		t.Fatalf("bound fired after %v, want about %v", elapsed, adapterkit.DefaultLockTimeout)
	}
	t.Logf("10s bound fired after %v against a 13s holder in another process", elapsed)
}
