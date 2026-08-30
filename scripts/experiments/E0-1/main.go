package main

// E0-1 checks (a) and (i): live verification of the brigade schema's security
// properties against the running local Supabase stack, over raw HTTP.
//
// Ground truth: supabase/migrations/20260830120000_brigade_schema.sql (+ realtime,
// housekeeping). Writes are RPC-only; anon has no schema usage; service_role has
// schema USAGE only (no table, no function privileges).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ---------- small utilities ----------

func randHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func randUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type resultRow struct {
	ID       string
	Pass     bool
	Evidence string
}

var results []resultRow

func check(id string, pass bool, evidence string) {
	results = append(results, resultRow{id, pass, evidence})
	st := "PASS"
	if !pass {
		st = "FAIL"
	}
	fmt.Printf("[%s] %s — %s\n", st, id, redact(evidence))
}

func rows(b []byte) []map[string]any {
	var r []map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		return nil
	}
	return r
}

func colHas(rs []map[string]any, col, val string) bool {
	for _, r := range rs {
		if s, ok := r[col].(string); ok && s == val {
			return true
		}
	}
	return false
}

func colAll(rs []map[string]any, col, val string) bool {
	for _, r := range rs {
		if s, ok := r[col].(string); !ok || s != val {
			return false
		}
	}
	return true
}

// ---------- REST helpers (tables; the RPC helpers live in postgrest.go) ----------

func restGet(ctx context.Context, env Env, apikey, jwt, q, label string) Resp {
	h := map[string]string{"apikey": apikey, "Accept-Profile": "brigade"}
	if jwt != "" {
		h["Authorization"] = "Bearer " + jwt
	}
	return do(ctx, label, "GET", env.APIURL+"/rest/v1/"+q, h, nil)
}

func restGetQuiet(ctx context.Context, env Env, apikey, jwt, q string) Resp {
	h := map[string]string{"apikey": apikey, "Accept-Profile": "brigade"}
	if jwt != "" {
		h["Authorization"] = "Bearer " + jwt
	}
	return doQuiet(ctx, "GET", env.APIURL+"/rest/v1/"+q, h, nil)
}

func restWrite(ctx context.Context, env Env, apikey, jwt, method, q string, body any, label string) Resp {
	h := map[string]string{"apikey": apikey, "Content-Type": "application/json",
		"Accept-Profile": "brigade", "Content-Profile": "brigade"}
	if jwt != "" {
		h["Authorization"] = "Bearer " + jwt
	}
	return do(ctx, label, method, env.APIURL+"/rest/v1/"+q, h, body)
}

// rpcBare: RPC with an apikey but NO Authorization header (quiet).
func rpcBare(ctx context.Context, env Env, apikey, fn string, args any) Resp {
	h := map[string]string{"apikey": apikey, "Content-Type": "application/json",
		"Accept-Profile": "brigade", "Content-Profile": "brigade"}
	if args == nil {
		args = map[string]any{}
	}
	return doQuiet(ctx, "POST", env.APIURL+"/rest/v1/rpc/"+fn, h, args)
}

// rpcSvc: RPC as the service principal — secret key, optionally with the legacy
// service_role JWT as Authorization to pin the Postgres role beyond doubt.
func rpcSvc(ctx context.Context, env Env, withJWT bool, fn string, args any) Resp {
	h := map[string]string{"apikey": env.SecretKey, "Content-Type": "application/json",
		"Accept-Profile": "brigade", "Content-Profile": "brigade"}
	if withJWT {
		h["Authorization"] = "Bearer " + env.ServiceRoleJWT
	}
	if args == nil {
		args = map[string]any{}
	}
	return doQuiet(ctx, "POST", env.APIURL+"/rest/v1/rpc/"+fn, h, args)
}

func svcGet(ctx context.Context, env Env, withJWT bool, q string) Resp {
	h := map[string]string{"apikey": env.SecretKey, "Accept-Profile": "brigade"}
	if withJWT {
		h["Authorization"] = "Bearer " + env.ServiceRoleJWT
	}
	return doQuiet(ctx, "GET", env.APIURL+"/rest/v1/"+q, h, nil)
}

// byteIdent prints both payloads and checks status + body byte equality.
func byteIdent(id, l1 string, r1 Resp, l2 string, r2 Resp) bool {
	fmt.Printf("  %-32s HTTP %d  body: %s\n", l1+":", r1.Status, string(r1.Body))
	fmt.Printf("  %-32s HTTP %d  body: %s\n", l2+":", r2.Status, string(r2.Body))
	same := r1.Status == r2.Status && bytes.Equal(r1.Body, r2.Body)
	check(id, same, fmt.Sprintf("HTTP %d vs %d; bodies byte-identical=%v (%dB vs %dB); payload=%s",
		r1.Status, r2.Status, same, len(r1.Body), len(r2.Body), string(r1.Body)))
	return same
}

func pgCodeMsg(r Resp) (string, string) {
	e, ok := parsePgError(r.Body)
	if !ok {
		return "", string(r.Body)
	}
	return e.Code, e.Message
}

// mustOK: fixture call that has to succeed or the whole run is void.
func mustOK(r Resp, what string) {
	if r.Status != 200 {
		fatal("fixture %s failed: HTTP %d body=%s", what, r.Status, redact(string(r.Body)))
	}
}

func mustStr(b []byte, what string, path ...string) string {
	v, ok := jsonGet[string](b, path...)
	if !ok {
		fatal("fixture: missing %s (%s) in %s", strings.Join(path, "."), what, redact(string(b)))
	}
	return v
}

// ---------- main ----------

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env := loadEnv(".env.test")
	pub := env.PublishableKey

	fmt.Println("E0-1 checks (a) and (i): brigade schema security properties, live stack", env.APIURL)
	fmt.Println("run at:", time.Now().Format(time.RFC3339))

	// ---------------- fixtures ----------------
	section("fixtures: four anonymous principals, two teams, five sessions, three messages")

	sA, r := signUpAnonymous(ctx, env, pub, "signup A (team A owner)", nil, nil)
	mustOK(r, "signup A")
	sA2, r := signUpAnonymous(ctx, env, pub, "signup A2 (second member of team A)", nil, nil)
	mustOK(r, "signup A2")
	sB, r := signUpAnonymous(ctx, env, pub, "signup B (team B owner)", nil, nil)
	mustOK(r, "signup B")
	sC, r := signUpAnonymous(ctx, env, pub, "signup C (member of neither team)", nil, nil)
	mustOK(r, "signup C")

	roleA, _ := jwtClaims(sA.AccessToken)["role"].(string)
	check("setup-role", roleA == "authenticated",
		fmt.Sprintf("anonymous sign-in JWT role claim = %q (want authenticated); is_anonymous=%v", roleA, sA.User.IsAnonymous))

	r = rpc(ctx, env, pub, sA.AccessToken, "create_team", map[string]any{"p_name": "E01 Team A", "p_human_label": "alice"}, "both", "A: create_team")
	mustOK(r, "create_team A")
	teamA := mustStr(r.Body, "create_team A", "team_id")
	secretA := mustStr(r.Body, "create_team A", "join_secret")

	r = rpc(ctx, env, pub, sB.AccessToken, "create_team", map[string]any{"p_name": "E01 Team B", "p_human_label": "bob"}, "both", "B: create_team")
	mustOK(r, "create_team B")
	teamB := mustStr(r.Body, "create_team B", "team_id")

	r = rpc(ctx, env, pub, sA2.AccessToken, "join_team", map[string]any{"p_join_secret": secretA, "p_human_label": "alice-2"}, "both", "A2: join_team(team A)")
	mustOK(r, "join_team A2")
	if st, _ := jsonGet[string](r.Body, "status"); st != "joined" {
		fatal("A2 join_team status=%s", st)
	}

	reg := func(s Session, team, name, label string) string {
		r := rpc(ctx, env, pub, s.AccessToken, "register_session", map[string]any{"p_team_id": team, "p_name": name}, "both", label)
		mustOK(r, "register_session "+name)
		return mustStr(r.Body, name, "session_id")
	}
	sessA1 := reg(sA, teamA, "e01-a1", "A: register_session a1")
	sessA2 := reg(sA, teamA, "e01-a2", "A: register_session a2")
	sessA2s := reg(sA2, teamA, "e01-a2s", "A2: register_session a2s")
	sessB1 := reg(sB, teamB, "e01-b1", "B: register_session b1")
	sessB2 := reg(sB, teamB, "e01-b2", "B: register_session b2")

	send := func(s Session, from, to, body, key, label string) Resp {
		return rpc(ctx, env, pub, s.AccessToken, "send_message", map[string]any{
			"p_sender_session_id": from, "p_recipient_session_id": to,
			"p_body": body, "p_idempotency_key": key}, "both", label)
	}
	r = send(sA, sessA1, sessA2, "hello inside team A", "e01-k1-"+randHex(4), "A: send a1->a2")
	mustOK(r, "send A")
	msgA := mustStr(r.Body, "msgA", "message_id")
	r = send(sB, sessB1, sessB2, "hello inside team B", "e01-k2-"+randHex(4), "B: send b1->b2")
	mustOK(r, "send B")
	msgB := mustStr(r.Body, "msgB", "message_id")
	r = send(sA, sessA1, sessA2s, "hello A2 from A", "e01-k3-"+randHex(4), "A: send a1->a2s (to A2)")
	mustOK(r, "send A->A2")
	msgToA2 := mustStr(r.Body, "msgToA2", "message_id")
	msgToA2Created := mustStr(r.Body, "msgToA2", "created_at")

	uidA, uidA2, uidB := sA.User.ID, sA2.User.ID, sB.User.ID
	fmt.Printf("\nfixture ids:\n  teamA=%s teamB=%s\n  uidA=%s uidA2=%s uidB=%s uidC=%s\n  sessA1=%s sessA2=%s sessA2s=%s\n  sessB1=%s sessB2=%s\n  msgA=%s msgB=%s msgToA2=%s\n",
		teamA, teamB, uidA, uidA2, uidB, sC.User.ID, sessA1, sessA2, sessA2s, sessB1, sessB2, msgA, msgB, msgToA2)

	// The isolation tests below are only meaningful if the fixtures really are distinct.
	check("setup-distinct", teamA != teamB && uidA != uidA2 && uidA != uidB && uidA2 != uidB && sC.User.ID != uidA && sC.User.ID != uidA2 && sC.User.ID != uidB,
		fmt.Sprintf("teamA!=teamB=%v; four distinct principals=%v", teamA != teamB,
			uidA != uidA2 && uidA != uidB && uidA2 != uidB && sC.User.ID != uidA && sC.User.ID != uidA2 && sC.User.ID != uidB))

	// ---------------- a1: team isolation on every table via direct select ----------------
	section("a1: team isolation on every table through direct select (RLS + grants)")

	teamCols := "select=id,name,secret_version,created_at"

	r = restGet(ctx, env, pub, sA.AccessToken, "teams?"+teamCols, "A: select teams")
	rs := rows(r.Body)
	check("a1-teams-A", r.Status == 200 && len(rs) == 1 && colHas(rs, "id", teamA) && !colHas(rs, "id", teamB),
		fmt.Sprintf("A sees %d team row(s); contains teamA=%v; contains teamB=%v", len(rs), colHas(rs, "id", teamA), colHas(rs, "id", teamB)))

	r = restGet(ctx, env, pub, sA.AccessToken, "memberships?select=team_id,user_id,status&order=joined_at", "A: select memberships")
	rs = rows(r.Body)
	check("a1-memberships-A-roster", r.Status == 200 && len(rs) == 2 && colAll(rs, "team_id", teamA) &&
		colHas(rs, "user_id", uidA) && colHas(rs, "user_id", uidA2) && !colHas(rs, "user_id", uidB),
		fmt.Sprintf("A sees %d membership row(s), all team A=%v; roster has A=%v and A2=%v; has B=%v (D22 roster, no cross-team leak)",
			len(rs), colAll(rs, "team_id", teamA), colHas(rs, "user_id", uidA), colHas(rs, "user_id", uidA2), colHas(rs, "user_id", uidB)))

	r = restGet(ctx, env, pub, sA.AccessToken, "sessions?select=id,team_id,owner_id", "A: select sessions")
	rs = rows(r.Body)
	check("a1-sessions-A", r.Status == 200 && len(rs) == 3 && colAll(rs, "team_id", teamA) &&
		colHas(rs, "id", sessA1) && colHas(rs, "id", sessA2) && colHas(rs, "id", sessA2s) &&
		!colHas(rs, "id", sessB1) && !colHas(rs, "id", sessB2),
		fmt.Sprintf("A sees %d session row(s), all team A=%v; any team B session=%v",
			len(rs), colAll(rs, "team_id", teamA), colHas(rs, "id", sessB1) || colHas(rs, "id", sessB2)))

	r = restGet(ctx, env, pub, sA.AccessToken, "messages?select=id,team_id,sender_user_id", "A: select messages")
	rs = rows(r.Body)
	check("a1-messages-A", r.Status == 200 && len(rs) == 2 && colAll(rs, "team_id", teamA) &&
		colHas(rs, "id", msgA) && colHas(rs, "id", msgToA2) && !colHas(rs, "id", msgB),
		fmt.Sprintf("A sees %d message row(s), all team A=%v; sees own msgA=%v msgToA2=%v; sees team-B msgB=%v",
			len(rs), colAll(rs, "team_id", teamA), colHas(rs, "id", msgA), colHas(rs, "id", msgToA2), colHas(rs, "id", msgB)))

	r = restGet(ctx, env, pub, sB.AccessToken, "teams?"+teamCols, "B: select teams")
	rs = rows(r.Body)
	check("a1-teams-B", r.Status == 200 && len(rs) == 1 && colHas(rs, "id", teamB) && !colHas(rs, "id", teamA),
		fmt.Sprintf("B sees %d team row(s); contains teamB=%v; contains teamA=%v", len(rs), colHas(rs, "id", teamB), colHas(rs, "id", teamA)))

	r = restGet(ctx, env, pub, sA2.AccessToken, "memberships?select=team_id,user_id,status", "A2: select memberships (roster as non-creator)")
	rs = rows(r.Body)
	check("a1-memberships-A2-roster", r.Status == 200 && len(rs) == 2 && colAll(rs, "team_id", teamA) &&
		colHas(rs, "user_id", uidA) && colHas(rs, "user_id", uidA2),
		fmt.Sprintf("A2 (plain member) sees %d roster row(s), all team A=%v, includes creator A=%v", len(rs), colAll(rs, "team_id", teamA), colHas(rs, "user_id", uidA)))

	for _, t := range []string{"teams?" + teamCols, "memberships?select=*", "sessions?select=*", "messages?select=*"} {
		name := strings.SplitN(t, "?", 2)[0]
		r = restGet(ctx, env, pub, sC.AccessToken, t, "C: select "+name)
		rs = rows(r.Body)
		check("a1-"+name+"-C-empty", r.Status == 200 && len(rs) == 0,
			fmt.Sprintf("C (member of no team) gets HTTP %d with %d row(s): %s", r.Status, len(rs), string(r.Body)))
	}

	r = restGet(ctx, env, pub, sA.AccessToken, "teams?select=*", "A: select teams * (must hit secret_hash column grant)")
	code, msg := pgCodeMsg(r)
	check("a1-teams-star-42501", r.Status == 403 && code == "42501",
		fmt.Sprintf("select * on teams even for a member: HTTP %d code=%s msg=%q (secret_hash never readable)", r.Status, code, msg))

	r = restGet(ctx, env, pub, sA.AccessToken, "join_attempts?select=*", "A: select join_attempts (server-only table)")
	code, msg = pgCodeMsg(r)
	check("a1-join_attempts-42501", r.Status == 403 && code == "42501",
		fmt.Sprintf("join_attempts as authenticated member: HTTP %d code=%s msg=%q", r.Status, code, msg))

	// ---------------- a2: server stamping; no forgeable identity columns ----------------
	section("a2: server stamping — direct writes denied, forged RPC args rejected, stamped values correct")

	r = restWrite(ctx, env, pub, sA.AccessToken, "POST", "messages", map[string]any{
		"team_id": teamB, "sender_user_id": uidB, "sender_session_id": sessB1,
		"recipient_session_id": sessB2, "body": "forged", "body_hash": "\\x00",
		"idempotency_key": "forge-" + randHex(4), "created_at": "2020-01-01T00:00:00Z",
	}, "A: direct INSERT into messages with forged team/sender/created_at")
	code, msg = pgCodeMsg(r)
	check("a2-direct-insert-messages", r.Status == 403 && code == "42501",
		fmt.Sprintf("direct INSERT messages: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = restWrite(ctx, env, pub, sA.AccessToken, "POST", "sessions", map[string]any{
		"team_id": teamA, "owner_id": uidB, "name": "forged-owner",
	}, "A: direct INSERT into sessions with forged owner_id")
	code, msg = pgCodeMsg(r)
	check("a2-direct-insert-sessions", r.Status == 403 && code == "42501",
		fmt.Sprintf("direct INSERT sessions: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = restWrite(ctx, env, pub, sA.AccessToken, "PATCH", "sessions?id=eq."+sessA1, map[string]any{"owner_id": uidB},
		"A: direct UPDATE sessions.owner_id (mailbox theft attempt)")
	code, msg = pgCodeMsg(r)
	check("a2-direct-update-sessions", r.Status == 403 && code == "42501",
		fmt.Sprintf("direct UPDATE sessions: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = restWrite(ctx, env, pub, sA.AccessToken, "PATCH", "messages?id=eq."+msgA, map[string]any{"created_at": "2020-01-01T00:00:00Z"},
		"A: direct UPDATE messages.created_at")
	code, msg = pgCodeMsg(r)
	check("a2-direct-update-messages", r.Status == 403 && code == "42501",
		fmt.Sprintf("direct UPDATE messages: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = restWrite(ctx, env, pub, sA.AccessToken, "DELETE", "messages?id=eq."+msgA, nil, "A: direct DELETE messages")
	code, msg = pgCodeMsg(r)
	check("a2-direct-delete-messages", r.Status == 403 && code == "42501",
		fmt.Sprintf("direct DELETE messages: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = rpc(ctx, env, pub, sA.AccessToken, "send_message", map[string]any{
		"p_sender_session_id": sessA1, "p_recipient_session_id": sessA2,
		"p_body": "forge", "p_idempotency_key": "forge-" + randHex(4),
		"sender_user_id": uidB, "team_id": teamB, "created_at": "2020-01-01T00:00:00Z",
	}, "both", "A: send_message with extra sender_user_id/team_id/created_at args")
	code, msg = pgCodeMsg(r)
	check("a2-rpc-extra-args-send", r.Status == 404 && code == "PGRST202",
		fmt.Sprintf("send_message with forged identity args: HTTP %d code=%s msg=%q — no signature accepts them", r.Status, code, msg))

	r = rpc(ctx, env, pub, sA.AccessToken, "register_session", map[string]any{
		"p_team_id": teamA, "p_name": "forge", "owner_id": uidB, "created_at": "2020-01-01T00:00:00Z",
	}, "both", "A: register_session with extra owner_id/created_at args")
	code, msg = pgCodeMsg(r)
	check("a2-rpc-extra-args-register", r.Status == 404 && code == "PGRST202",
		fmt.Sprintf("register_session with forged identity args: HTTP %d code=%s msg=%q", r.Status, code, msg))

	r = restGet(ctx, env, pub, sA.AccessToken, "messages?id=eq."+msgToA2+"&select=id,team_id,sender_user_id,created_at", "A: verify stamped message columns")
	rs = rows(r.Body)
	stampedOK := false
	stampEvid := "message row not readable"
	if len(rs) == 1 {
		created, _ := rs[0]["created_at"].(string)
		ct, errT := time.Parse(time.RFC3339Nano, created)
		rt, errR := time.Parse(time.RFC3339Nano, msgToA2Created)
		drift := time.Duration(0)
		if errT == nil {
			drift = time.Since(ct)
			if drift < 0 {
				drift = -drift
			}
		}
		stampedOK = rs[0]["sender_user_id"] == uidA && rs[0]["team_id"] == teamA &&
			errT == nil && errR == nil && ct.Equal(rt) && drift < 5*time.Minute
		stampEvid = fmt.Sprintf("sender_user_id=%v (want uidA=%s), team_id=%v (want teamA=%s), created_at=%s (equals RPC-returned %s: %v; drift from local clock %s)",
			rs[0]["sender_user_id"], uidA, rs[0]["team_id"], teamA, created, msgToA2Created, errT == nil && errR == nil && ct.Equal(rt), drift.Round(time.Millisecond))
	}
	check("a2-stamped-message", stampedOK, stampEvid)

	r = restGet(ctx, env, pub, sA.AccessToken, "sessions?id=eq."+sessA1+"&select=id,owner_id,team_id,created_at", "A: verify stamped session owner")
	rs = rows(r.Body)
	check("a2-stamped-session-owner", len(rs) == 1 && rs[0]["owner_id"] == uidA && rs[0]["team_id"] == teamA,
		fmt.Sprintf("sessions.owner_id=%v (want uidA=%s), team_id=%v", func() any {
			if len(rs) == 1 {
				return rs[0]["owner_id"]
			}
			return "<no row>"
		}(), uidA, func() any {
			if len(rs) == 1 {
				return rs[0]["team_id"]
			}
			return "<no row>"
		}()))

	// ---------------- a3: uniform not_found — foreign vs unknown session ids ----------------
	// Positive controls first: each RPC compared below must SUCCEED on an owned session, or the
	// byte-identical not_found pairs would prove nothing (both sides could fail for any reason).
	section("a3 controls: the same RPCs succeed on an OWNED session (send already proven by the fixtures)")

	r = rpc(ctx, env, pub, sA.AccessToken, "session_heartbeat", map[string]any{"p_session_id": sessA1}, "both", "A: heartbeat OWNED sessA1 (control)")
	sidHB, _ := jsonGet[string](r.Body, "session_id")
	check("a3-ctl-heartbeat", r.Status == 200 && sidHB == sessA1,
		fmt.Sprintf("heartbeat on owned session: HTTP %d, session_id=%s (want %s)", r.Status, sidHB, sessA1))

	r = rpc(ctx, env, pub, sA.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sessA2}, "both", "A: fetch_inbox OWNED sessA2 (control)")
	rs = rows(r.Body)
	check("a3-ctl-fetch_inbox", r.Status == 200 && colHas(rs, "message_id", msgA),
		fmt.Sprintf("fetch_inbox on owned session: HTTP %d, %d message(s), contains msgA=%v", r.Status, len(rs), colHas(rs, "message_id", msgA)))

	r = rpc(ctx, env, pub, sA.AccessToken, "ack_messages", map[string]any{"p_session_id": sessA2, "p_message_ids": []string{msgA}}, "both", "A: ack OWNED sessA2 [msgA] (control)")
	ackOK := r.Status == 200 && strings.Contains(string(r.Body), msgA) && strings.Contains(string(r.Body), `"unknown": []`)
	check("a3-ctl-ack", ackOK, fmt.Sprintf("ack on owned session: HTTP %d body=%s", r.Status, string(r.Body)))

	r = rpc(ctx, env, pub, sA.AccessToken, "register_session", map[string]any{"p_team_id": teamA, "p_name": "e01-a3ctl"}, "both", "A: register scratch session (control)")
	mustOK(r, "register scratch e01-a3ctl")
	sessCtl := mustStr(r.Body, "e01-a3ctl", "session_id")

	r = rpc(ctx, env, pub, sA.AccessToken, "close_session", map[string]any{"p_session_id": sessCtl}, "both", "A: close OWNED scratch session (control)")
	stCl, _ := jsonGet[string](r.Body, "state")
	check("a3-ctl-close", r.Status == 200 && stCl == "offline",
		fmt.Sprintf("close on owned session: HTTP %d, state=%q (want offline)", r.Status, stCl))

	r = rpc(ctx, env, pub, sA.AccessToken, "register_session", map[string]any{
		"p_team_id": teamA, "p_name": "e01-a3ctl-r", "p_resume_session_id": sessCtl}, "both", "A: resume OWNED closed scratch session (control)")
	resumed, _ := jsonGet[bool](r.Body, "resumed")
	sidRes, _ := jsonGet[string](r.Body, "session_id")
	check("a3-ctl-resume", r.Status == 200 && resumed && sidRes == sessCtl,
		fmt.Sprintf("resume of owned closed session: HTTP %d, resumed=%v, session_id=%s (want %s)", r.Status, resumed, sidRes, sessCtl))

	section("a3: uniform not_found — foreign (team B) session id vs random unknown uuid, byte-for-byte")

	unknown := randUUID()
	fmt.Printf("foreign id = sessB1 = %s (real, team B); unknown id = %s (random)\n", sessB1, unknown)

	k := "e01-a3-" + randHex(4)
	rF := rpc(ctx, env, pub, sA.AccessToken, "send_message", map[string]any{
		"p_sender_session_id": sessA1, "p_recipient_session_id": sessB1, "p_body": "x", "p_idempotency_key": k},
		"both", "A: send to FOREIGN recipient")
	rU := rpc(ctx, env, pub, sA.AccessToken, "send_message", map[string]any{
		"p_sender_session_id": sessA1, "p_recipient_session_id": unknown, "p_body": "x", "p_idempotency_key": k},
		"both", "A: send to UNKNOWN recipient")
	byteIdent("a3-send-recipient", "foreign", rF, "unknown", rU)

	rF = rpc(ctx, env, pub, sA.AccessToken, "send_message", map[string]any{
		"p_sender_session_id": sessB1, "p_recipient_session_id": sessA2, "p_body": "x", "p_idempotency_key": k},
		"both", "A: send from FOREIGN sender session")
	rU = rpc(ctx, env, pub, sA.AccessToken, "send_message", map[string]any{
		"p_sender_session_id": unknown, "p_recipient_session_id": sessA2, "p_body": "x", "p_idempotency_key": k},
		"both", "A: send from UNKNOWN sender session")
	byteIdent("a3-send-sender", "foreign", rF, "unknown", rU)

	rF = rpc(ctx, env, pub, sA.AccessToken, "session_heartbeat", map[string]any{"p_session_id": sessB1}, "both", "A: heartbeat FOREIGN session")
	rU = rpc(ctx, env, pub, sA.AccessToken, "session_heartbeat", map[string]any{"p_session_id": unknown}, "both", "A: heartbeat UNKNOWN session")
	byteIdent("a3-heartbeat", "foreign", rF, "unknown", rU)

	rF = rpc(ctx, env, pub, sA.AccessToken, "close_session", map[string]any{"p_session_id": sessB1}, "both", "A: close FOREIGN session")
	rU = rpc(ctx, env, pub, sA.AccessToken, "close_session", map[string]any{"p_session_id": unknown}, "both", "A: close UNKNOWN session")
	byteIdent("a3-close", "foreign", rF, "unknown", rU)

	rF = rpc(ctx, env, pub, sA.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sessB1}, "both", "A: fetch_inbox FOREIGN session")
	rU = rpc(ctx, env, pub, sA.AccessToken, "fetch_inbox", map[string]any{"p_session_id": unknown}, "both", "A: fetch_inbox UNKNOWN session")
	byteIdent("a3-fetch_inbox", "foreign", rF, "unknown", rU)

	dummyMsg := randUUID()
	rF = rpc(ctx, env, pub, sA.AccessToken, "ack_messages", map[string]any{"p_session_id": sessB1, "p_message_ids": []string{dummyMsg}}, "both", "A: ack on FOREIGN session")
	rU = rpc(ctx, env, pub, sA.AccessToken, "ack_messages", map[string]any{"p_session_id": unknown, "p_message_ids": []string{dummyMsg}}, "both", "A: ack on UNKNOWN session")
	byteIdent("a3-ack", "foreign", rF, "unknown", rU)

	rF = rpc(ctx, env, pub, sA.AccessToken, "register_session", map[string]any{
		"p_team_id": teamA, "p_name": "resume-try", "p_resume_session_id": sessB1}, "both", "A: resume FOREIGN session id")
	rU = rpc(ctx, env, pub, sA.AccessToken, "register_session", map[string]any{
		"p_team_id": teamA, "p_name": "resume-try", "p_resume_session_id": unknown}, "both", "A: resume UNKNOWN session id")
	byteIdent("a3-resume", "foreign", rF, "unknown", rU)

	// ---------------- a6: join_team uniform invalid_secret ----------------
	section("a6: join_team — unknown team vs known team with wrong secret, byte-for-byte")

	wrongUnknown := "brg1." + randUUID() + "." + randHex(16)
	wrongKnown := "brg1." + teamB + "." + randHex(16)
	rF = rpc(ctx, env, pub, sC.AccessToken, "join_team", map[string]any{"p_join_secret": wrongUnknown}, "both", "C: join_team UNKNOWN team, valid-format secret")
	rU = rpc(ctx, env, pub, sC.AccessToken, "join_team", map[string]any{"p_join_secret": wrongKnown}, "both", "C: join_team KNOWN team B, wrong secret")
	stF, _ := jsonGet[string](rF.Body, "status")
	stU, _ := jsonGet[string](rU.Body, "status")
	same := byteIdent("a6-join-uniform", "unknown-team", rF, "known-team-wrong-secret", rU)
	check("a6-join-invalid_secret", same && rF.Status == 200 && stF == "invalid_secret" && stU == "invalid_secret",
		fmt.Sprintf("both HTTP 200 with status=%q/%q (want invalid_secret twice, no other error, no dummy-hash breakage)", stF, stU))

	// ---------------- a8: list_members uniform unauthorized for non-members ----------------
	section("a8: list_members — non-member asking about a real foreign team vs a random uuid, byte-for-byte")

	rF = rpc(ctx, env, pub, sC.AccessToken, "list_members", map[string]any{"p_team_id": teamA}, "both", "C: list_members REAL foreign team A")
	rU = rpc(ctx, env, pub, sC.AccessToken, "list_members", map[string]any{"p_team_id": randUUID()}, "both", "C: list_members RANDOM uuid")
	same = byteIdent("a8-list_members-uniform", "real-foreign-team", rF, "random-uuid", rU)
	codeF, msgF := pgCodeMsg(rF)
	check("a8-list_members-unauthorized", same && rF.Status == 403 && codeF == "42501" && msgF == "brigade:unauthorized",
		fmt.Sprintf("HTTP %d code=%s msg=%q for both (no team-existence oracle)", rF.Status, codeF, msgF))

	// ---------------- a7: revocation is immediate for selects and inbox ----------------
	section("a7: revoked membership loses direct selects AND fetch_inbox at once")

	r = rpc(ctx, env, pub, sA2.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sessA2s}, "both", "A2: fetch_inbox BEFORE revocation")
	rs = rows(r.Body)
	check("a7-pre-inbox", r.Status == 200 && colHas(rs, "message_id", msgToA2),
		fmt.Sprintf("before revocation: HTTP %d, inbox has %d message(s), contains msgToA2=%v", r.Status, len(rs), colHas(rs, "message_id", msgToA2)))

	// Direct-select baseline while still active: the post-revocation "0 rows" checks are only
	// meaningful if A2 demonstrably saw these rows moments earlier.
	preSel := []struct{ q, name, col, want string }{
		{"teams?" + teamCols, "teams", "id", teamA},
		{"memberships?select=team_id,user_id,status", "memberships", "user_id", uidA2},
		{"sessions?select=id,team_id", "sessions", "id", sessA2s},
		{"messages?select=id,team_id", "messages", "id", msgToA2},
	}
	for _, p := range preSel {
		r = restGet(ctx, env, pub, sA2.AccessToken, p.q, "A2 (still active): select "+p.name)
		rs = rows(r.Body)
		check("a7-pre-select-"+p.name, r.Status == 200 && len(rs) > 0 && colHas(rs, p.col, p.want),
			fmt.Sprintf("before revocation, %s returns HTTP %d with %d row(s), contains expected %s=%v", p.name, r.Status, len(rs), p.want, colHas(rs, p.col, p.want)))
	}

	r = rpc(ctx, env, pub, sA2.AccessToken, "leave_team", map[string]any{"p_team_id": teamA}, "both", "A2: leave_team (membership -> revoked)")
	left, _ := jsonGet[bool](r.Body, "left")
	check("a7-leave", r.Status == 200 && left, fmt.Sprintf("leave_team: HTTP %d body=%s", r.Status, string(r.Body)))

	for _, t := range []string{"teams?" + teamCols, "memberships?select=*", "sessions?select=*", "messages?select=*"} {
		name := strings.SplitN(t, "?", 2)[0]
		r = restGet(ctx, env, pub, sA2.AccessToken, t, "A2 (revoked): select "+name)
		rs = rows(r.Body)
		check("a7-revoked-select-"+name, r.Status == 200 && len(rs) == 0,
			fmt.Sprintf("immediately after revocation, %s returns HTTP %d with %d row(s): %s", name, r.Status, len(rs), string(r.Body)))
	}

	r = rpc(ctx, env, pub, sA2.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sessA2s}, "both", "A2 (revoked): fetch_inbox")
	code, msg = pgCodeMsg(r)
	check("a7-revoked-inbox", r.Status == 403 && code == "42501" && msg == "brigade:unauthorized",
		fmt.Sprintf("fetch_inbox after revocation: HTTP %d code=%s msg=%q", r.Status, code, msg))

	// ---------------- enumerations: every brigade object ----------------
	tables := []string{"teams", "memberships", "sessions", "messages", "join_attempts"}
	ruuid := randUUID()
	rpcCases := []struct {
		fn   string
		args map[string]any
	}{
		{"my_team_ids", map[string]any{}},
		{"create_team", map[string]any{"p_name": "x"}},
		{"join_team", map[string]any{"p_join_secret": "x"}},
		{"leave_team", map[string]any{"p_team_id": ruuid}},
		{"register_session", map[string]any{"p_team_id": ruuid, "p_name": "x"}},
		{"session_heartbeat", map[string]any{"p_session_id": ruuid}},
		{"close_session", map[string]any{"p_session_id": ruuid}},
		{"list_sessions", map[string]any{"p_team_id": ruuid}},
		{"list_members", map[string]any{"p_team_id": ruuid}},
		{"send_message", map[string]any{"p_sender_session_id": ruuid, "p_recipient_session_id": ruuid, "p_body": "x", "p_idempotency_key": "k"}},
		{"fetch_inbox", map[string]any{"p_session_id": ruuid}},
		{"ack_messages", map[string]any{"p_session_id": ruuid, "p_message_ids": []string{}}},
		{"owns_session_topic", map[string]any{"p_topic": "x"}},
		{"gc_expired", map[string]any{}},
	}

	// a4: publishable key alone (no user JWT) -> 42501 on every table and RPC.
	section("a4: publishable key only (no user JWT) — 42501 on every brigade.* object")

	allOK := true
	var lines []string
	for _, t := range tables {
		r = restGetQuiet(ctx, env, pub, "", t+"?select=*")
		c, m := pgCodeMsg(r)
		ok := c == "42501" && strings.Contains(m, "permission denied")
		allOK = allOK && ok
		line := fmt.Sprintf("table %-13s HTTP %d code=%s msg=%q", t+":", r.Status, c, m)
		lines = append(lines, line)
		fmt.Println("  " + line)
	}
	check("a4-tables-anon", allOK, "publishable key, no JWT, direct select on all 5 tables: "+strings.Join(lines, " | "))

	allOK = true
	lines = nil
	for _, rc := range rpcCases {
		r = rpcBare(ctx, env, pub, rc.fn, rc.args)
		c, m := pgCodeMsg(r)
		ok := c == "42501" && strings.Contains(m, "permission denied")
		allOK = allOK && ok
		line := fmt.Sprintf("rpc %-20s HTTP %d code=%s msg=%q", rc.fn+":", r.Status, c, m)
		lines = append(lines, line)
		fmt.Println("  " + line)
	}
	check("a4-rpcs-anon", allOK, "publishable key, no JWT, all 14 brigade RPCs: "+strings.Join(lines, " | "))

	// a5: service principal -> 42501 on every table.
	section("a5: secret key / service_role — 42501 on every brigade table")

	for _, withJWT := range []bool{false, true} {
		variant := "sb_secret key alone"
		tag := "secret-key"
		if withJWT {
			variant = "sb_secret key + legacy service_role JWT"
			tag = "service-role-jwt"
		}
		allOK = true
		lines = nil
		for _, t := range tables {
			r = svcGet(ctx, env, withJWT, t+"?select=*")
			c, m := pgCodeMsg(r)
			ok := c == "42501"
			allOK = allOK && ok
			line := fmt.Sprintf("table %-13s HTTP %d code=%s msg=%q", t+":", r.Status, c, m)
			lines = append(lines, line)
			fmt.Println("  [" + tag + "] " + line)
		}
		check("a5-tables-"+tag, allOK, variant+", direct select on all 5 tables: "+strings.Join(lines, " | "))
	}

	rw := restWrite(ctx, env, env.SecretKey, env.ServiceRoleJWT, "POST", "messages",
		map[string]any{"team_id": teamA, "sender_user_id": uidA, "sender_session_id": sessA1,
			"recipient_session_id": sessA2, "body": "svc", "body_hash": "\\x00", "idempotency_key": "svc-" + randHex(4)},
		"service_role: direct INSERT into messages")
	code, msg = pgCodeMsg(rw)
	check("a5-insert-service-role", rw.Status == 403 && code == "42501",
		fmt.Sprintf("service_role direct INSERT messages: HTTP %d code=%s msg=%q", rw.Status, code, msg))

	// (i): service principal gets `permission denied for function` on EVERY RPC.
	section("check (i): secret key / service_role — 42501 permission denied for FUNCTION on every RPC (never reaches the body)")

	// The 14 caller-facing RPCs plus owned_active_session, the internal helper that is still
	// addressable at /rest/v1/rpc/ (its composite-arg siblings are not). Each must stop at the
	// EXECUTE check: HTTP 403, SQLSTATE 42501, message naming exactly this function — never a
	// brigade:*/28000 raise, which would mean the body ran.
	svcCases := append(rpcCases, struct {
		fn   string
		args map[string]any
	}{"owned_active_session", map[string]any{"p_session_id": ruuid, "p_uid": ruuid}})
	for _, rc := range svcCases {
		rNoJWT := rpcSvc(ctx, env, false, rc.fn, rc.args)
		rJWT := rpcSvc(ctx, env, true, rc.fn, rc.args)
		c1, m1 := pgCodeMsg(rNoJWT)
		c2, m2 := pgCodeMsg(rJWT)
		want := func(st int, c, m string) bool {
			return st == 403 && c == "42501" && m == "permission denied for function "+rc.fn
		}
		notBody := !strings.Contains(m1, "brigade:") && !strings.Contains(m2, "brigade:") // 28000/brigade:* would mean the body ran
		fmt.Printf("  %-22s [secret-key]      HTTP %d SQLSTATE=%s msg=%q\n", rc.fn+":", rNoJWT.Status, c1, m1)
		fmt.Printf("  %-22s [service-role-jwt] HTTP %d SQLSTATE=%s msg=%q\n", "", rJWT.Status, c2, m2)
		check("i-"+rc.fn, want(rNoJWT.Status, c1, m1) && want(rJWT.Status, c2, m2) && notBody,
			fmt.Sprintf("secret key: HTTP %d SQLSTATE=%s msg=%q; service_role JWT: HTTP %d SQLSTATE=%s msg=%q (function body never entered)",
				rNoJWT.Status, c1, m1, rJWT.Status, c2, m2))
	}

	// Supplement: the server-only functions must be denied to AUTHENTICATED members too — the
	// explicit revokes, not a service_role quirk, are what fence them off.
	section("supplement: server-only functions denied to an authenticated member as well")
	for _, sc := range []struct {
		fn   string
		args map[string]any
	}{
		{"gc_expired", map[string]any{}},
		{"owned_active_session", map[string]any{"p_session_id": sessA1, "p_uid": uidA}},
	} {
		r = rpc(ctx, env, pub, sA.AccessToken, sc.fn, sc.args, "both", "A (authenticated member): rpc "+sc.fn)
		c, m := pgCodeMsg(r)
		check("i-authenticated-"+sc.fn, r.Status == 403 && c == "42501" && m == "permission denied for function "+sc.fn,
			fmt.Sprintf("authenticated member calling %s: HTTP %d SQLSTATE=%s msg=%q (server-only, body never entered)", sc.fn, r.Status, c, m))
	}

	// ---------------- summary ----------------
	section("summary")
	failed := 0
	for _, rr := range results {
		if !rr.Pass {
			failed++
			fmt.Printf("FAILED: %s — %s\n", rr.ID, redact(rr.Evidence))
		}
	}
	fmt.Printf("%d assertions, %d failed\n", len(results), failed)
	if failed > 0 {
		os.Exit(1)
	}
}
