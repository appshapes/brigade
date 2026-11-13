package rest

import (
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// adapterName is the `adapter.name` of 4.4.1.
const adapterName = progName

// capTeamAdmin is the capability of the three administration commands
// (4.7), and the name a server lists in `/v1/info` when it carries their
// endpoints (the contract's section 3a).
const capTeamAdmin = "team.admin"

// capabilities is the 4.7 registry entries this adapter advertises: what
// the contract's section 5 requires of every version 1 server (the three
// team conventions and the nine session members), `message.receive`, the
// two the adapter earns itself (`message.watch.push`, because a waiting
// receive answers within a second of an acceptance, section 4, and
// `message.watch.stdin_commands`), and `team.admin`, the administration
// endpoints of section 3a, which a server may leave out. The list is
// fixed, because describe never asks the server anything: whether this
// profile's server carries those endpoints is asked before each
// administration command, which answers `config` when it does not
// (requireTeamAdmin).
func capabilities() []string {
	return []string{
		"team.create",
		"team.join",
		"team.roster",
		capTeamAdmin,
		"message.receive",
		"message.watch.push",
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
// directory and it never dials: C-01 and C-07 run it under
// BRIGADE_TEST_OFFLINE=1, and C-01 asserts that nothing appeared under the
// principal's directories. The limits, the lease range and the retention
// floors are the protocol's v1 constants, which the contract's section 5
// makes every server implement exactly.
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
		info.TeamRef, info.TeamName, info.HumanLabel = p.TeamRef, p.TeamName, p.HumanLabel
		info.PrincipalRef = principalOf(p, cred)
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
		Lease:        protocol.DefaultLease(),
		Retention:    protocol.DefaultRetention(),
		Profile:      info,
	}, nil
}
