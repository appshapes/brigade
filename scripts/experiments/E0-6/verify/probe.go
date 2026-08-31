package main

import (
	"context"
	"strings"
)

// probe is the soak's (b) instrument, copied verbatim from the experiment's main.go: one
// authenticated RPC whose refusal code is the server's own verdict on the JWT.
func probe(ctx context.Context, env Env, jwt, teamID string) (int, string) {
	r := rpcQuiet(ctx, env, env.PublishableKey, jwt, "list_members", map[string]any{"p_team_id": teamID})
	if r.Status == 200 {
		return 200, ""
	}
	if e, ok := parsePgError(r.Body); ok {
		return r.Status, e.Code
	}
	return r.Status, strings.TrimSpace(truncate(string(r.Body), 120))
}
