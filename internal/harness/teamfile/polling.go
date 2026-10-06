package teamfile

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/appshapes/brigade/internal/harness/polling"
)

// PollingConfig is a usable `polling` member (card 53): how often this
// team's sessions heartbeat and how often folder sync reads the roster,
// each in whole seconds, 0 for a value the member leaves out (the
// default, package polling). A team tunes them against its backend's
// request budget; nothing here changes how Brigade connects or what it
// trusts. The json tags are the member's own shape, so `team create
// --force` writes a carried PollingConfig back unchanged.
type PollingConfig struct {
	HeartbeatSeconds int `json:"heartbeat_seconds,omitzero"`
	RosterSeconds    int `json:"roster_seconds,omitzero"`
}

// The closed list of reasons a `polling` member is not usable. None is a
// refusal: the hook renders the token in its one fixed line and the
// session connects on the default intervals. The first rule a member
// breaks, in the order below, is the one reported.
const (
	PollingNotObject           = "not_object"             // polling is not a JSON object
	PollingHeartbeatInvalid    = "heartbeat_invalid"      // heartbeat_seconds is not an integer
	PollingHeartbeatOutOfRange = "heartbeat_out_of_range" // heartbeat_seconds is outside polling's heartbeat bounds
	PollingRosterInvalid       = "roster_invalid"         // roster_seconds is not an integer
	PollingRosterOutOfRange    = "roster_out_of_range"    // roster_seconds is outside polling's roster bounds
)

// PollingReasons is the closed unusable-polling token list, in check order.
func PollingReasons() []string {
	return []string{PollingNotObject, PollingHeartbeatInvalid, PollingHeartbeatOutOfRange, PollingRosterInvalid, PollingRosterOutOfRange}
}

// pollingMembers is what this version reads inside `polling`; any other
// inner member is ignored and named `polling.<name>`.
var pollingMembers = map[string]bool{"heartbeat_seconds": true, "roster_seconds": true}

// parsePolling validates the `polling` member. It never refuses: an
// unusable member is (nil, ignored, token), and the session runs on the
// defaults. The secret walk over the whole document already ran.
func parsePolling(member jsontext.Value) (*PollingConfig, []string, string) {
	var obj map[string]jsontext.Value
	if member.Kind() != '{' || json.Unmarshal(member, &obj) != nil {
		return nil, nil, PollingNotObject
	}
	var ignored []string
	for name := range obj {
		if !pollingMembers[name] {
			ignored = append(ignored, "polling."+displayName(name))
		}
	}
	var cfg PollingConfig
	if reason := pollingValue(obj, "heartbeat_seconds", polling.HeartbeatInRange, &cfg.HeartbeatSeconds,
		PollingHeartbeatInvalid, PollingHeartbeatOutOfRange); reason != "" {
		return nil, ignored, reason
	}
	if reason := pollingValue(obj, "roster_seconds", polling.RosterInRange, &cfg.RosterSeconds,
		PollingRosterInvalid, PollingRosterOutOfRange); reason != "" {
		return nil, ignored, reason
	}
	return &cfg, ignored, ""
}

// pollingValue reads one optional seconds value into dst: absent leaves
// it 0 (the default); a value that is not a JSON integer is invalid, and
// one outside inRange is outOfRange.
func pollingValue(obj map[string]jsontext.Value, name string, inRange func(int) bool, dst *int, invalid, outOfRange string) string {
	v, ok := obj[name]
	if !ok {
		return ""
	}
	if v.Kind() != '0' || json.Unmarshal(v, dst) != nil {
		*dst = 0
		return invalid
	}
	if !inRange(*dst) {
		*dst = 0
		return outOfRange
	}
	return ""
}

// CarriedPolling is what `team create --force` keeps of the project's
// `polling` member, by the same rules as CarriedSync: the usable member,
// or nil and the token saying why nothing came across ("" when there was
// nothing to carry).
func CarriedPolling(filePath string) (carried *PollingConfig, cause string) {
	return carriedMember(filePath, "polling", func(f *File) (*PollingConfig, string) { return f.Polling, f.PollingUnusable })
}
