// Package write holds every writer of the team store. It is split from
// the read side so the depguard rule "internal/harness/hook must not
// import teamstore/write" makes the hook's attach-only promise a
// compile-time property (plan P7-4). Only the terminal commands — team
// create, team join, team list — import this package.
package write

import (
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// lockTimeout bounds the wait for projects.json's flock; the writers are
// short-lived human-driven commands, so seconds are plenty.
const lockTimeout = 5 * time.Second

// tempPrefix marks a credential directory still being created. It must
// pass CheckProfileName (no leading dot — the temp dir doubles as the
// adapter's --profile during create), so orphans are visible siblings
// pruned by age instead of hidden files (plan correction 3).
const tempPrefix = "tmp-"

// Pin records consent for a canonical checkout directory, read-modify-
// writing projects.json under its flock: 0600 via WriteAtomic, so a
// crash leaves the old pins or the new, never a torn file.
func Pin(configDir, canonicalDir string, p teamstore.Pin) error {
	path := teamstore.PinsPath(configDir)
	lock, err := adapterkit.LockFile(path+".lock", lockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	pins, err := readPins(path)
	if err != nil {
		return err
	}
	pins.Projects[canonicalDir] = p
	data, err := json.Marshal(pins)
	if err != nil {
		return fmt.Errorf("teamstore: marshal pins: %w", err)
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// readPins loads the pins file for a read-modify-write; a missing file
// is an empty store, anything unreadable or unparsable refuses (a pin
// store we cannot faithfully rewrite must not be rewritten blind).
func readPins(path string) (*teamstore.PinsFile, error) {
	pins := &teamstore.PinsFile{Version: teamstore.PinsVersion, Projects: map[string]teamstore.Pin{}}
	data, err := adapterkit.ReadStrict(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pins, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, pins); err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "projects.json does not parse; refusing to rewrite what cannot be read back",
			Details: map[string]string{"reason": "pins_malformed"},
		}
	}
	if pins.Projects == nil {
		pins.Projects = map[string]teamstore.Pin{}
	}
	return pins, nil
}

// TempKey mints a fresh create-time directory name: tempPrefix plus 12
// random hex characters. It passes CheckProfileName, so `team create`
// can drive the adapter's frozen --profile vocabulary with it before
// the real key exists (plan correction 3, the create-circularity fix).
func TempKey() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("teamstore: mint temp key: %w", err)
	}
	return tempPrefix + hex.EncodeToString(b[:]), nil
}

// Promote renames teams/<tempKey> to teams/<key> once the create RPC has
// answered the real team_ref — same-filesystem atomic. An existing
// destination refuses loudly (EEXIST/ENOTEMPTY): a second create against
// a team that already has a local credential is a human's call, not a
// silent overwrite.
func Promote(configDir, tempKey, key string) error {
	from := filepath.Join(configDir, "teams", tempKey)
	to := filepath.Join(configDir, "teams", key)
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	// Renaming onto an existing directory answers EEXIST or ENOTEMPTY
	// depending on platform and emptiness; both mean the same conflict.
	if errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY) {
		return &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: "a credential directory for this team already exists; remove it with `brigade team reset` if it is stale",
			Details: map[string]string{"reason": "team_dir_exists"},
		}
	}
	return fmt.Errorf("teamstore: promote temp key: %w", err)
}

// PatchBindingBackend rewrites the binding's harness-owned members —
// adapter, url, publishable_key — leaving everything the adapter wrote
// intact (verify-round high fix 1: the kit schema round-trips, so a
// later adapter load-modify-save preserves the patch, and this patch
// preserves the adapter's members).
func PatchBindingBackend(configDir, key, adapter, url, publishableKey string) error {
	p, err := adapterkit.LoadProfile(configDir, key)
	if err != nil {
		return err
	}
	p.Adapter, p.URL, p.PublishableKey = adapter, url, publishableKey
	return adapterkit.SaveProfile(configDir, key, p)
}

// PruneOrphans removes tmp-* credential directories older than maxAge —
// the crash window between a create's signup and its Promote. The next
// create or `team list` calls this; fresh temp dirs (a create racing us)
// are left alone. Removed paths are returned for the caller's DEBUG log.
func PruneOrphans(configDir string, maxAge time.Duration, now time.Time) ([]string, error) {
	teams := filepath.Join(configDir, "teams")
	entries, err := os.ReadDir(teams)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("teamstore: scan for orphans: %w", err)
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		dir := filepath.Join(teams, e.Name())
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("teamstore: prune %s: %w", dir, err)
		}
		removed = append(removed, dir)
	}
	return removed, nil
}
