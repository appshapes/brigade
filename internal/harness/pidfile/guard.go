package pidfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"

	"github.com/appshapes/brigade/internal/procutil"
)

// LookupFunc is procutil.Lookup's shape. [Check] takes one so the hook's
// and the watcher's tests can inject process facts instead of starting
// processes (plan 7.3: every side effect injectable).
type LookupFunc func(pid int) (procutil.Info, error)

// Alive judges whether e's watcher is still the process the file
// describes: the pid exists, it is not a zombie (E0-5 item 1), it is not
// another user's (EPERM is treated as pid reuse, 6.6), and its current
// start token equals the stored one byte for byte (the pid-reuse guard).
// An Info with an empty token — the process could not be read — is never
// alive. When info carries a PID it must be e's; an Info built by hand
// with PID 0 skips that cross-check.
func Alive(e Entry, info procutil.Info) bool {
	if info.PID != 0 && info.PID != e.PID {
		return false
	}
	return info.Exists &&
		!info.Zombie &&
		!info.Foreign &&
		info.StartToken != "" &&
		info.StartToken == e.StartToken
}

// Verdict is [Check]'s answer.
type Verdict struct {
	// Entry is the file's content when Found.
	Entry Entry
	// Found is false when there is no pidfile at all.
	Found bool
	// Alive is [Alive](Entry, lookup(Entry.PID)); false when not Found.
	Alive bool
}

// Check reads the pidfile at path and judges its holder through lookup.
// A missing file is (Verdict{}, nil). A file that cannot be read or
// parsed — the wrong mode, a symlink, garbage — is an error the caller
// reports rather than a "dead" verdict, because [Replace] could not
// retire it by content anyway. A lookup error is returned with
// Found=true and Alive=false so the caller can see which pid failed.
func Check(path string, lookup LookupFunc) (Verdict, error) {
	e, err := Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Verdict{}, nil
	}
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Entry: e, Found: true}
	info, err := lookup(e.PID)
	if err != nil {
		return v, err
	}
	v.Alive = Alive(e, info)
	return v, nil
}

// TokenSHA256 is the lowercase hex SHA-256 of token: the only form in
// which the messaging token is ever compared without being stored (D9,
// 3.2). It is what goes in Entry.TokenSHA256 and what the next hook
// computes from its own environment to detect a rotated token.
func TokenSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
