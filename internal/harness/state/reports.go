// reports.go is the subscribers file, `reports.json` in the folder (card
// 162, plan §5.5, rewritten by card 167): who gets the digests and the
// decisions addressed to them, where, and on what period. Each
// subscriber's own session writes its entry (`brigade report subscribe`
// and `unsubscribe`, P66-1); `brigade decision`, `brigade report` and
// the producing watcher read the file. The 0.43 shape — one manager —
// reads as one subscriber.

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/gateway"
	"github.com/appshapes/brigade/internal/protocol"
)

// The reports file's bounds.
const (
	// ReportsVersion is the file's own version: 2 is the subscriber
	// list; 1, the manager of 0.43, is read as one subscriber.
	ReportsVersion = 2
	// MaxSubscribers caps the list; MaxRecipients a subscriber's
	// recipients; MaxRecipientChars one recipient (a handle, a channel,
	// an address).
	MaxSubscribers    = 64
	MaxRecipients     = 8
	MaxRecipientChars = 256
	// DefaultEvery is the period when a subscriber sets none.
	DefaultEvery = 24 * time.Hour
	// OnlyMine is the one filter: the digest's decision sections list
	// only the decisions addressed to the subscriber; the contribution
	// sections stay the team's.
	OnlyMine = "mine"
	// labelStemChars caps the readable part of LabelStem.
	labelStemChars = 40
)

// A Subscriber is one entry: the member's roster label (the key), the
// recipients in precedence (a Slack handle or channel, or an address;
// each goes through the Slack gateway when the team has one, an address
// through the mail gateway otherwise; none means their own best
// session), the period — `every` as a Go duration ("24h", "4h"), or for
// a daily digest `at` ("08:00") in `tz` (an IANA zone) — and the filter,
// "" or OnlyMine.
type Subscriber struct {
	Label      string   `json:"label"`
	Recipients []string `json:"recipients"`
	Every      string   `json:"every,omitzero"`
	At         string   `json:"at,omitzero"`
	TZ         string   `json:"tz,omitzero"`
	Only       string   `json:"only,omitzero"`
}

// Reports is the file: the subscribers, in the order they subscribed.
type Reports struct {
	Version     int          `json:"version"`
	Subscribers []Subscriber `json:"subscribers"`
	// Legacy is set by ReadReports for a file of the 0.43 shape (version
	// 1, `manager` and `recipients`): its one subscriber's marker is the
	// v1 marker until they have their own, and the first write carries
	// it over. Never written.
	Legacy bool `json:"-"`
}

// reportsFile is every member of either shape, for the reader.
type reportsFile struct {
	Version     int          `json:"version"`
	Subscribers []Subscriber `json:"subscribers"`
	Manager     string       `json:"manager"`
	Recipients  []string     `json:"recipients"`
	Every       string       `json:"every"`
	At          string       `json:"at"`
	TZ          string       `json:"tz"`
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

// ReadReports reads the folder's reports file, sanitised: each
// subscriber's label, the recipients that have a kind (the rest dropped,
// the list capped), the period members as given, each one line; a
// subscriber with no label, or a second with a label already seen, is
// dropped. A version-1 file reads as one subscriber (Legacy set). A
// missing file satisfies errors.Is(err, fs.ErrNotExist); a file of
// another version or shape is an error.
func ReadReports(folder string) (*Reports, error) {
	data, err := ReadFolderFile(ReportsPath(folder))
	if err != nil {
		return nil, err
	}
	var f reportsFile
	if err := Unmarshal(data, &f); err != nil {
		return nil, errors.New("state: reports.json is not a JSON document of the expected shape")
	}
	r := &Reports{Version: ReportsVersion}
	switch f.Version {
	case ReportsVersion:
		r.Subscribers = f.Subscribers
	case 1:
		r.Legacy = true
		if strings.TrimSpace(f.Manager) != "" {
			r.Subscribers = []Subscriber{{Label: f.Manager, Recipients: f.Recipients, Every: f.Every, At: f.At, TZ: f.TZ}}
		}
	default:
		return nil, errors.New("state: reports.json's version is not one this package reads")
	}
	r.Sanitize()
	return r, nil
}

// WriteReports writes the file into the folder, atomically, as version
// ReportsVersion after Sanitize: `brigade report subscribe` and
// `unsubscribe` are its writers.
func WriteReports(folder string, r *Reports) error {
	r.Version = ReportsVersion
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

// Sanitize applies the file's rules in place: every subscriber
// sanitised, the unlabelled and the duplicates dropped, the list capped,
// and never a nil list.
func (r *Reports) Sanitize() {
	subs := make([]Subscriber, 0, len(r.Subscribers))
	for i := range r.Subscribers {
		s := r.Subscribers[i]
		s.Sanitize()
		if s.Label == "" || len(subs) == MaxSubscribers || hasLabel(subs, s.Label) {
			continue
		}
		subs = append(subs, s)
	}
	r.Subscribers = subs
}

// hasLabel reports whether a subscriber of the label is in the list.
func hasLabel(subs []Subscriber, label string) bool {
	for i := range subs {
		if strings.EqualFold(subs[i].Label, label) {
			return true
		}
	}
	return false
}

// Subscriber is the entry of the label, compared without case; nil for
// none.
func (r *Reports) Subscriber(label string) *Subscriber {
	label = strings.TrimSpace(label)
	for i := range r.Subscribers {
		if strings.EqualFold(r.Subscribers[i].Label, label) {
			return &r.Subscribers[i]
		}
	}
	return nil
}

// Subscribe puts s in the list: in place of the entry with its label, or
// at the end.
func (r *Reports) Subscribe(s Subscriber) {
	if have := r.Subscriber(s.Label); have != nil {
		*have = s
		return
	}
	r.Subscribers = append(r.Subscribers, s)
}

// Unsubscribe removes the entry of the label; false when there was none.
func (r *Reports) Unsubscribe(label string) bool {
	for i := range r.Subscribers {
		if strings.EqualFold(r.Subscribers[i].Label, strings.TrimSpace(label)) {
			r.Subscribers = append(r.Subscribers[:i:i], r.Subscribers[i+1:]...)
			return true
		}
	}
	return false
}

// Sanitize applies a subscriber's rules in place, and fills an empty
// recipients list with the label when that is itself a recipient — an
// email address, which a roster label is by default (owner 2026-10-29):
// subscribing is then enough, and the Slack gateway finds them by it.
func (s *Subscriber) Sanitize() {
	s.Label = protocol.SanitizeLabel(line(s.Label))
	recipients := make([]string, 0, len(s.Recipients))
	for _, x := range s.Recipients {
		x = protocol.TruncateRunes(line(x), MaxRecipientChars)
		if RecipientKind(x) != "" && len(recipients) < MaxRecipients {
			recipients = append(recipients, x)
		}
	}
	if len(recipients) == 0 && RecipientKind(s.Label) != "" {
		recipients = append(recipients, s.Label)
	}
	s.Recipients = recipients
	s.Every = line(s.Every)
	s.At = line(s.At)
	s.TZ = line(s.TZ)
	if s.Only != OnlyMine {
		s.Only = ""
	}
}

// Period is the subscriber's period: `every` parsed, DefaultEvery when it
// is absent or not a duration or under a minute.
func (s *Subscriber) Period() time.Duration {
	d, err := time.ParseDuration(s.Every)
	if err != nil || d < time.Minute {
		return DefaultEvery
	}
	return d
}

// LabelStem is the file stem of a label: its letters and digits in
// lower case, every other run of characters one dash, cut at
// labelStemChars, then a dash and six hex digits of the lower-cased
// label's hash — so "rjae@example.com" is "rjae-example-com-3f2a9c",
// two labels that slug alike stay apart, and the stem passes CheckID.
func LabelStem(label string) string {
	lower := strings.ToLower(strings.TrimSpace(label))
	var b strings.Builder
	dash := false
	for _, r := range lower {
		switch {
		case r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	stem := strings.TrimRight(b.String(), "-")
	if len(stem) > labelStemChars {
		stem = strings.TrimRight(stem[:labelStemChars], "-")
	}
	sum := sha256.Sum256([]byte(lower))
	tail := hex.EncodeToString(sum[:3])
	if stem == "" {
		return tail
	}
	return stem + "-" + tail
}

// A Gateway is the roster session a recipient goes through: its id, the
// `to:` line's value, and the recipient's kind.
type Gateway struct {
	SessionID string
	To        string
	Kind      string
}

// PickGateway walks the recipients in order and picks the first that a
// gateway on the roster (not offline) can carry: a handle or a channel
// through the Slack gateway; an address through the Slack gateway too
// when the team has one — it finds the person by their email, as
// `/brigade:slack <address>` does — and else through the mail gateway.
// So Slack comes before email whatever shape the recipient has (ruling
// 5; owner 2026-10-29: a member's roster label is an address, and that
// must not mean mail). Kind says the road taken. false when no recipient
// can be carried.
func PickGateway(recipients []string, sessions []protocol.SessionRecord) (Gateway, bool) {
	for _, to := range recipients {
		kind := RecipientKind(to)
		if kind == "" {
			continue
		}
		if id := onlineGateway(gateway.HarnessSlack, sessions); id != "" {
			return Gateway{SessionID: id, To: to, Kind: RecipientSlack}, true
		}
		if kind == RecipientEmail {
			if id := onlineGateway(gateway.HarnessEmail, sessions); id != "" {
				return Gateway{SessionID: id, To: to, Kind: RecipientEmail}, true
			}
		}
	}
	return Gateway{}, false
}

// onlineGateway is the id of the first roster session of the harness
// that is not offline, "" for none.
func onlineGateway(harness string, sessions []protocol.SessionRecord) string {
	for i := range sessions {
		if sessions[i].Harness == harness && sessions[i].State != protocol.SessionStateOffline {
			return sessions[i].SessionID
		}
	}
	return ""
}
