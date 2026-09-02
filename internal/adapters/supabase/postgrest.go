package supabase

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"regexp"
)

// PostgREST (5.11): POST /rest/v1/rpc/<fn> with apikey, Authorization:
// Bearer <access token>, Content-Type: application/json and BOTH profile
// headers — Content-Profile alone suffices for POST /rpc/*, but without
// it PostgREST looks for public.<fn> and answers 404 PGRST202. The body of
// a 200 is the function's jsonb verbatim. Never `?apikey=` on /rest/v1: it
// is parsed as a column filter (400 PGRST100). Ported from E0-1.

// schemaProfile is the exposed schema every RPC lives in (D22).
const schemaProfile = "brigade"

// rpcPath prefixes every function name.
const rpcPath = "/rest/v1/rpc/"

// rpcArgs is one RPC's named arguments, as the migrations declare them
// (p_team_id, p_name, …). A nil map sends `{}`.
type rpcArgs map[string]any

// callRPC performs one RPC with the given access token and returns the
// raw answer; the caller reads the status and maps the body. A transport
// failure is already the mapped *protocol.Error.
func (cl *client) callRPC(ctx context.Context, token, fn string, args rpcArgs) (*response, error) {
	if args == nil {
		args = rpcArgs{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, errInternal("the RPC arguments could not be encoded")
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"Accept-Profile":  schemaProfile,
		"Content-Profile": schemaProfile,
		"Content-Type":    "application/json",
	}
	return cl.post(ctx, rpcPath+fn, headers, body)
}

// rpc is the command-level call every verb uses: a fresh access token
// (refreshed under the flock when fewer than 90 s remain), one call, and
// on PGRST301/PGRST303 — a JWT the server would not honour, the cold-start
// path of 5.1 — one forced refresh and one retry, then `unauthenticated`.
// A 200 body is decoded into out (a pointer; nil discards the body); any
// other answer is the mapped error of errors.go, with the server's own
// text logged at debug through the redactor and never returned.
func (c *command) rpc(ctx context.Context, fn string, args rpcArgs, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.client.callRPC(ctx, token, fn, args)
		if err != nil {
			return err
		}
		if resp.status == http.StatusOK {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(resp.body, out); err != nil {
				c.log.Debug("rpc result did not decode", slog.String("fn", fn))
				return errUnexpectedResponse("the backend answered with a result this adapter could not decode")
			}
			return nil
		}
		pg, isPostgrest := parsePostgrestError(resp.body)
		c.logRPCFailure(fn, resp.status, pg, isPostgrest, resp.body)
		if isPostgrest && pg.jwtRejected() && attempt == 0 {
			if token, err = c.forceRefresh(ctx); err != nil {
				return err
			}
			continue
		}
		return mapRPCFailure(resp.status, resp.body)
	}
}

// logRPCFailure records a refused RPC at debug: scalars only, and the
// server's text through the redacting handler.
func (c *command) logRPCFailure(fn string, status int, pg postgrestError, isPostgrest bool, body []byte) {
	if isPostgrest {
		c.log.Debug("rpc refused", slog.String("fn", fn), slog.Int("status", status),
			slog.String("sqlstate", pg.Code), slog.String("server_message", pg.Message))
		return
	}
	text := string(body)
	if len(text) > 256 {
		text = text[:256]
	}
	c.log.Debug("rpc failed without a PostgREST body", slog.String("fn", fn), slog.Int("status", status), slog.String("body", text))
}

// uuidPattern is the shape of every identifier the backend issues:
// team_ref, principal_ref, session_id and message_id are all uuids. An
// id that is not one can name nothing, so a verb answers the uniform
// error locally instead of sending it to a uuid-typed argument (22P02).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validUUID reports whether s is uuid-shaped.
func validUUID(s string) bool {
	return uuidPattern.MatchString(s)
}
