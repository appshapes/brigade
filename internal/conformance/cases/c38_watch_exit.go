package cases

import (
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c38WatchExit: the watch exits 0 within 5 s of stdin EOF, and within 5 s
// of SIGTERM (4.4.9).
func c38WatchExit() conformance.Case {
	return conformance.Case{
		ID:    "C-38",
		Rule:  "4.4.9 watch exit",
		Title: "stdin EOF and SIGTERM each end the watch with exit 0 within 5 s",
		Tags:  []string{conformance.TagCore},
		Run:   runC38,
	}
}

func runC38(t *conformance.T) {
	b := t.B()
	_, sb := t.Register(b, nameFor(t, "C-38", "watched"), nil)

	w1 := t.Watch(b, sb)
	w1.Expect(protocol.EventReady, t.PushDeadline())
	w1.CloseStdin()
	if exit, exited := w1.Wait(5 * time.Second); !exited {
		t.Errorf("message watch: still running 5 s after stdin EOF (4.4.9)")
	} else if exit != 0 {
		t.Errorf("message watch: exit %d after stdin EOF, want 0 (4.4.9)", exit)
	}

	w2 := t.Watch(b, sb)
	w2.Expect(protocol.EventReady, t.PushDeadline())
	w2.Signal(syscall.SIGTERM)
	if exit, exited := w2.Wait(5 * time.Second); !exited {
		t.Errorf("message watch: still running 5 s after SIGTERM (4.4.9)")
	} else if exit != 0 {
		t.Errorf("message watch: exit %d after SIGTERM, want 0 (4.4.9)", exit)
	}
}
