// reports.go is the manager's settings file, `reports.json` in the folder
// (card 162, plan §5.5): who the manager is, where the digest and the
// decisions go, and on what period. `brigade report configure` writes it
// (P65-5); `brigade decision` and the producing watcher read it.

package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/gateway"
	"github.com/appshapes/brigade/internal/protocol"
)

// The reports file's bounds.
const (
	// MaxRecipients caps the recipients list; MaxRecipientChars one
	// recipient (a handle, a channel, an address).
	MaxRecipients     = 8
	MaxRecipientChars = 256
	// DefaultEvery is the period when the file sets none.
	DefaultEvery = 24 * time.Hour
)

// Reports is the file: the manager's roster label, the recipients in
// precedence (a Slack handle or channel first, an address after, each
// typed by its shape), and the period — `every` as a Go duration ("24h",
// "4h"), and for a daily digest `at` ("08:00") in `tz` (an IANA zone).
type Reports struct {
	Version    int      `json:"version"`
	Manager    string   `json:"manager"`
	Recipients []string `json:"recipients"`
	Every      string   `json:"every,omitzero"`
	At         string   `json:"at,omitzero"`
	TZ         string   `json:"tz,omitzero"`
}

// The two kinds of recipient, by shape.
const (
	RecipientSlack = "slack"
	RecipientEmail = "email"
)

// RecipientKind says which gateway carries a recipient: RecipientSlack
// for `@name` or `#channel`, RecipientEmail for an address, "" for a
// shape neither gateway takes.
func RecipientKind(r string) string {
	switch {
	case strings.HasPrefix(r, "@") && len(r) > 1, strings.HasPrefix(r, "#") && len(r) > 1:
		return RecipientSlack
	}
	at := strings.Index(r, "@")
	if at > 0 && strings.Contains(r[at+1:], ".") && !strings.ContainsAny(r, " \t") {
		return RecipientEmail
	}
	return ""
}

// ReportsPath is <folder>/reports.json.
func ReportsPath(folder string) string { return filepath.Join(folder, ReportsFile) }

// ReadReports reads the folder's reports file, sanitised: the manager
// label, the recipients that have a kind (the rest dropped, the list
// capped), and the period members as given, each one line. A missing
// file satisfies errors.Is(err, fs.ErrNotExist); a file of another
// version or shape is an error.
func ReadReports(folder string) (*Reports, error) {
	data, err := ReadFolderFile(ReportsPath(folder))
	if err != nil {
		return nil, err
	}
	var r Reports
	if err := Unmarshal(data, &r); err != nil {
		return nil, errors.New("state: reports.json is not a JSON document of the expected shape")
	}
	if r.Version != Version {
		return nil, errors.New("state: reports.json's version is not this package's")
	}
	r.Sanitize()
	return &r, nil
}

// WriteReports writes the file into the folder, atomically, after
// Sanitize: `brigade report configure` is its one writer.
func WriteReports(folder string, r *Reports) error {
	r.Version = Version
	r.Sanitize()
	data, err := Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0o750); err != nil { //nolint:gosec // G301: a checkout tree is an ordinary directory
		return err
	}
	return adapterkit.WriteAtomic(ReportsPath(folder), data)
}

// Sanitize applies the file's rules in place.
func (r *Reports) Sanitize() {
	r.Manager = protocol.SanitizeLabel(line(r.Manager))
	recipients := make([]string, 0, len(r.Recipients))
	for _, x := range r.Recipients {
		x = protocol.TruncateRunes(line(x), MaxRecipientChars)
		if RecipientKind(x) != "" && len(recipients) < MaxRecipients {
			recipients = append(recipients, x)
		}
	}
	r.Recipients = recipients
	r.Every = line(r.Every)
	r.At = line(r.At)
	r.TZ = line(r.TZ)
}

// Period is the digest's period: `every` parsed, DefaultEvery when it is
// absent or not a duration or under a minute.
func (r *Reports) Period() time.Duration {
	d, err := time.ParseDuration(r.Every)
	if err != nil || d < time.Minute {
		return DefaultEvery
	}
	return d
}

// A Gateway is the roster session a recipient goes through: its id, the
// `to:` line's value, and the recipient's kind.
type Gateway struct {
	SessionID string
	To        string
	Kind      string
}

// PickGateway walks the recipients in order and picks the first whose
// gateway is on the roster and not offline — the Slack gateway for a
// handle or a channel, the mail gateway for an address — so Slack comes
// before email when the file lists them so (ruling 5). false when no
// recipient's gateway is there.
func PickGateway(recipients []string, sessions []protocol.SessionRecord) (Gateway, bool) {
	for _, to := range recipients {
		kind := RecipientKind(to)
		harness := map[string]string{RecipientSlack: gateway.HarnessSlack, RecipientEmail: gateway.HarnessEmail}[kind]
		if harness == "" {
			continue
		}
		for i := range sessions {
			if sessions[i].Harness == harness && sessions[i].State != protocol.SessionStateOffline {
				return Gateway{SessionID: sessions[i].SessionID, To: to, Kind: kind}, true
			}
		}
	}
	return Gateway{}, false
}
