package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: brigadetest <auth|refresh|logout|rpc|realtime|expiry|all> [env-file]")
	}
	envPath := "/private/tmp/claude-501/-Users-rjae-Development-appshapes-brigade/beee3690-4aa8-485b-9342-e1bb09c28aca/scratchpad/wf2/sb/.env.local"
	if len(os.Args) > 2 {
		envPath = os.Args[2]
	}
	env := loadEnv(envPath)
	ctx := context.Background()
	fmt.Printf("# run %s at %s\n", os.Args[1], time.Now().UTC().Format(time.RFC3339))
	switch os.Args[1] {
	case "auth":
		expAuth(ctx, env)
	case "refresh":
		expRefresh(ctx, env)
	case "logout":
		expLogout(ctx, env)
	case "rpc":
		expRPC(ctx, env)
	case "realtime":
		expRealtime(ctx, env)
	case "expiry":
		expExpiry(ctx, env)
	case "refresh2":
		expRefresh2(ctx, env)
	case "reauth":
		expReauth(ctx, env)
	case "expjoin":
		expExpiredJoin(ctx, env)
	case "refresh3":
		expRefresh3(ctx, env)
	default:
		fatal("unknown experiment %q", os.Args[1])
	}
}

func newPrincipal(ctx context.Context, env Env, label string) Session {
	s, r := signUpAnonymous(ctx, env, env.PublishableKey, label, nil, nil)
	if r.Status != 200 {
		fatal("sign-up failed: %d %s", r.Status, r.Body)
	}
	return s
}

func newPrincipalQuiet(ctx context.Context, env Env) Session {
	r := doQuiet(ctx, "POST", env.APIURL+"/auth/v1/signup", authHeaders(env, env.PublishableKey),
		map[string]any{"data": map[string]any{}, "gotrue_meta_security": map[string]any{}})
	if r.Status != 200 {
		fatal("quiet sign-up failed: %d %s", r.Status, r.Body)
	}
	var s Session
	_ = json.Unmarshal(r.Body, &s)
	return s
}

func registerSession(ctx context.Context, env Env, s Session, name string) string {
	r := rpc(ctx, env, env.PublishableKey, s.AccessToken, "register_session", map[string]any{"p_name": name}, "both", "register_session "+name)
	if r.Status != 200 {
		fatal("register_session failed: %d %s", r.Status, r.Body)
	}
	id, _ := jsonGet[string](r.Body, "session_id")
	return id
}

// ---------------------------------------------------------------- (a) anonymous sign-up
func expAuth(ctx context.Context, env Env) {
	section("(a) GoTrue anonymous sign-up")
	s, r := signUpAnonymous(ctx, env, env.PublishableKey, "signup: publishable key, auth-js body", nil, nil)
	if r.Status == 200 {
		printClaims("access_token", s.AccessToken)
		fmt.Printf("session fields: token_type=%s expires_in=%d expires_at=%d refresh_token_len=%d user.id=%s user.aud=%s user.role=%s user.is_anonymous=%v\n",
			s.TokenType, s.ExpiresIn, s.ExpiresAt, len(s.RefreshToken), s.User.ID, s.User.Aud, s.User.Role, s.User.IsAnonymous)
	}
	signUpAnonymous(ctx, env, env.AnonJWT, "signup: legacy anon JWT as apikey", nil, nil)
	signUpAnonymous(ctx, env, env.PublishableKey, "signup: empty JSON object body {}", nil, map[string]any{})
	signUpAnonymous(ctx, env, env.PublishableKey, "signup: no body at all", nil, []byte{})
	signUpAnonymous(ctx, env, env.PublishableKey, "signup: with X-Supabase-Api-Version: 2024-01-01 (auth-js default)", map[string]string{"X-Supabase-Api-Version": "2024-01-01"}, nil)
	signUpAnonymous(ctx, env, env.PublishableKey, "signup: with Authorization: Bearer <publishable key> too (supabase-js default headers)", map[string]string{"Authorization": "Bearer " + env.PublishableKey}, nil)
	do(ctx, "signup: NO apikey header", "POST", env.APIURL+"/auth/v1/signup", map[string]string{"Content-Type": "application/json"}, map[string]any{})
	do(ctx, "signup: bogus apikey", "POST", env.APIURL+"/auth/v1/signup", map[string]string{"Content-Type": "application/json", "apikey": "sb_publishable_bogus"}, map[string]any{})
	do(ctx, "signup: secret key as apikey (must not be used by the adapter; recorded only)", "POST", env.APIURL+"/auth/v1/signup", map[string]string{"Content-Type": "application/json", "apikey": env.SecretKey}, map[string]any{})
	do(ctx, "GET /auth/v1/user with the anonymous access token", "GET", env.APIURL+"/auth/v1/user", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + s.AccessToken}, nil)
	do(ctx, "GET /auth/v1/settings (public; shows external.anonymous_users)", "GET", env.APIURL+"/auth/v1/settings", map[string]string{"apikey": env.PublishableKey}, nil)
	do(ctx, "GET /auth/v1/health", "GET", env.APIURL+"/auth/v1/health", map[string]string{"apikey": env.PublishableKey}, nil)
}

// ---------------------------------------------------------------- (b) refresh, rotation, race, reuse
func expRefresh(ctx context.Context, env Env) {
	section("(b) refresh: rotation")
	s0 := newPrincipal(ctx, env, "signup for refresh tests")
	t0 := s0.RefreshToken
	s1, r1 := refresh(ctx, env, env.PublishableKey, t0, "refresh #1 with T0 -> expect T1")
	if r1.Status != 200 {
		fatal("refresh failed")
	}
	fmt.Printf("rotation: T0=%s T1=%s same_user=%v access_token_changed=%v expires_in=%d\n", t0, s1.RefreshToken, s1.User.ID == s0.User.ID, s1.AccessToken != s0.AccessToken, s1.ExpiresIn)
	printClaims("T1 access_token", s1.AccessToken)

	section("(b) refresh: two concurrent refreshes with the same token T1 (race)")
	var wg sync.WaitGroup
	results := make([]Resp, 2)
	sessions := make([]Session, 2)
	starts := make([]time.Time, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			starts[i] = time.Now()
			sessions[i], results[i] = refreshQuiet(ctx, env, env.PublishableKey, s1.RefreshToken)
		}(i)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		fmt.Printf("racer %d: started+%dms status=%d elapsed=%dms body=%s\n", i, starts[i].Sub(starts[0]).Milliseconds(), results[i].Status, results[i].Elapsed.Milliseconds(), redact(string(results[i].Body)))
	}
	if results[0].Status == 200 && results[1].Status == 200 {
		fmt.Printf("race: both 200; refresh tokens equal=%v (%s vs %s); access tokens equal=%v\n", sessions[0].RefreshToken == sessions[1].RefreshToken, sessions[0].RefreshToken, sessions[1].RefreshToken, sessions[0].AccessToken == sessions[1].AccessToken)
	}
	var s2 Session
	for i := 0; i < 2; i++ {
		if results[i].Status == 200 {
			s2 = sessions[i]
		}
	}
	if s2.RefreshToken == "" {
		fatal("race produced no session")
	}

	section("(b) refresh: reuse rules (reuse interval 10 s; one-behind rule; two-behind revokes)")
	_, _ = refresh(ctx, env, env.PublishableKey, s1.RefreshToken, "reuse T1 again immediately (<10 s after its use) -> expect allowed, returns active token")
	fmt.Println("sleeping 11 s to leave the reuse interval …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, s1.RefreshToken, "reuse T1 after 11 s: T1 is one-behind the active token T2 -> expect allowed (fail-to-save rule)")
	s3, r3 := refresh(ctx, env, env.PublishableKey, s2.RefreshToken, "refresh with T2 (active) -> T3")
	if r3.Status != 200 {
		fatal("refresh T2 failed")
	}
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, t0, "reuse T0 after >11 s: two or more behind -> expect 400 refresh_token_already_used and family revoked")
	_, _ = refresh(ctx, env, env.PublishableKey, s3.RefreshToken, "immediately use the active token T3 -> auth digest observed a 10 s grace after revocation")
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, s3.RefreshToken, "use T3 again after 11 s -> expect terminal refresh_token_already_used")
	rpc(ctx, env, env.PublishableKey, s3.AccessToken, "whoami", nil, "both", "the revoked family's last access token still works on PostgREST until exp (stateless JWT)")

	section("(b) refresh: error shapes for garbage / unknown tokens")
	_, _ = refresh(ctx, env, env.PublishableKey, "nonexistent00", "refresh with an unknown token")
	_, _ = refresh(ctx, env, env.PublishableKey, "", "refresh with an empty token")
	do(ctx, "refresh with no apikey", "POST", env.APIURL+"/auth/v1/token?grant_type=refresh_token", map[string]string{"Content-Type": "application/json"}, map[string]string{"refresh_token": t0})
	do(ctx, "refresh with an unsupported grant_type", "POST", env.APIURL+"/auth/v1/token?grant_type=nope", authHeaders(env, env.PublishableKey), map[string]string{"refresh_token": t0})
}

// ---------------------------------------------------------------- (c) global sign-out
func expLogout(ctx context.Context, env Env) {
	section("(c) global sign-out")
	s0 := newPrincipal(ctx, env, "signup for logout tests")
	s1, _ := refresh(ctx, env, env.PublishableKey, s0.RefreshToken, "refresh once so the chain has two tokens")
	logout(ctx, env, env.PublishableKey, s1.AccessToken, "global", "POST /auth/v1/logout?scope=global with the current access token")
	_, _ = refresh(ctx, env, env.PublishableKey, s1.RefreshToken, "refresh with the current refresh token after global sign-out")
	_, _ = refresh(ctx, env, env.PublishableKey, s0.RefreshToken, "refresh with the previous (one-behind) refresh token after global sign-out")
	rpc(ctx, env, env.PublishableKey, s1.AccessToken, "whoami", nil, "both", "PostgREST with the signed-out access token (stateless JWT, still inside exp)")
	do(ctx, "GET /auth/v1/user with the signed-out access token", "GET", env.APIURL+"/auth/v1/user", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + s1.AccessToken}, nil)
	logout(ctx, env, env.PublishableKey, s1.AccessToken, "global", "logout again with the same (already signed-out) access token")
	logout(ctx, env, env.PublishableKey, "not-a-jwt", "global", "logout with a garbage bearer")
	do(ctx, "logout with no Authorization header", "POST", env.APIURL+"/auth/v1/logout?scope=global", authHeaders(env, env.PublishableKey), nil)
	s2 := newPrincipal(ctx, env, "signup for scope=local/others comparison")
	logout(ctx, env, env.PublishableKey, s2.AccessToken, "others", "scope=others (should keep the current session)")
	_, _ = refresh(ctx, env, env.PublishableKey, s2.RefreshToken, "refresh after scope=others -> expect still valid")
	s3 := newPrincipal(ctx, env, "signup for scope=local")
	logout(ctx, env, env.PublishableKey, s3.AccessToken, "local", "scope=local")
	_, _ = refresh(ctx, env, env.PublishableKey, s3.RefreshToken, "refresh after scope=local -> expect revoked")
	logout(ctx, env, env.PublishableKey, s2.AccessToken, "", "no scope parameter (server default)")
	_, _ = refresh(ctx, env, env.PublishableKey, s2.RefreshToken, "refresh after logout with no scope")
}

// ---------------------------------------------------------------- (d) PostgREST RPC and error shapes
func expRPC(ctx context.Context, env Env) {
	section("(d) PostgREST: schema headers and success shape")
	a := newPrincipal(ctx, env, "signup A")
	b := newPrincipalQuiet(ctx, env)
	do(ctx, "GET /rest/v1/ (OpenAPI root: PostgREST version)", "GET", env.APIURL+"/rest/v1/", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + a.AccessToken, "Accept-Profile": "brigade"}, nil)
	rA := rpc(ctx, env, env.PublishableKey, a.AccessToken, "register_session", map[string]any{"p_name": "alpha"}, "both", "register_session with Accept-Profile + Content-Profile: brigade")
	sidA, _ := jsonGet[string](rA.Body, "session_id")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "whoami", nil, "content", "whoami with Content-Profile only")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "whoami", nil, "accept", "whoami with Accept-Profile only")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "whoami", nil, "none", "whoami with neither header (defaults to public)")
	rB := rpc(ctx, env, env.PublishableKey, b.AccessToken, "register_session", map[string]any{"p_name": "bravo"}, "both", "register_session for B")
	sidB, _ := jsonGet[string](rB.Body, "session_id")
	rpc(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "hello alpha"}, "both", "send_message B -> A (success shape)")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sidA}, "both", "fetch_inbox A")

	section("(d) PostgREST: error JSON for RAISE with errcode P0002 'brigade:not_found'")
	rpc(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": "00000000-0000-0000-0000-000000000000", "p_body": "to nobody"}, "both", "send_message to an unknown recipient -> RAISE P0002")
	rpc(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidA, "p_recipient_session_id": sidB, "p_body": "forged sender"}, "both", "send_message with a sender session B does not own -> RAISE P0002 (byte-identical?)")
	rpc(ctx, env, env.PublishableKey, b.AccessToken, "fetch_inbox", map[string]any{"p_session_id": sidA}, "both", "fetch_inbox on a foreign session -> RAISE P0002")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_with_detail", nil, "both", "RAISE P0002 with DETAIL and HINT set")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_unauthorized", nil, "both", "RAISE 42501 'brigade:unauthorized' (with a JWT)")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_conflict", nil, "both", "RAISE P0001 'brigade:conflict:session_live'")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_rate_limited", nil, "both", "RAISE P0001 'brigade:rate_limited:send_per_minute:60'")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_invalid", nil, "both", "RAISE 22023 'brigade:invalid_input:body'")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "raise_unauthenticated", nil, "both", "RAISE 28000 'brigade:unauthenticated' (with a JWT)")

	section("(d) PostgREST: missing / bad JWT and real 42501")
	rpc(ctx, env, env.PublishableKey, "", "whoami", nil, "both", "whoami with apikey only (no Authorization) -> role anon, uid null")
	rpc(ctx, env, env.PublishableKey, "", "authenticated_only", nil, "both", "authenticated_only with apikey only -> real 42501 as anon")
	rpc(ctx, env, env.PublishableKey, env.AnonJWT, "authenticated_only", nil, "both", "authenticated_only with Authorization: Bearer <legacy anon JWT> -> real 42501 with a JWT present")
	rpc(ctx, env, env.PublishableKey, "", "register_session", map[string]any{"p_name": "x"}, "both", "register_session with apikey only (execute revoked from anon)")
	rpc(ctx, env, env.PublishableKey, env.PublishableKey, "whoami", nil, "both", "Authorization: Bearer <publishable key> (not a JWT)")
	rpc(ctx, env, env.PublishableKey, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4In0.badsignaturebadsignaturebadsignature", "whoami", nil, "both", "Authorization: Bearer <JWT with a bad signature>")
	expired := mintJWT(env.JWTSecret, map[string]any{"aud": "authenticated", "role": "authenticated", "sub": a.User.ID, "exp": time.Now().Unix() - 100, "iat": time.Now().Unix() - 200, "is_anonymous": true})
	rpc(ctx, env, env.PublishableKey, expired, "whoami", nil, "both", "Authorization: Bearer <correctly signed but expired JWT>")
	do(ctx, "rpc with NO apikey header at all", "POST", env.APIURL+"/rest/v1/rpc/whoami", map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + a.AccessToken, "Content-Profile": "brigade"}, map[string]any{})
	do(ctx, "rpc with apikey only as query parameter ?apikey=", "POST", env.APIURL+"/rest/v1/rpc/whoami?apikey="+env.PublishableKey, map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + a.AccessToken, "Content-Profile": "brigade"}, map[string]any{})
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "does_not_exist", nil, "both", "unknown function")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "register_session", map[string]any{"wrong_arg": 1}, "both", "known function, wrong argument names")
	rpc(ctx, env, env.PublishableKey, a.AccessToken, "register_session", map[string]any{"p_name": ""}, "both", "invalid_input (22023) from a real RPC")
	rpc(ctx, env, env.SecretKey, "", "whoami", nil, "both", "secret key as apikey with no Authorization (recorded only; the adapter never holds it)")
	rpc(ctx, env, env.PublishableKey, env.ServiceRoleJWT, "register_session", map[string]any{"p_name": "svc"}, "both", "service_role JWT calling register_session (no grant; expect 42501)")

	section("(d) PostgREST: direct table access under RLS")
	do(ctx, "GET /rest/v1/sessions?select=* as A (Accept-Profile: brigade)", "GET", env.APIURL+"/rest/v1/sessions?select=*", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + a.AccessToken, "Accept-Profile": "brigade"}, nil)
	do(ctx, "GET /rest/v1/messages?select=* as A", "GET", env.APIURL+"/rest/v1/messages?select=*", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + a.AccessToken, "Accept-Profile": "brigade"}, nil)
	do(ctx, "POST /rest/v1/messages (direct insert, no grant) as A", "POST", env.APIURL+"/rest/v1/messages", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + a.AccessToken, "Content-Profile": "brigade", "Content-Type": "application/json", "Prefer": "return=representation"}, map[string]any{"sender_user_id": a.User.ID, "sender_session_id": sidA, "recipient_session_id": sidB, "body": "direct"})
	do(ctx, "GET /rest/v1/sessions with apikey only", "GET", env.APIURL+"/rest/v1/sessions?select=*", map[string]string{"apikey": env.PublishableKey, "Accept-Profile": "brigade"}, nil)
	do(ctx, "GET /rest/v1/sessions with the secret key as apikey and no JWT (service_role has no table grant)", "GET", env.APIURL+"/rest/v1/sessions?select=*", map[string]string{"apikey": env.SecretKey, "Accept-Profile": "brigade"}, nil)
	do(ctx, "GET /rest/v1/nope (unknown table) as A", "GET", env.APIURL+"/rest/v1/nope?select=*", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + a.AccessToken, "Accept-Profile": "brigade"}, nil)
}

// ---------------------------------------------------------------- (e) Realtime
func expRealtime(ctx context.Context, env Env) {
	section("(e) Realtime: setup two principals and sessions")
	a := newPrincipalQuiet(ctx, env)
	b := newPrincipalQuiet(ctx, env)
	sidA := registerSession(ctx, env, a, "rt-alpha")
	sidB := registerSession(ctx, env, b, "rt-bravo")
	topicA := "realtime:brigade:session:" + sidA
	topicB := "realtime:brigade:session:" + sidB

	section("(e1) vsn=2.0.0: dial with the publishable key, heartbeat, private join of own topic")
	p, err := dial(ctx, env, env.PublishableKey, "2.0.0", "A-v2")
	if err != nil {
		fatal("dial: %v", err)
	}
	if m, ok := p.heartbeat(ctx); ok {
		fmt.Printf("heartbeat reply: %s\n", m.String())
	} else {
		fmt.Println("heartbeat: NO reply within 5 s")
	}
	joinRefA, rep, ok := p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("join own private topic: ok=%v reply=%s\n", ok, rep.String())
	// any system messages right after the join?
	if m, ok := p.waitFor(1500*time.Millisecond, func(m Msg) bool { return m.Event != "phx_reply" }); ok {
		fmt.Printf("post-join message: %s\n", m.String())
	} else {
		fmt.Println("post-join: no further message within 1.5 s")
	}

	section("(e2) broadcast from the database trigger: 10 inserts, latency insert -> event")
	var lat []time.Duration
	var latFromResp []time.Duration
	for i := 0; i < 10; i++ {
		t0 := time.Now()
		r := rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": fmt.Sprintf("msg %d", i)})
		tResp := time.Now()
		if r.Status != 200 {
			fatal("send_message failed: %d %s", r.Status, r.Body)
		}
		mid, _ := jsonGet[string](r.Body, "message_id")
		m, got := p.waitFor(5*time.Second, func(m Msg) bool {
			if m.Event != "broadcast" {
				return false
			}
			var pl struct {
				Payload struct {
					MessageID string `json:"message_id"`
				} `json:"payload"`
			}
			_ = json.Unmarshal(m.Payload, &pl)
			return pl.Payload.MessageID == mid
		})
		if !got {
			fmt.Printf("insert %d: NO broadcast within 5 s (message_id %s)\n", i, mid)
			continue
		}
		lat = append(lat, m.At.Sub(t0))
		latFromResp = append(latFromResp, m.At.Sub(tResp))
		if i == 0 {
			fmt.Printf("first broadcast frame: binary=%v payload=%s\n", m.Binary, string(m.Payload))
			fmt.Printf("send_message response: %s\n", string(r.Body))
		}
		fmt.Printf("insert %d: rpc %d ms; event %d ms after request start, %d ms after response\n", i, r.Elapsed.Milliseconds(), lat[len(lat)-1].Milliseconds(), latFromResp[len(latFromResp)-1].Milliseconds())
	}
	if len(lat) > 0 {
		var sum time.Duration
		mx := time.Duration(0)
		for _, d := range lat {
			sum += d
			if d > mx {
				mx = d
			}
		}
		fmt.Printf("latency insert->event: n=%d mean=%d ms max=%d ms\n", len(lat), (sum / time.Duration(len(lat))).Milliseconds(), mx.Milliseconds())
	}

	section("(e3) client-side broadcast push on the private topic (no insert policy): with ack requested")
	ref, _ := p.push(ctx, &joinRefA, topicA, "broadcast", map[string]any{"type": "broadcast", "event": "message_accepted", "payload": map[string]any{"message_id": "forged"}})
	if m, ok := p.waitFor(3*time.Second, func(m Msg) bool { return m.Ref != nil && *m.Ref == ref || m.Event == "broadcast" || m.Event == "system" }); ok {
		fmt.Printf("client broadcast result: %s\n", m.String())
	} else {
		fmt.Println("client broadcast: no reply, no echo, no system message within 3 s (ack:false join)")
	}

	section("(e4) access_token push with a refreshed JWT")
	a2, _ := refresh(ctx, env, env.PublishableKey, a.RefreshToken, "refresh A to get a new access token")
	ref, _ = p.push(ctx, &joinRefA, topicA, "access_token", map[string]any{"access_token": a2.AccessToken})
	if m, ok := p.waitFor(3*time.Second, func(m Msg) bool { return (m.Ref != nil && *m.Ref == ref) || m.Event == "system" || m.Event == "phx_close" || m.Event == "phx_error" }); ok {
		fmt.Printf("access_token push result: %s\n", m.String())
	} else {
		fmt.Println("access_token push: no reply/system message within 3 s (fire-and-forget)")
	}
	r := rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "after token push"})
	if m, ok := p.waitFor(5*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("broadcast still delivered after access_token push: %s (rpc %d)\n", m.String(), r.Status)
	} else {
		fmt.Println("NO broadcast after access_token push")
	}

	section("(e5) foreign topic (B's session) and a wrong topic on the same socket")
	_, rep, ok = p.join(ctx, topicB, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("join FOREIGN private topic as A: ok=%v reply=%s\n", ok, rep.String())
	if m, ok := p.waitFor(2*time.Second, func(m Msg) bool { return m.Topic == topicB }); ok {
		fmt.Printf("follow-up on the foreign topic: %s\n", m.String())
	}
	wrong := "realtime:brigade:session:00000000-0000-0000-0000-000000000000"
	_, rep, ok = p.join(ctx, wrong, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("join WRONG private topic as A: ok=%v reply=%s\n", ok, rep.String())
	if m, ok := p.waitFor(2*time.Second, func(m Msg) bool { return m.Topic == wrong }); ok {
		fmt.Printf("follow-up on the wrong topic: %s\n", m.String())
	}
	_, rep, ok = p.join(ctx, "brigade:session:"+sidA, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("join topic WITHOUT the realtime: prefix: ok=%v reply=%s\n", ok, rep.String())
	_, rep, ok = p.join(ctx, "realtime:brigade:session:"+sidA+"-second", JoinConfig{Private: true}, 10*time.Second)
	fmt.Printf("join a private topic with NO access_token in the join payload (apikey only): ok=%v reply=%s\n", ok, rep.String())
	fmt.Println("is the original channel still joined after those refusals? sending one more message …")
	rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "still there?"})
	if m, ok := p.waitFor(5*time.Second, func(m Msg) bool { return m.Event == "broadcast" && m.Topic == topicA }); ok {
		fmt.Printf("yes, still delivered: %s\n", m.String())
	} else {
		fmt.Println("NO broadcast on the original channel")
	}

	section("(e6) phx_leave -> phx_reply and phx_close")
	ref, _ = p.push(ctx, &joinRefA, topicA, "phx_leave", map[string]any{})
	if m, ok := p.waitReply(ref, 3*time.Second); ok {
		fmt.Printf("leave reply: %s\n", m.String())
	}
	if m, ok := p.waitFor(3*time.Second, func(m Msg) bool { return m.Event == "phx_close" }); ok {
		fmt.Printf("phx_close: %s\n", m.String())
	} else {
		fmt.Println("no phx_close within 3 s")
	}
	fmt.Println("rejoin the same topic on the same socket after leaving:")
	joinRefA, rep, ok = p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 10*time.Second)
	fmt.Printf("rejoin: ok=%v reply=%s\n", ok, rep.String())
	fmt.Println("join the SAME topic a second time on the same socket (duplicate join):")
	_, rep, ok = p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 10*time.Second)
	fmt.Printf("duplicate join: ok=%v reply=%s\n", ok, rep.String())
	if m, ok := p.waitFor(2*time.Second, func(m Msg) bool { return m.Event == "phx_close" || m.Event == "phx_error" }); ok {
		fmt.Printf("after duplicate join: %s\n", m.String())
	}
	p.close()
	select {
	case <-p.closed:
	case <-time.After(2 * time.Second):
	}

	section("(e7) PUBLIC join (private:false) of the same topic on a new socket: private broadcasts must not arrive")
	pub, err := dial(ctx, env, env.PublishableKey, "2.0.0", "A-public")
	if err != nil {
		fatal("dial: %v", err)
	}
	_, rep, ok = pub.join(ctx, topicA, JoinConfig{Private: false, AccessToken: a2.AccessToken}, 10*time.Second)
	fmt.Printf("public join: ok=%v reply=%s\n", ok, rep.String())
	for i := 0; i < 3; i++ {
		rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": fmt.Sprintf("public? %d", i)})
	}
	if m, ok := pub.waitFor(4*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("UNEXPECTED: public channel received a private broadcast: %s\n", m.String())
	} else {
		fmt.Println("public channel received no broadcast within 4 s of 3 inserts (expected: private -> private only)")
	}
	pub.close()

	section("(e8) vsn=1.0.0 (JSON object frames): heartbeat, join, one broadcast")
	p1, err := dial(ctx, env, env.PublishableKey, "1.0.0", "A-v1")
	if err != nil {
		fatal("dial v1: %v", err)
	}
	p1.heartbeat(ctx)
	_, rep, ok = p1.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 10*time.Second)
	fmt.Printf("v1 join: ok=%v reply=%s\n", ok, rep.String())
	rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "v1 frame"})
	if m, ok := p1.waitFor(5*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("v1 broadcast: binary=%v %s\n", m.Binary, m.String())
	} else {
		fmt.Println("v1: NO broadcast")
	}
	p1.close()

	section("(e9) no vsn parameter: which serializer does the server assume?")
	p0, err := dial(ctx, env, env.PublishableKey, "", "A-novsn")
	if err != nil {
		fatal("dial novsn: %v", err)
	}
	// send a v1-style heartbeat and print whatever comes back raw (decoder assumes v1 objects)
	p0.heartbeat(ctx)
	p0.close()

	section("(e10) legacy anon JWT as apikey; bogus apikey; secret key")
	pj, err := dial(ctx, env, env.AnonJWT, "2.0.0", "A-anonjwt")
	if err == nil {
		pj.heartbeat(ctx)
		_, rep, ok = pj.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 10*time.Second)
		fmt.Printf("join with anon-JWT apikey: ok=%v reply=%s\n", ok, rep.String())
		pj.close()
	} else {
		fmt.Printf("dial with anon JWT apikey failed: %v\n", err)
	}
	pbog, err := dial(ctx, env, "sb_publishable_bogus", "2.0.0", "A-bogus")
	if err == nil {
		if m, ok := pbog.heartbeat(ctx); ok {
			fmt.Printf("bogus apikey heartbeat reply: %s\n", m.String())
		}
		if m, ok := pbog.waitFor(2*time.Second, func(m Msg) bool { return true }); ok {
			fmt.Printf("bogus apikey follow-up: %s\n", m.String())
		}
		_, rep, ok = pbog.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 5*time.Second)
		fmt.Printf("bogus apikey join: ok=%v reply=%s\n", ok, rep.String())
		pbog.close()
	} else {
		fmt.Printf("dial with a bogus apikey failed at upgrade: %v\n", err)
	}
	pnk, err := dial(ctx, env, "", "2.0.0", "A-nokey")
	if err == nil {
		if m, ok := pnk.heartbeat(ctx); ok {
			fmt.Printf("no apikey heartbeat reply: %s\n", m.String())
		}
		pnk.close()
	} else {
		fmt.Printf("dial with NO apikey failed at upgrade: %v\n", err)
	}
}

// ---------------------------------------------------------------- (e, cont.) token expiry, heartbeat timeout
func expExpiry(ctx context.Context, env Env) {
	section("(e11) JWT expiry on a joined private channel, and access_token renewal before expiry; heartbeat timeout")
	a := newPrincipalQuiet(ctx, env)
	b := newPrincipalQuiet(ctx, env)
	sidA := registerSession(ctx, env, a, "exp-alpha")
	sidB := registerSession(ctx, env, b, "exp-bravo")
	topicA := "realtime:brigade:session:" + sidA
	short := shortLivedTokenFor(env, a, 40*time.Second)
	printClaims("minted 40 s token", short)
	rpc(ctx, env, env.PublishableKey, short, "whoami", nil, "both", "sanity: PostgREST accepts the minted token")

	// socket 1: join with the 40 s token and never renew -> observe what happens at expiry
	p1, err := dial(ctx, env, env.PublishableKey, "2.0.0", "expire")
	if err != nil {
		fatal("dial: %v", err)
	}
	// socket 2: join with the 40 s token, renew at t=20 s with a 120 s token
	p2, err := dial(ctx, env, env.PublishableKey, "2.0.0", "renew")
	if err != nil {
		fatal("dial: %v", err)
	}
	// socket 3: joined, but no heartbeat ever -> observe the server's idle close
	p3, err := dial(ctx, env, env.PublishableKey, "2.0.0", "nohb")
	if err != nil {
		fatal("dial: %v", err)
	}
	jr1, rep, ok := p1.join(ctx, topicA, JoinConfig{Private: true, AccessToken: short}, 10*time.Second)
	fmt.Printf("expire: join ok=%v %s\n", ok, rep.String())
	jr2, rep, ok := p2.join(ctx, topicA, JoinConfig{Private: true, AccessToken: short}, 10*time.Second)
	fmt.Printf("renew: join ok=%v %s\n", ok, rep.String())
	_, rep, ok = p3.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("nohb: join ok=%v %s\n", ok, rep.String())
	_ = jr1

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// heartbeats for p1 and p2 every 20 s; p3 gets none
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p1.push(ctx, nil, "phoenix", "heartbeat", map[string]any{})
				p2.push(ctx, nil, "phoenix", "heartbeat", map[string]any{})
			}
		}
	}()
	// renewal at t=20 s on p2
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Second)
		fresh := shortLivedTokenFor(env, a, 120*time.Second)
		p2.push(ctx, &jr2, topicA, "access_token", map[string]any{"access_token": fresh})
	}()
	// a sender every 15 s so both channels show whether they still deliver
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 6; i++ {
			time.Sleep(15 * time.Second)
			rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": fmt.Sprintf("tick %d", i)})
			fmt.Printf("%7.3fs [sender] inserted tick %d\n", time.Since(p1.start).Seconds(), i)
		}
	}()
	// drain all three inboxes for 95 s (everything is printed by the read loops)
	deadline := time.After(95 * time.Second)
	for done := false; !done; {
		select {
		case <-deadline:
			done = true
		case <-p1.closed:
			fmt.Printf("%7.3fs expire socket closed\n", time.Since(p1.start).Seconds())
			p1.closed <- nil
			time.Sleep(time.Second)
		case <-p3.closed:
			fmt.Printf("%7.3fs nohb socket closed\n", time.Since(p1.start).Seconds())
			p3.closed <- nil
			time.Sleep(time.Second)
		case <-time.After(100 * time.Millisecond):
			for _, p := range []*Phx{p1, p2, p3} {
				select {
				case <-p.in:
				default:
				}
			}
		}
	}
	close(stop)
	wg.Wait()
	fmt.Println("expiry run finished")
	p1.close()
	p2.close()
	p3.close()
}
