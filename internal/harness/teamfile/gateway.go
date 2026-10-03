package teamfile

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/protocol"
)

// GatewayConfig is a usable `gateway` member (Trello card 48; the hosted
// mail gateway of .context/plans/human-sessions-slack-email.md): the
// address a person writes to so that their mail reaches a session of this
// team. It is a public value, like the backend url: the gateway accepts
// only mail it can route and every session treats what arrives as the
// untrusted text it is. The json tag is the member's own shape, so `team
// create --force` writes a carried GatewayConfig back unchanged.
type GatewayConfig struct {
	// Email is the mail gateway's inbox address, "" when the team has none.
	Email string `json:"email,omitempty"`
	// Slack names the Slack gateway for a person: the workspace and the
	// bot to message, as free text such as "appshapes.slack.com: @brigade";
	// "" when the team has none.
	Slack string `json:"slack,omitempty"`
}

// MaxGatewayEmailBytes bounds the address (RFC 5321's 254, rounded).
const MaxGatewayEmailBytes = 254

// MaxGatewaySlackRunes bounds the Slack line: a workspace and a handle.
const MaxGatewaySlackRunes = 128

// The closed list of reasons a `gateway` member is not usable. None is a
// refusal: the session connects, with no gateway known to this checkout.
const (
	GatewayNotObject    = "not_object"    // gateway is not a JSON object
	GatewayEmpty        = "empty"         // neither an email nor a slack member
	GatewayEmailInvalid = "email_invalid" // email is not a string of the shape local@domain.tld without spaces or controls
	GatewayEmailTooLong = "email_too_long"
	GatewaySlackInvalid = "slack_invalid" // slack is not a non-empty string free of control and format characters
	GatewaySlackTooLong = "slack_too_long"
)

// GatewayReasons is the closed unusable-gateway token list, in check order.
func GatewayReasons() []string {
	return []string{GatewayNotObject, GatewayEmpty, GatewayEmailInvalid, GatewayEmailTooLong, GatewaySlackInvalid, GatewaySlackTooLong}
}

// gatewayMembers is what this version reads inside `gateway`; any other
// inner member is ignored and named `gateway.<name>`.
var gatewayMembers = map[string]bool{"email": true, "slack": true}

// parseGateway validates the `gateway` member. It never refuses: an
// unusable member is (nil, ignored, token). The secret walk over the
// whole document already ran.
func parseGateway(member jsontext.Value) (*GatewayConfig, []string, string) {
	var obj map[string]jsontext.Value
	if member.Kind() != '{' || json.Unmarshal(member, &obj) != nil {
		return nil, nil, GatewayNotObject
	}
	var ignored []string
	for name := range obj {
		if !gatewayMembers[name] {
			ignored = append(ignored, "gateway."+displayName(name))
		}
	}
	rawEmail, hasEmail := obj["email"]
	rawSlack, hasSlack := obj["slack"]
	if !hasEmail && !hasSlack {
		return nil, ignored, GatewayEmpty
	}
	var cfg GatewayConfig
	if hasEmail {
		if err := json.Unmarshal(rawEmail, &cfg.Email); err != nil || !gatewayAddress(cfg.Email) {
			return nil, ignored, GatewayEmailInvalid
		}
		if len(cfg.Email) > MaxGatewayEmailBytes {
			return nil, ignored, GatewayEmailTooLong
		}
	}
	if hasSlack {
		if err := json.Unmarshal(rawSlack, &cfg.Slack); err != nil || cfg.Slack == "" || !plainText(cfg.Slack) {
			return nil, ignored, GatewaySlackInvalid
		}
		if utf8.RuneCountInString(cfg.Slack) > MaxGatewaySlackRunes {
			return nil, ignored, GatewaySlackTooLong
		}
	}
	return &cfg, ignored, ""
}

// plainText reports whether s is free of control and format characters
// (spaces are fine: the Slack line is prose).
func plainText(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// gatewayAddress is the shape rule: exactly one `@`, a non-empty local
// part, a domain with a dot, and no whitespace, control or format
// character anywhere. Not RFC 5322; enough to tell an address from
// anything else a file might carry.
func gatewayAddress(s string) bool {
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	domain := s[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune("<>\"", r) {
			return false
		}
	}
	return true
}

// CarriedGateway is what `team create --force` keeps of the project's
// `gateway` member, by the same rules as CarriedSync: the usable member,
// or nil and the token saying why nothing came across ("" when there was
// nothing to carry).
func CarriedGateway(filePath string) (carried *GatewayConfig, cause string) {
	data, err := readCapped(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		var perr *protocol.Error
		if errors.As(err, &perr) {
			return nil, perr.Details["reason"]
		}
		return nil, SyncNotCarriedUnreadable
	}
	f, perr := parseBytes(filePath, data)
	if perr == nil {
		return f.Gateway, f.GatewayUnusable
	}
	var raw map[string]jsontext.Value
	if json.Unmarshal(data, &raw) == nil {
		if _, ok := raw["gateway"]; ok {
			var pe *protocol.Error
			if errors.As(perr, &pe) {
				return nil, pe.Details["reason"]
			}
			return nil, ReasonMalformed
		}
	}
	return nil, ""
}
