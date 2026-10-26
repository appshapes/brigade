package syncthing

import (
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// adapterName is the `adapter.name` of 4.4.1.
const adapterName = progName

// adapterLease is this adapter's advertised lease range (4.4.1: "lease is
// the range of lease_seconds an adapter accepts"). min_seconds is 1, as
// the test fs adapter's, so the conformance suite's lease-expiry cases take
// seconds; a harness never asks for less than its heartbeat interval.
func adapterLease() protocol.Lease {
	return protocol.Lease{DefaultSeconds: 90, MinSeconds: 1, MaxSeconds: 600}
}

// capabilities is the 4.7 registry entries this adapter implements. It
// does NOT advertise message.watch.push: the watch polls the local replica,
// so its `ready` event says mode "polling" (C-33). team.admin is the three
// creator-only verbs of admin.go.
func capabilities() []string {
	return []string{
		"team.create",
		"team.join",
		"team.roster",
		"team.admin",
		"message.receive",
		"message.watch.stdin_commands",
		"session.description",
		"session.resume",
		"session.workspace_label",
		"session.inbound",
		"session.model",
		"session.context_used_tokens",
		"session.human_label",
		"session.brigade_version",
		"session.sync_peer",
	}
}

// describe answers 4.4.1 from local files only. It creates NO file and NO
// directory anywhere — not the config directory, not the store root — it
// starts no engine and it runs no retention sweep, because C-01 runs it on
// a scratch principal and asserts that nothing appeared.
func (c *command) describe() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	info := protocol.ProfileInfo{Name: c.profileName, State: state}
	if state == protocol.ProfileStateJoined {
		info.TeamRef, info.TeamName = p.TeamRef, p.TeamName
		info.PrincipalRef, info.HumanLabel = cred.PrincipalRef, p.HumanLabel
	}
	return &protocol.DescribeResult{
		ProtocolVersion: protocol.ProtocolVersion,
		Adapter:         protocol.AdapterInfo{Name: adapterName, Version: buildinfo.String()},
		Delivery: protocol.DeliveryInfo{
			Guarantee: protocol.GuaranteeAtLeastOnce,
			Ordering:  "none",
			AckState:  protocol.AckStateInjected,
		},
		Capabilities: capabilities(),
		Limits:       protocol.DefaultLimits(),
		Lease:        adapterLease(),
		Retention:    protocol.DefaultRetention(),
		Profile:      info,
	}, nil
}
