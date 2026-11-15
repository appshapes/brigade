package rest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
)

// take-in-key.json: the retry key of a `team take-in` whose answer is not
// kept yet (the contract's section 3b; work item 328, ADR 0005).
//
// A take-in needs no credential: the server makes a principal, makes it the
// team's owner, and its credential rides the answer. An answer that is lost
// between the server's sync and this adapter's write of credential.json (the
// command's deadline, a proxy's 502, an interrupt, a disk with no room)
// leaves a team whose owner is a principal nobody holds a credential for,
// and the same command run again would join as a stranger. So the adapter
// mints a key before the request, keeps it here, and sends it: the server
// keeps the key's hash with the take-in, and answers the same request with
// the same key for the principal the first one made, with a new credential.
//
// The file is beside credential.json and is held to the same rules: mode
// 0600, written by rename (adapterkit.WriteAtomic), read by
// adapterkit.ReadStrict, which refuses a copy that group or other can read.
// It is there from before the request until credential.json holds what the
// answer carried, and is removed then, so it outlives a take-in only when
// the answer was lost. It names the server and the team it was minted for,
// and a key is sent to no other: the next `team take-in` of this profile
// uses it when it is for the same server and the same team, and puts a new
// one in its place otherwise. The key is never printed, never logged and
// never on argv, and it is registered on the redactor the moment it is
// read or minted.

// retryKeyFileName is the retry key file.
//
//nolint:gosec // G101: this is a FILE NAME, not a credential; the file's content is the key
const retryKeyFileName = "take-in-key.json"

// The contract's bounds on a retry key: 22 to 128 characters of a
// credential's alphabet. retryKeyBytes is what this adapter mints one from:
// 256 random bits, 43 characters of base64url.
const (
	minRetryKeyChars = 22
	maxRetryKeyChars = 128
	retryKeyBytes    = 32
)

// A retryKey is the content of take-in-key.json: the key, and the server
// and the team it was minted for.
type retryKey struct {
	Key         string    `json:"key"`
	URL         string    `json:"url"`
	Certificate string    `json:"certificate,omitzero"`
	TeamRef     string    `json:"team_ref"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
}

// validRetryKey reports whether s is a retry key by the contract's rule.
func validRetryKey(s string) bool {
	return len(s) >= minRetryKeyChars && len(s) <= maxRetryKeyChars && validCredential(s)
}

// retryKeyPath is ${profileDir}/take-in-key.json.
func (c *command) retryKeyPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, retryKeyFileName), nil
}

// takeInRetryKey returns the key a take-in of teamRef at the command's
// server carries: the one an earlier attempt for the same server and the
// same team left in take-in-key.json, else a new one, which is in the file
// before it is returned. It runs after the profile is connected and before
// the request. A file that group or other can read is the strict reader's
// `config`, as for credential.json: a key that others could read is not
// sent. A file that does not parse, holds no key, or was minted for another
// server or another team is replaced, the last with a line on stderr that
// says so: the take-in that key was for cannot be made again once it is
// gone. A key that cannot be written is
// `internal` and nothing is sent: the credential the answer carries goes to
// the same directory, so a take-in that cannot keep its key could not keep
// its answer either.
func (c *command) takeInRetryKey(teamRef string) (string, error) {
	path, err := c.retryKeyPath()
	if err != nil {
		return "", err
	}
	data, err := adapterkit.ReadStrict(path)
	switch {
	case err == nil:
		var kept retryKey
		if json.Unmarshal(data, &kept) == nil && validRetryKey(kept.Key) {
			if kept.URL == c.profile.URL && kept.Certificate == c.profile.Certificate && kept.TeamRef == teamRef {
				c.redactor.Add(kept.Key)
				c.log.Debug("the take-in carries the retry key an earlier attempt kept")
				return kept.Key, nil
			}
			// A key that could still be honoured is given up here, and the
			// take-in it was minted for can then not be made again: that is
			// said, and not done in silence.
			c.log.Warn("the retry key in " + retryKeyFileName + " was kept for a take-in of another team, at another server or under another certificate pin; this take-in puts its own in its place, and that one cannot be made again from this profile")
		}
	case errors.Is(err, iofs.ErrNotExist):
	default:
		return "", err
	}
	var raw [retryKeyBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errInternal("the operating system gave no randomness for the retry key")
	}
	minted := retryKey{
		Key: base64.RawURLEncoding.EncodeToString(raw[:]), URL: c.profile.URL, Certificate: c.profile.Certificate,
		TeamRef: teamRef, CreatedAt: c.now().UTC(),
	}
	c.redactor.Add(minted.Key)
	encoded, err := json.Marshal(&minted)
	if err != nil {
		return "", errInternal("the retry key could not be encoded")
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return "", errInternal("the profile directory could not be created")
	}
	if err := adapterkit.WriteAtomic(path, append(encoded, '\n')); err != nil {
		c.log.Debug("retry key write failed", adapterlog.Err(err))
		return "", errInternal("the retry key of the take-in could not be written; nothing was sent")
	}
	return minted.Key, nil
}

// dropRetryKey removes take-in-key.json once the credential the answer
// carried is in credential.json, or the answer carried none because the
// profile holds the one it acted with. A missing file is not an error, and
// a file that cannot be removed fails nothing: the take-in is done. The
// line on stderr says to remove it, because the server honours the key
// until the team's join secret is rotated.
func (c *command) dropRetryKey() {
	path, err := c.retryKeyPath()
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		c.log.Warn("the retry key of the take-in could not be removed; remove " + retryKeyFileName + " from the profile directory, where the credential is")
	}
}
