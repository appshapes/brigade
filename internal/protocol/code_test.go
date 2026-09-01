package protocol

import "testing"

// TestExitCodeTable pins the whole 4.6 table: all twelve codes, exit
// status and retryability. The taxonomy is frozen (freeze list item 7),
// so this table is written out literally rather than derived from
// anything the implementation could drift with.
func TestExitCodeTable(t *testing.T) {
	t.Parallel()
	table := []struct {
		code      Code
		exit      int
		retryable bool
	}{
		{CodeInternal, 1, false},
		{CodeUsage, 2, false},
		{CodeInvalidInput, 3, false},
		{CodeUnauthenticated, 4, false},
		{CodeUnauthorized, 5, false},
		{CodeNotFound, 6, false},
		{CodeConflict, 7, false},
		{CodeRateLimited, 8, true},
		{CodeUnavailable, 9, true},
		{CodeProtocolMismatch, 10, false},
		{CodeConfig, 11, false},
		{CodeLoopDetected, 12, false},
	}
	if len(table) != 12 {
		t.Fatalf("the 4.6 taxonomy has twelve codes, table has %d", len(table))
	}
	seen := map[int]Code{}
	for _, row := range table {
		if got := row.code.Exit(); got != row.exit {
			t.Errorf("%s.Exit() = %d, want %d", row.code, got, row.exit)
		}
		if got := row.code.Retryable(); got != row.retryable {
			t.Errorf("%s.Retryable() = %v, want %v", row.code, got, row.retryable)
		}
		if prev, dup := seen[row.exit]; dup {
			t.Errorf("exit %d assigned to both %s and %s", row.exit, prev, row.code)
		}
		seen[row.exit] = row.code
	}
}

// TestUnknownCodeFailsClosed: a code outside the table maps to the
// internal exit status and is not retryable — never to success.
func TestUnknownCodeFailsClosed(t *testing.T) {
	t.Parallel()
	c := Code("something-new")
	if got := c.Exit(); got != 1 {
		t.Errorf("unknown code Exit() = %d, want 1 (internal), and never %d", got, ExitOK)
	}
	if c.Retryable() {
		t.Error("unknown code Retryable() = true, want false")
	}
	if empty := Code(""); empty.Exit() == ExitOK {
		t.Error("empty code maps to exit 0; a failure became a success")
	}
}
