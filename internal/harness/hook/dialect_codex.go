package hook

import (
	"slices"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// thread is Codex CLI's dialect (config.DialectThread; card 107): Codex
// runs its hooks with no CODEX_* variable, so the hook's argv names the
// host, the document's session id is the thread the model's shell will
// carry, and the session is keyed by that thread. The rest is the plain
// form.
type thread struct{ plugin }

// adopt appends the thread id of the document to the run's environment
// under the host's own marker, so that every config resolver treats the
// run as the session it is. A document whose id is not a safe path
// component is refused: nothing of this session could be keyed by it.
func (thread) adopt(r *run, in input) error {
	variable := r.args.host.NativeIDVar()
	if err := sessionmap.CheckNativeID(in.SessionID); err != nil {
		// The words are the ones the refusal has always had, and name the
		// one host that speaks this dialect today.
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the Codex hook document carries no usable thread id",
			Details: map[string]string{"reason": config.ReasonInvalidNativeID, "variable": variable},
		}
	}
	r.environ = append(slices.Clone(r.environ), variable+"="+in.SessionID)
	return nil
}

// key: measured on 0.163.0 with the hooks trusted, the hook's parent is
// the shared app-server daemon, not the TUI, so the key is the thread's
// and the parent is kept for liveness only.
func (thread) key(nativeID string, parent int) (pid, hostProcess int) {
	return codexKey(nativeID), parent
}
