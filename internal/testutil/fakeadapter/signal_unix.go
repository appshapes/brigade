// The fake adapter models a real adapter's process behaviour: it exits 0
// on SIGTERM from its supervising watch (C-38) and can die by a signal on
// command (the SIGKILLed-child model). Both use syscall, which exists on
// the two supported platforms only (D33), so a third OS fails to build
// here rather than silently linking a fake that cannot be signalled.

//go:build darwin || linux

package fakeadapter

import (
	"context"
	"os/signal"
	"syscall"
)

// notifyTerm returns a context cancelled on SIGTERM, so the watch loop can
// exit 0 within the harness's WaitDelay of a cancel.
func notifyTerm() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM)
}

// raiseSignal sends sig to this process. Used to model a signal death (a
// SIGKILLed child): the harness maps the signalled exit to unavailable
// with the signal name.
func raiseSignal(sig int) {
	_ = syscall.Kill(syscall.Getpid(), syscall.Signal(sig))
}
