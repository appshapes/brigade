//go:build !windows

package rest

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

// TestTheProfilesLockEndsWithAProcessThatASignalEnded: the real binary runs
// a take-in whose request the server holds, so the command is in the middle
// with the profile's lock held. A signal ends the process: SIGINT, which a
// person's Ctrl-C sends, SIGTERM, which a supervisor sends, and SIGKILL,
// which nothing can handle. The lock is the kernel's and goes with the process: the next
// command on the profile takes it at once, though the lock's file is still
// there. A lock that outlived its holder would be worse than the race it
// closes.
func TestTheProfilesLockEndsWithAProcessThatASignalEnded(t *testing.T) {
	t.Parallel()
	brigade := testutil.Build(t, "./cmd/brigade")
	for name, sig := range map[string]syscall.Signal{"SIGINT": syscall.SIGINT, "SIGTERM": syscall.SIGTERM, "SIGKILL": syscall.SIGKILL} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			r := newRig(t, b.url())
			r.ok("", "profile", "init", "--url", b.url())
			document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "alice@example.com")
			arrived, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(release) })
			var first sync.Once
			b.answerWith(func(req *seenRequest) *canned {
				if req.Path == pathTeamTakeIn {
					first.Do(func() {
						close(arrived)
						<-release
					})
				}
				return nil
			})
			//nolint:gosec // G204: a binary this test built, with fixed arguments
			cmd := exec.CommandContext(t.Context(), brigade, "adapter", "rest", "team", "take-in")
			cmd.Env = r.env
			cmd.Stdin = strings.NewReader(document)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-arrived:
			case <-time.After(hangCatcher):
				_ = cmd.Process.Kill()
				t.Fatal("the take-in never reached the server")
			}
			// In the middle: another command on the profile refuses.
			if out := r.soon(document, "team", "take-in"); !strings.Contains(out.stdout, reasonProfileBusy) {
				t.Fatalf("a take-in beside the running one was not refused as busy: exit %d\n%s", out.code, out.stdout)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			// The process is gone, and its lock with it: `profile status` reads, and a command that locks runs.
			if out := r.soon("", "profile", "revoke-credentials"); strings.Contains(out.stdout, reasonProfileBusy) {
				t.Fatalf("the lock outlived the process that %s ended:\n%s", name, out.stdout)
			}
		})
	}
}
