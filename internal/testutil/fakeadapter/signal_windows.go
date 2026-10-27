//go:build windows

package fakeadapter

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows"
)

// notifyTerm on Windows: a supervising watch cancels its child with
// TerminateProcess (adapterkit.cancelProcess), which no handler can see,
// so the context ends only on a console interrupt. The watch loop's
// stdin EOF exit is the path a Windows supervisor takes.
func notifyTerm() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// raiseSignal models a signal death as nearly as Windows allows: the
// process terminates itself with 128+sig, the exit status a unix shell
// reports for that signal.
func raiseSignal(sig int) {
	_ = windows.TerminateProcess(windows.CurrentProcess(), uint32(128+sig)) //nolint:gosec // G115: a small positive signal number
}
