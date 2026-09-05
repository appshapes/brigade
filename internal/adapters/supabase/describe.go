package supabase

import (
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// adapterName is the `adapter.name` of 4.4.1.
const adapterName = progName

// capabilities is the 4.7 registry entries this adapter implements: every
// team convention including team.admin (P5-2: rotate-secret, revoke-member
// and transfer, creator only), the push watch with stdin commands, and
// every session member (brief section 2).
func capabilities() []string {
	return []string{
		"team.create",
		"team.join",
		"team.roster",
		"team.admin",
		"message.receive",
		"message.watch.push",
		"message.watch.stdin_commands",
		"session.description",
		"session.resume",
		"session.workspace_label",
		"session.inbound",
	}
}

// describe answers 4.4.1 from local files only. It creates NO file and NO
// directory — not the config directory, not a lock sidecar, not a log —
// and it never dials: C-01 and C-07 run it under BRIGADE_TEST_OFFLINE=1,
// and C-01 asserts that nothing appeared under the principal's
// directories. The lease range is the protocol's own (30..600), which is
// what the backend enforces.
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
		info.PrincipalRef = cred.principalRef()
		if info.PrincipalRef == "" {
			info.PrincipalRef = p.PrincipalRef
		}
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
