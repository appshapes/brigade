package watch

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/notify"
)

// Delivery into a Codex CLI session (card 107, `brigade watch --host
// codex`). Codex has no inbox socket and no monitor, but an outside process
// may queue a message into a thread: `codex queue --thread <uuid> --message
// <text>` (the app-server's thread/queue/add). Measured on 0.163.0
// (docs/experiments/codex-queue-participation.md): in daemon mode the turn
// starts inside the command's own 45 ms, in embedded mode within one queue
// poll of about 10 s, under the bypass flag and under the default profile,
// before the session's first prompt too, and a message queued mid-turn
// starts the next turn. The text travels on argv — visible to `ps` of the
// same user, an accepted limit (docs/security.md §16) — through the quiet
// spawn seam: an argv array, a from-scratch environment, never a shell.
//
// Exit 0 means STORED, not delivered: a thread whose TUI has quit keeps
// accepting rows, which land on its next `codex resume`. So nothing is
// queued unless the host process the map names is alive at that moment —
// the TUI in embedded mode, where the hooks are its children, or the shared
// app-server daemon in daemon mode, where Codex runs the hooks itself and no
// signal names the TUI (card 107, D2) — and the thread id is the map's as of
// the last liveness tick.
const (
	// codexQueueTimeout bounds one `codex queue` run; the measured command
	// took 45–340 ms.
	codexQueueTimeout = 15 * time.Second
	// codexProgram is the executable the watcher finds on its own PATH.
	codexProgram = "codex"
	// envCodexHome is passed through to the child when the hook's
	// environment carried it, so the queue reaches the daemon the TUI uses.
	envCodexHome = "CODEX_HOME"
)

// The failures a queue can report, each a reason the drain backs off on;
// the message is left to the server's redelivery.
var (
	errHostGone = errors.New("watch: the Codex process is gone; nothing is queued into a thread with no client")
	errNoThread = errors.New("watch: the by-pid map names no thread to queue into")
	errNoCodex  = errors.New("watch: no codex executable on the watcher's PATH")
)

// queueCodex puts one item in front of the Codex session: `codex queue
// --thread <thread> --message <frame>`.
func (in *injector) queueCodex(ctx context.Context, item inbound.Item) error {
	w := in.w
	if !w.hostAlive() {
		return errHostGone
	}
	thread := w.state.snapshot().nativeID
	if thread == "" {
		return errNoThread
	}
	base := w.codexCommand()
	if base == nil {
		return errNoCodex
	}
	base = append(base, "queue", "--thread", thread, "--message", item.Content)
	qctx, cancel := context.WithTimeout(ctx, codexQueueTimeout)
	defer cancel()
	return w.deps.Announce(qctx, base, w.codexEnv())
}

// hostAlive answers, right before a queue, what checkLiveness answers on
// its tick: the host process exists, is no zombie, is this user's, and is
// the one the watcher started with (pid reuse).
func (w *watcher) hostAlive() bool {
	info, err := w.deps.Lookup(w.hostPID)
	switch {
	case err != nil:
		return true // the tick logs the failure; a lookup error is not a death
	case !info.Exists, info.Zombie, info.Foreign:
		return false
	case w.claudeStart != "" && info.StartToken != "" && info.StartToken != w.claudeStart:
		return false
	}
	return true
}

// codexCommand is the argv prefix that stands for `codex`: the test seam's,
// else the executable on the watcher's own PATH (notify's search, which
// skips a relative entry). A machine with none is logged once per watcher.
func (w *watcher) codexCommand() []string {
	if len(w.deps.QueueCommand) > 0 {
		return slices.Clone(w.deps.QueueCommand)
	}
	path, ok := notify.LookPath(adapterkit.Getenv(w.environ, "PATH"), codexProgram)
	if !ok {
		if w.codexMissing.CompareAndSwap(false, true) {
			w.log.Warn("no codex executable on the watcher's PATH; team messages wait for the server's redelivery")
		}
		return nil
	}
	w.codexMissing.Store(false)
	return []string{path}
}

// codexEnv is the child's from-scratch environment plus the user's
// CODEX_HOME when the hook passed one.
func (w *watcher) codexEnv() []string {
	env := adapterkit.ChildEnv(w.environ)
	if home := adapterkit.Getenv(w.environ, envCodexHome); home != "" && filepath.IsAbs(home) {
		env = append(env, envCodexHome+"="+home)
	}
	return env
}
