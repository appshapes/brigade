//go:build !windows

package rest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// A command that writes a profile's credential holds the profile's lock for
// its whole length (work item 337). Two `team take-in` of one profile at
// once could cross before: each read the profile, asked the server, and
// wrote what it was given, and the profile could end with a credential the
// server had since made unknown and no retry key.
//
// heldTakeIn starts a take-in whose request the server holds until let_go
// is called, and returns once the server has it: the command is then in the
// middle, with the profile's lock held.
func heldTakeIn(t *testing.T, b *backend, r *rig, document string) (done <-chan outcome, letGo func()) {
	t.Helper()
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	b.answerWith(func(req *seenRequest) *canned {
		if req.Path == pathTeamTakeIn {
			held := false
			once.Do(func() { held = true })
			if held {
				close(arrived)
				<-release
			}
		}
		return nil
	})
	finished := make(chan outcome, 1)
	go func() { finished <- r.exec(document, "team", "take-in") }()
	select {
	case <-arrived:
	case <-time.After(hangCatcher):
		t.Fatal("the first take-in never reached the server")
	}
	return finished, sync.OnceFunc(func() { close(release) })
}

// shortWait is how long these tests have a command wait for the profile's
// lock where the default, ten seconds, would only make them slow.
const shortWait = 50 * time.Millisecond

// soon runs one command of the rig with the profile's lock waited for no
// longer than shortWait.
func (r *rig) soon(input string, args ...string) outcome {
	r.t.Helper()
	tm := defaultTiming()
	tm.profileLock = shortWait
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(input), &stdout, &stderr, r.env, options{timing: &tm})
	assertOneEnvelope(r.t, stdout.String())
	r.keep(stdout.String(), stderr.String())
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// TestASecondCommandOnAProfileRefusesWhileTheFirstWritesItsCredential: two
// take-ins of one profile are started at once. The second, given a short
// wait, refuses with a line that says why and sends nothing; so do the
// other commands that write or remove the credential. A command that only
// reads is not held up. Once the first has ended the profile holds the
// credential the server knows, and its retry key is gone because the
// take-in was answered.
func TestASecondCommandOnAProfileRefusesWhileTheFirstWritesItsCredential(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	r.ok("", "profile", "init", "--url", b.url())
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "alice@example.com")
	first, letGo := heldTakeIn(t, b, r, document)
	defer letGo()

	for _, second := range [][]string{
		{"team", "take-in"}, {"team", "join"}, {"team", "create"}, {"team", "leave"},
		{"profile", "reset"}, {"profile", "revoke-credentials"}, {"profile", "init", "--url", b.url(), "--force"},
		// The profile named after the verb is the one that is locked.
		{"team", "take-in", "--profile", "default"},
	} {
		refused := r.soon(document, second...)
		if refused.code != protocol.CodeUnavailable.Exit() {
			t.Fatalf("%v beside a take-in: exit %d, want unavailable\n%s", second, refused.code, refused.stdout)
		}
		for _, want := range []string{`"reason":"` + reasonProfileBusy + `"`, "another brigade command is changing this profile", "nothing was sent"} {
			if !strings.Contains(refused.stdout, want) {
				t.Errorf("%v beside a take-in: the refusal lacks %q: %s", second, want, refused.stdout)
			}
		}
	}
	if n := len(b.seen(pathTeamTakeIn)); n != 1 {
		t.Fatalf("the server saw %d take-ins while the first was held, want the one", n)
	}
	// A command that writes nothing takes no lock.
	if status := r.soon("", "profile", "status"); status.code != 0 {
		t.Fatalf("profile status beside a take-in: exit %d\n%s", status.code, status.stdout)
	}
	// Another profile of the same machine is not held up, and a command with flags it does not have is told
	// so at once: the lock comes after the flags.
	if other := r.soon("", "profile", "init", "--url", b.url(), "--profile", "another"); other.code != 0 {
		t.Fatalf("profile init of another profile beside a take-in: exit %d\n%s", other.code, other.stdout)
	}
	if bad := r.soon(document, "team", "join", "--no-such-flag"); bad.code != protocol.CodeUsage.Exit() {
		t.Fatalf("a join with a flag it does not have, beside a take-in: exit %d, want usage\n%s", bad.code, bad.stdout)
	}

	letGo()
	select {
	case out := <-first:
		if out.code != 0 {
			t.Fatalf("the first take-in: exit %d\n%s", out.code, out.stdout)
		}
	case <-time.After(hangCatcher):
		t.Fatal("the first take-in never ended")
	}
	r.noRetryKey("after the take-in that was answered")
	if r.state() != protocol.ProfileStateJoined {
		t.Fatalf("the profile is %s after the take-in", r.state())
	}
	r.ok("", "team", "members")
	// The lock is given back: the same profile takes the next command at once.
	if left := r.soon("", "team", "leave"); left.code != 0 {
		t.Fatalf("team leave after the take-in ended: exit %d\n%s", left.code, left.stdout)
	}
}

// TestASecondCommandOnAProfileWaitsForTheFirst: with the wait a command has
// by default the second take-in does not refuse. It waits for the first,
// and then runs on the profile as the first left it: it presents the
// credential the first was given, which it could only have read after the
// first wrote it.
func TestASecondCommandOnAProfileWaitsForTheFirst(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	r.ok("", "profile", "init", "--url", b.url())
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "alice@example.com")
	first, letGo := heldTakeIn(t, b, r, document)
	defer letGo()

	second := make(chan outcome, 1)
	go func() { second <- r.exec(document, "team", "take-in") }()
	// The second is waiting for the lock: it has sent nothing.
	time.Sleep(300 * time.Millisecond)
	if n := len(b.seen(pathTeamTakeIn)); n != 1 {
		t.Fatalf("the server saw %d take-ins while the first was held, want the one", n)
	}
	letGo()
	for name, ended := range map[string]<-chan outcome{"first": first, "second": second} {
		select {
		case out := <-ended:
			if name == "first" && out.code != 0 {
				t.Fatalf("the first take-in: exit %d\n%s", out.code, out.stdout)
			}
		case <-time.After(hangCatcher):
			t.Fatalf("the %s take-in never ended", name)
		}
	}
	if r.state() != protocol.ProfileStateJoined {
		t.Fatalf("the profile is %s after the two take-ins", r.state())
	}
	// What the profile holds is what the server knows.
	r.ok("", "team", "members")
	if n := len(b.seen(pathTeamTakeIn)); n > 1 {
		// A second request, when the second command made one, came after the first's answer was kept.
		if got := b.seen(pathTeamTakeIn)[1].Authorization; got != "Bearer "+r.storedCredential() {
			t.Errorf("the second take-in did not present the credential the first was given")
		}
	}
}

// TestTheProfilesLockIsGivenBackHoweverACommandEnds: after a command that
// failed, and after one whose lock file was left by a process that is gone,
// the next command takes the lock at once. The lock is the kernel's, on an
// open file: a file that is merely there holds nothing.
func TestTheProfilesLockIsGivenBackHoweverACommandEnds(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	r.ok("", "profile", "init", "--url", b.url())
	// A command that fails: a take-in with no document.
	if out := r.soon("", "team", "take-in"); out.code == 0 {
		t.Fatalf("a take-in with no document succeeded: %s", out.stdout)
	}
	lockPath := filepath.Dir(r.credentialPath()) + profileLockSuffix
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("the lock's file is not where the test looks for it: %v", err)
	}
	// Held by another descriptor, as a command in flight holds it: the next command refuses.
	held, err := adapterkit.LockFile(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "alice@example.com")
	if out := r.soon(document, "team", "take-in"); out.code != protocol.CodeUnavailable.Exit() {
		t.Fatalf("a take-in beside a held lock: exit %d\n%s", out.code, out.stdout)
	}
	// Given back, with the file still there: the next command runs.
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	if out := r.soon(document, "team", "take-in"); out.code != 0 {
		t.Fatalf("a take-in after the lock was given back: exit %d\n%s", out.code, out.stdout)
	}
}

// TestAProfileWithNoDirectoryYetIsLockedToo: the lock's file is beside the
// profile's directory, so two commands that would each make the directory,
// on a machine that has never joined, do not both run; and `profile reset`,
// which removes the directory, leaves the lock where a command that waits on
// it still finds it.
func TestAProfileWithNoDirectoryYetIsLockedToo(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url()) // the backend is named in the environment: no `profile init` is run
	if _, err := os.Stat(filepath.Dir(r.credentialPath())); err == nil {
		t.Fatal("the rig's profile has a directory before any command")
	}
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "alice@example.com")
	first, letGo := heldTakeIn(t, b, r, document)
	defer letGo()
	if refused := r.soon(document, "team", "take-in"); !strings.Contains(refused.stdout, reasonProfileBusy) {
		t.Fatalf("a second take-in on a profile that had no directory was not refused as busy: exit %d\n%s", refused.code, refused.stdout)
	}
	letGo()
	select {
	case out := <-first:
		if out.code != 0 {
			t.Fatalf("the first take-in: exit %d\n%s", out.code, out.stdout)
		}
	case <-time.After(hangCatcher):
		t.Fatal("the first take-in never ended")
	}
	// The reset removes the directory and not the lock's file.
	lockPath := filepath.Dir(r.credentialPath()) + profileLockSuffix
	if out := r.soon("", "profile", "reset"); out.code != 0 {
		t.Fatalf("profile reset: exit %d\n%s", out.code, out.stdout)
	}
	if _, err := os.Stat(filepath.Dir(r.credentialPath())); err == nil {
		t.Fatal("profile reset left the profile's directory")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("profile reset removed the lock's file with the directory: %v", err)
	}
}
