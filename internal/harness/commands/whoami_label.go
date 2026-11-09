package commands

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// whoamiLabel is the `label` member of `whoami --json` (card 191): the
// label this machine's membership carries, where it came from and when,
// and the Claude account this session is signed in to. One machine user
// has one credential store, so every Claude profile on the machine is the
// same member under the label of whichever profile joined; this is how a
// member finds that out without tracing transcripts and config backups.
// Everything is read from this machine: the team store and Claude Code's
// own config, never the backend.
type whoamiLabel struct {
	// HumanLabel is the label the membership was joined with on this
	// machine; absent when it was joined with none.
	HumanLabel string `json:"human_label,omitzero"`
	// Source is a teamstore LabelFrom* word, or LabelUnrecorded when no
	// record describes HumanLabel.
	Source string `json:"source"`
	// SentAt is when the recorded create or join sent the label.
	SentAt *time.Time `json:"sent_at,omitzero"`
	// CredentialSince is when this machine's credential directory for the
	// team was first written. It survives a leave and a rejoin, so it
	// dates the credential, not the membership; it is what a label joined
	// before the record existed still has.
	CredentialSince *time.Time `json:"credential_since,omitzero"`
	// SessionAccount is the email of the Claude account this session is
	// signed in to, read from this profile's own config alone, and only
	// where it can explain the label: a Claude Code session whose `label`
	// option is `account`, with no label or one read from an account (or
	// of unknown source). Absent otherwise or when it cannot be read.
	SessionAccount string `json:"session_account,omitzero"`

	// The human form's own facts; --json never carries a path.
	accountFile, sessionFile, home string
	// claude: a Claude Code session, whose start fills an empty label from
	// its account; a Muse Code, Codex CLI or Grok Build session never does.
	claude bool
}

// label reads the facts. Every one is best effort, as the delivery facts
// are: a file that cannot be read leaves its fact out, with a debug line.
func (inv Invocation) label(t *target) whoamiLabel {
	l := whoamiLabel{Source: teamstore.LabelUnrecorded, home: adapterkit.HomeDir(inv.Environ)}
	l.claude = t.session == nil || !config.Host(t.session.Harness).Pointed()
	raw := ""
	if b, err := teamstore.LoadBinding(t.configDir, t.profile); err == nil {
		raw = b.HumanLabel
		if !b.CreatedAt.IsZero() {
			at := b.CreatedAt.UTC()
			l.CredentialSince = &at
		}
	} else {
		inv.logger().Debug("whoami: team binding unreadable; label read from describe", log.Err(err))
	}
	if raw == "" && t.describe != nil {
		raw = t.describe.Profile.HumanLabel
	}
	l.HumanLabel = protocol.SanitizeLabel(raw)
	if l.HumanLabel != "" {
		o, err := teamstore.LoadLabelOrigin(t.configDir, t.profile)
		switch {
		case err == nil && o.Describes(raw):
			l.Source, l.accountFile = protocol.SanitizeAttribute(o.Source), o.AccountFile
			if !o.SentAt.IsZero() {
				at := o.SentAt.UTC()
				l.SentAt = &at
			}
		case err == nil:
			// Something other than a recorded create or join wrote the
			// label since (a scripted join, an adapter): the record is
			// about another label and must not be shown against this one.
			inv.logger().Debug("whoami: label record describes another label; source unrecorded")
		case !errors.Is(err, fs.ErrNotExist):
			inv.logger().Debug("whoami: label record unreadable; source unrecorded", log.Err(err))
		}
	}
	explains := l.HumanLabel == "" || l.Source == teamstore.LabelFromAccount || l.Source == teamstore.LabelUnrecorded
	if !l.claude || !explains || inv.sessionLabelOption(t.stateDir) != config.LabelAccount {
		// A host with no Claude account (cards 88, 107, 120), a label the
		// member chose as text, or a `label` option other than `account`:
		// the session's account explains nothing, so it is not read.
		return l
	}
	email, file, err := account.OwnEmailFile(inv.Environ)
	if err != nil {
		inv.logger().Debug("whoami: claude account email unavailable", log.Err(err))
	}
	l.SessionAccount, l.sessionFile = email, file
	return l
}

// lines renders the facts for the human form: `label:` always, and
// `account:` when the session's account was read.
func (l whoamiLabel) lines() []string {
	account := oneLine(l.SessionAccount)
	if l.HumanLabel == "" {
		line := "label: none recorded on this machine; the MEMBER column of brigade sessions shows the label the team holds"
		if l.claude {
			line += ", which a session start fills from its Claude account while there is none"
		}
		out := []string{line}
		if account != "" {
			out = append(out, "account: this session's Claude account is "+account)
		}
		return out
	}
	line := "label: " + labelLine(l.HumanLabel) + ", sent when this machine joined"
	if l.Source == teamstore.LabelUnrecorded {
		if l.CredentialSince != nil {
			line += " (its credential for this team dates from " + l.CredentialSince.Format(time.DateOnly) + ")"
		}
		line += "; no record says where it came from"
	} else {
		if l.SentAt != nil {
			line += " on " + l.SentAt.Format(time.DateOnly)
		}
		line += ", " + sourceText(l.Source, homeShort(l.accountFile, l.home))
	}
	out := []string{line}
	switch {
	case account == "":
		return out
	case strings.EqualFold(account, oneLine(l.HumanLabel)):
		return append(out, "account: this session's Claude account is the label")
	case l.accountFile != "" && l.sessionFile != "" && filepath.Clean(l.accountFile) == filepath.Clean(l.sessionFile):
		// The label was read from this very profile: it has signed in to
		// another account since, and no other profile is involved.
		return append(out, "account: this session's Claude account is "+account+", not the label: "+
			"this profile has signed in to another account since the join, and the label stays the one sent then")
	default:
		return append(out, "account: this session's Claude account is "+account+", not the label: "+
			"every Claude profile that uses this machine's credential store is this one member, under the label sent when it joined; "+
			"to be a member of its own, a profile sets the config_dir option and joins from a session of its own")
	}
}

// sourceText is the human words for a recorded source.
func sourceText(source, accountFile string) string {
	switch source {
	case teamstore.LabelFromAccount:
		if p := pathLine(accountFile); p != "" {
			return "read from the Claude account in " + p
		}
		return "read from the Claude account signed in then"
	case teamstore.LabelFromOption:
		return "from the label option"
	case teamstore.LabelFromFlag:
		return "from --label"
	case teamstore.LabelFromPrompt:
		return "typed at team create's prompt"
	default:
		return "from " + enumLine(source)
	}
}

// homeShort writes a path under home as ~/…, so the line names the Claude
// profile without the member's home directory.
func homeShort(path, home string) string {
	if path == "" || home == "" {
		return path
	}
	home = filepath.Clean(home)
	if rel, err := filepath.Rel(home, filepath.Clean(path)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}
