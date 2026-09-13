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

// flockHangCatcher bounds every wait on the child processes here — a
// holder signalling ready, a peer reaching the rendezvous — and the
// children's own waits on files the parent writes. Nothing timed here is
// a claim; the claims are the lock's outcomes and the recorded spans.
const flockHangCatcher = 30 * time.Second

// waitForFile polls for path until it exists or the hang catcher fires.
func waitForFile(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(flockHangCatcher)
	for {
		if _, err := os.Stat(path); err == nil { //nolint:gosec // G703: a file under the test's own directory, named by the parent
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s did not appear within %v", what, path, flockHangCatcher)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// touch creates path (the parent's signal to a child).
func touch(t *testing.T, path string) {
	t.Helper()
	if err := adapterkit.WriteAtomic(path, []byte("go\n")); err != nil {
		t.Fatal(err)
	}
}

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
	// LockFile never gives up before its timeout: the deadline is taken
	// inside the call, so the wall time around it is at least the timeout
	// on any machine. How much longer it took is the scheduler's (a 2 s
	// upper bound here, and a 1 s one on the re-acquisition below, were
	// stopwatches), so it is only logged.
	if elapsed < 50*time.Millisecond {
		t.Fatalf("bounded wait gave up after %v, before its 50ms timeout", elapsed)
	}
	t.Logf("50ms lock timeout fired after %v", elapsed)
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	// After release the lock is acquirable again (a leaked descriptor
	// would make this wait the whole default timeout and then fail).
	l2, err := adapterkit.LockFile(path, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatalf("re-acquisition after Unlock: %v", err)
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

// The contention the race children aim for: a child stops once it has
// waited for the lock this many times (or once its peer has), so the
// parent's vacuity guard is met by construction rather than by two fixed
// loops happening to overlap on a loaded machine. contendedWait sits
// below any contended acquisition (at least one 5 ms lockRetryInterval)
// and above an uncontended one; raceMaxIters is a hang catcher by count,
// ~50 s of 10–20 ms holds.
const (
	wantContended = 5
	contendedWait = 2 * time.Millisecond
	raceMaxIters  = 2000
)

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
		// Hold the lock until the parent writes the release file: the
		// parent's LockFile is then refused for as long as the parent
		// chooses, never for a fixed number of seconds the parent must
		// beat with its own scheduling.
		release := os.Getenv("ADAPTERKIT_FLOCK_RELEASE")
		l, err := adapterkit.LockFile(lockPath, adapterkit.DefaultLockTimeout)
		if err != nil {
			t.Fatalf("child hold: %v", err)
		}
		if err := adapterkit.WriteAtomic(outPath, []byte("locked\n")); err != nil {
			t.Fatalf("child hold ready: %v", err)
		}
		waitForFile(t, release, "release")
		if err := l.Unlock(); err != nil {
			t.Fatalf("child hold unlock: %v", err)
		}
	case "race":
		seed, err := strconv.Atoi(os.Getenv("ADAPTERKIT_FLOCK_SEED"))
		if err != nil {
			t.Fatalf("bad ADAPTERKIT_FLOCK_SEED: %v", err)
		}
		ready, start := os.Getenv("ADAPTERKIT_FLOCK_READY"), os.Getenv("ADAPTERKIT_FLOCK_START")
		done, peerDone := os.Getenv("ADAPTERKIT_FLOCK_DONE"), os.Getenv("ADAPTERKIT_FLOCK_PEER_DONE")
		// Rendezvous: both children are up before either starts, so a
		// child that took seconds to start under load (a -race binary
		// re-exec'd on a loaded machine) does not find its peer finished.
		if err := adapterkit.WriteAtomic(ready, []byte("ready\n")); err != nil {
			t.Fatalf("child race ready: %v", err)
		}
		waitForFile(t, start, "start")
		// Deterministic per-seed jitter, not math/rand: it only has to
		// desynchronise the two children enough to contend.
		var b strings.Builder
		contended := 0
		for i := 0; ; i++ {
			if i >= raceMaxIters {
				t.Fatalf("child race: %d contended acquisitions in %d iterations; the peer never contended", contended, i)
			}
			if _, err := os.Stat(peerDone); err == nil { //nolint:gosec // G703: the peer's done file under the test's directory, named by the parent
				break
			}
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
			if acq.Sub(t0) > contendedWait {
				contended++
			}
			if contended >= wantContended {
				if err := adapterkit.WriteAtomic(done, []byte("done\n")); err != nil {
					t.Fatalf("child race done: %v", err)
				}
				break
			}
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
// must never overlap; the contended waits are logged, and E0-6's fix (a
// 5 ms retry, not the 100 ms poll that rounded every contention up to
// ≥ 100.2 ms) is pinned on the constant by TestLockRetryIntervalIsE06sFix
// rather than on a stopwatch across two scheduled processes. The children
// rendezvous before they start and loop until one of them has contended
// wantContended times, so the vacuity guard below is met by construction
// (two fixed 40-iteration loops had to overlap in time to meet it).
func TestFlockExclusiveBetweenTwoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock := filepath.Join(dir, "session.json.lock")
	start := filepath.Join(dir, "start")

	outs := []string{filepath.Join(dir, "a.spans"), filepath.Join(dir, "b.spans")}
	readies := []string{filepath.Join(dir, "a.ready"), filepath.Join(dir, "b.ready")}
	dones := []string{filepath.Join(dir, "a.done"), filepath.Join(dir, "b.done")}
	cmds := make([]*exec.Cmd, len(outs))
	stderrs := make([]*bytes.Buffer, len(outs))
	for i, out := range outs {
		//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildFlock$")
		cmd.Env = append(os.Environ(),
			"ADAPTERKIT_FLOCK_CHILD=race",
			"ADAPTERKIT_FLOCK_PATH="+lock,
			"ADAPTERKIT_FLOCK_OUT="+out,
			"ADAPTERKIT_FLOCK_READY="+readies[i],
			"ADAPTERKIT_FLOCK_START="+start,
			"ADAPTERKIT_FLOCK_DONE="+dones[i],
			"ADAPTERKIT_FLOCK_PEER_DONE="+dones[1-i],
			"ADAPTERKIT_FLOCK_SEED="+strconv.Itoa(7919+i*104729),
		)
		stderrs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = stderrs[i], stderrs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for _, ready := range readies {
		waitForFile(t, ready, "child ready")
	}
	touch(t, start)
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
		if len(lines) == 0 || lines[0] == "" {
			t.Fatalf("child %d recorded no spans", child)
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
	// refuse to pass vacuously (the P1-2 lesson). The children stop only
	// once one of them counted wantContended contended waits, so this is
	// a check on the recording, not on the scheduler.
	var contended []time.Duration
	for _, s := range spans {
		if s.wait > contendedWait {
			contended = append(contended, s.wait)
		}
	}
	if len(contended) < wantContended {
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
	// contended acquisition rounded up to the 100 ms poll quantum. A
	// median under 50 ms was asserted here as its witness, but each wait is
	// the holder's 10–20 ms hold plus up to one retry plus whatever the
	// scheduler adds to two children — a performance measurement on a
	// nondeterministic input — so the retry interval is asserted on the
	// constant instead and the numbers above are only logged.
}

// TestFlockTimeoutBoundBetweenTwoProcesses proves the 10 s `unavailable`
// bound of 5.1 against a REAL holder in another process, the way E0-6
// proved it live with a 13 s holder — except that this holder keeps the
// lock until the parent releases it, AFTER the bound has fired. A fixed
// 13 s hold gave the parent 3 s to get from the holder's ready file to
// its own LockFile, or the lock would simply succeed; and a 12.5 s upper
// bound on the refusal was a stopwatch. The refusal and its lower bound
// (LockFile never gives up early: its deadline is taken inside the call)
// are the claims; the wall time is logged.
func TestFlockTimeoutBoundBetweenTwoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock := filepath.Join(dir, "session.json.lock")
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")

	var childOut bytes.Buffer
	//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildFlock$")
	cmd.Env = append(os.Environ(),
		"ADAPTERKIT_FLOCK_CHILD=hold",
		"ADAPTERKIT_FLOCK_PATH="+lock,
		"ADAPTERKIT_FLOCK_OUT="+ready,
		"ADAPTERKIT_FLOCK_RELEASE="+release,
	)
	cmd.Stdout, cmd.Stderr = &childOut, &childOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = adapterkit.WriteAtomic(release, []byte("go\n"))
		}
		_ = cmd.Wait()
	}()
	waitForFile(t, ready, "holder ready")

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
	if perr.Details["reason"] != "lock_timeout" {
		t.Fatalf("details = %v, want reason=lock_timeout", perr.Details)
	}
	if elapsed < adapterkit.DefaultLockTimeout {
		t.Fatalf("bound fired after %v, before its %v", elapsed, adapterkit.DefaultLockTimeout)
	}
	t.Logf("10s bound fired after %v against a holder in another process", elapsed)

	// Released, the holder unlocks and exits 0, and the lock is free.
	touch(t, release)
	released = true
	if err := cmd.Wait(); err != nil {
		t.Fatalf("holder child: %v\n%s", err, childOut.String())
	}
	l, err := adapterkit.LockFile(lock, adapterkit.DefaultLockTimeout)
	if err != nil {
		t.Fatalf("after the holder's release: %v", err)
	}
	_ = l.Unlock()
}
