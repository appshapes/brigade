package hook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
)

// All Grok hook read/modify/write transactions share PID then native-ID locks.
// The PID lock serializes a new/load replacement; the UUID lock also excludes
// simultaneous claims of one UUID by different TUIs. Neither lock is per-hook.
// Descriptors release on process death; stable sidecars are never unlinked.
func (r *run) lockGrokHook() (func(), error) {
	id, err := config.NativeID(config.HostGrok, r.environ)
	if err != nil || !grokUUID(id) {
		return nil, errors.New("invalid Grok hook identity")
	}
	if _, err := r.grokProcess(); err != nil {
		return nil, err
	}
	state, err := config.BrigadeStateDir(r.environ)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(state, "state")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		return nil, err
	}
	budget := 100 * time.Millisecond
	if r.args.sub == SubSessionStart {
		budget = time.Until(r.grokDeadline)
	}
	deadline := time.Now().Add(budget)
	var files []*os.File
	release := func() {
		for i := len(files) - 1; i >= 0; i-- {
			_ = files[i].Close()
		}
	}
	for _, name := range []string{"grok-pid-" + strconv.Itoa(r.deps.Getppid()), "grok-native-" + id} {
		file, err := adapterkit.OpenNoFollow(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			release()
			return nil, err
		}
		files = append(files, file)
		info, err := file.Stat()
		if err != nil {
			release()
			return nil, err
		}
		if !info.Mode().IsRegular() || !adapterkit.IsPrivate(info) {
			release()
			return nil, errors.New("grok hook lock is not private")
		}
		for {
			err := adapterkit.TryLockFile(file)
			if err == nil {
				break
			}
			if !adapterkit.LockWouldBlock(err) {
				release()
				return nil, err
			}
			if !time.Now().Before(deadline) {
				release()
				return nil, errors.New("grok receiver transaction is busy; hook deferred")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return release, nil
}

func (r *run) hookContext(budget time.Duration) (context.Context, context.CancelFunc) {
	if !r.grokDeadline.IsZero() {
		budget = min(budget, max(0, time.Until(r.grokDeadline)))
	}
	return context.WithTimeout(context.Background(), budget)
}
