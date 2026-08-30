package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// expRefresh2: does the refresh-token family really die after refresh_token_already_used?
// Chain: T0 -> T1 -> T2 (T2 never used). Wait 11 s. Use T0 (two behind) -> expect 400 already_used.
// Then: wait 11 s, use T2 (the never-used active token) -> dead or alive? Then the token that returns, 11 s later.
func expRefresh2(ctx context.Context, env Env) {
	section("(b2) refresh: is the family dead after refresh_token_already_used?")
	s0 := newPrincipal(ctx, env, "signup for refresh2")
	s1, _ := refresh(ctx, env, env.PublishableKey, s0.RefreshToken, "T0 -> T1")
	s2, _ := refresh(ctx, env, env.PublishableKey, s1.RefreshToken, "T1 -> T2 (T2 is now the active, never-used token)")
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, r := refresh(ctx, env, env.PublishableKey, s0.RefreshToken, "T0 again (two behind, outside the reuse interval) -> expect refresh_token_already_used")
	fmt.Printf("status=%d\n", r.Status)
	rpc(ctx, env, env.PublishableKey, s2.AccessToken, "whoami", nil, "both", "T2's access token on PostgREST right after the revocation")
	fmt.Println("sleeping 11 s (past the grace the auth digest observed) …")
	time.Sleep(11 * time.Second)
	s3, r3 := refresh(ctx, env, env.PublishableKey, s2.RefreshToken, "T2 (active, never used) 11 s after the revocation -> dead?")
	if r3.Status == 200 {
		fmt.Println("sleeping 11 s …")
		time.Sleep(11 * time.Second)
		_, _ = refresh(ctx, env, env.PublishableKey, s3.RefreshToken, "T3 11 s later -> still alive?")
		_, _ = refresh(ctx, env, env.PublishableKey, s2.RefreshToken, "T2 (now one behind) -> allowed?")
	}
	do(ctx, "GET /auth/v1/user with T2's access token (GoTrue-side check: does GoTrue reject a revoked session's JWT?)", "GET", env.APIURL+"/auth/v1/user", map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + s2.AccessToken}, nil)

	section("(b3) the auth digest's exact sequence: two-behind reuse, then the ACTIVE token < 10 s and again > 10 s later")
	u0 := newPrincipal(ctx, env, "signup for refresh3")
	u1, _ := refresh(ctx, env, env.PublishableKey, u0.RefreshToken, "U0 -> U1")
	u2, _ := refresh(ctx, env, env.PublishableKey, u1.RefreshToken, "U1 -> U2")
	u3, _ := refresh(ctx, env, env.PublishableKey, u2.RefreshToken, "U2 -> U3 (active)")
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, u0.RefreshToken, "U0 (three behind) -> expect already_used")
	_, _ = refresh(ctx, env, env.PublishableKey, u3.RefreshToken, "U3 (active) immediately -> the digest saw 200 (grace)")
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, u3.RefreshToken, "U3 again after 11 s -> the digest saw already_used")
	_ = u3
}

// expReauth: does pushing a new access_token re-run the topic policy? Delete the session row underneath a joined channel.
func expReauth(ctx context.Context, env Env) {
	section("(e12) re-authorization: session row deleted under a joined private channel, then access_token push")
	a := newPrincipalQuiet(ctx, env)
	b := newPrincipalQuiet(ctx, env)
	sidA := registerSession(ctx, env, a, "reauth-alpha")
	sidB := registerSession(ctx, env, b, "reauth-bravo")
	topicA := "realtime:brigade:session:" + sidA
	p, err := dial(ctx, env, env.PublishableKey, "2.0.0", "reauth")
	if err != nil {
		fatal("dial: %v", err)
	}
	jr, rep, ok := p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a.AccessToken}, 10*time.Second)
	fmt.Printf("join: ok=%v %s\n", ok, rep.String())
	rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "before"})
	if m, ok := p.waitFor(5*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("before: delivered %s\n", m.String())
	}
	// Transfer ownership of A's session to B underneath the open channel (as postgres, bypassing the API).
	out, err := exec.Command("docker", "exec", "-i", "supabase_db_sb", "psql", "-U", "postgres", "-d", "postgres", "-At",
		"-c", fmt.Sprintf("update brigade.sessions set owner_id = '%s' where id = '%s';", b.User.ID, sidA)).CombinedOutput()
	fmt.Printf("psql: %s err=%v\n", string(out), err)
	rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "after ownership change, before token push"})
	if m, ok := p.waitFor(4*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("after ownership change, before token push: STILL delivered (policy cached per connection): %s\n", m.String())
	} else {
		fmt.Println("after ownership change, before token push: not delivered")
	}
	a2, _ := refresh(ctx, env, env.PublishableKey, a.RefreshToken, "refresh A")
	p.push(ctx, &jr, topicA, "access_token", map[string]any{"access_token": a2.AccessToken})
	if m, ok := p.waitFor(6*time.Second, func(m Msg) bool { return m.Event == "system" || m.Event == "phx_close" || m.Event == "phx_error" }); ok {
		fmt.Printf("after access_token push: %s\n", m.String())
		if m2, ok := p.waitFor(2*time.Second, func(m Msg) bool { return m.Event == "system" || m.Event == "phx_close" || m.Event == "phx_error" }); ok {
			fmt.Printf("then: %s\n", m2.String())
		}
	} else {
		fmt.Println("after access_token push: no system/close message within 6 s")
	}
	rpcQuiet(ctx, env, env.PublishableKey, b.AccessToken, "send_message", map[string]any{"p_sender_session_id": sidB, "p_recipient_session_id": sidA, "p_body": "after token push"})
	if m, ok := p.waitFor(4*time.Second, func(m Msg) bool { return m.Event == "broadcast" }); ok {
		fmt.Printf("after token push: still delivered: %s\n", m.String())
	} else {
		fmt.Println("after token push: not delivered")
	}
	fmt.Println("rejoin attempt with the new token (policy now false):")
	_, rep, ok = p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: a2.AccessToken}, 10*time.Second)
	fmt.Printf("rejoin: ok=%v %s\n", ok, rep.String())
	p.close()
}

// expExpiredJoin: join a private topic with a correctly signed but already expired JWT.
func expExpiredJoin(ctx context.Context, env Env) {
	section("(e13) join with an already-expired JWT, and with a JWT for another user's session")
	a := newPrincipalQuiet(ctx, env)
	sidA := registerSession(ctx, env, a, "expjoin-alpha")
	topicA := "realtime:brigade:session:" + sidA
	expired := shortLivedTokenFor(env, a, -30*time.Second)
	p, err := dial(ctx, env, env.PublishableKey, "2.0.0", "expjoin")
	if err != nil {
		fatal("dial: %v", err)
	}
	_, rep, ok := p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: expired}, 10*time.Second)
	fmt.Printf("join with expired JWT: ok=%v %s\n", ok, rep.String())
	if m, ok := p.waitFor(2*time.Second, func(m Msg) bool { return true }); ok {
		fmt.Printf("follow-up: %s\n", m.String())
	}
	_, rep, ok = p.join(ctx, topicA, JoinConfig{Private: true, AccessToken: "garbage.token.here"}, 10*time.Second)
	fmt.Printf("join with a garbage access_token: ok=%v %s\n", ok, rep.String())
	if m, ok := p.waitFor(2*time.Second, func(m Msg) bool { return true }); ok {
		fmt.Printf("follow-up: %s\n", m.String())
	}
	// is the socket still usable afterwards?
	if m, ok := p.heartbeat(ctx); ok {
		fmt.Printf("socket still alive: %s\n", m.String())
	} else {
		fmt.Println("socket dead after the bad joins")
	}
	p.close()
}

// expRefresh3: after a two-behind revocation, the active token used inside the grace window yields a NEW token.
// Does that new token survive past the reuse interval (family alive) or not (family dead)?
func expRefresh3(ctx context.Context, env Env) {
	section("(b4) does the token minted inside the post-revocation grace window survive?")
	u0 := newPrincipal(ctx, env, "signup for refresh4")
	u1, _ := refresh(ctx, env, env.PublishableKey, u0.RefreshToken, "U0 -> U1")
	u2, _ := refresh(ctx, env, env.PublishableKey, u1.RefreshToken, "U1 -> U2 (active)")
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	_, _ = refresh(ctx, env, env.PublishableKey, u0.RefreshToken, "U0 (two behind) -> already_used, family revoked")
	u3, r3 := refresh(ctx, env, env.PublishableKey, u2.RefreshToken, "U2 (active) inside the grace window -> U3?")
	if r3.Status != 200 {
		fmt.Println("no grace this time; done")
		return
	}
	fmt.Println("sleeping 11 s …")
	time.Sleep(11 * time.Second)
	u4, r4 := refresh(ctx, env, env.PublishableKey, u3.RefreshToken, "U3 (minted inside the grace window) after 11 s -> alive or dead?")
	if r4.Status == 200 {
		fmt.Println("sleeping 11 s …")
		time.Sleep(11 * time.Second)
		_, _ = refresh(ctx, env, env.PublishableKey, u4.RefreshToken, "U4 after 11 s -> alive or dead?")
	}
	out, err := exec.Command("docker", "exec", "-i", "supabase_db_sb", "psql", "-U", "postgres", "-d", "postgres", "-At",
		"-c", fmt.Sprintf("select token, revoked, parent, to_char(updated_at,'HH24:MI:SS.MS') from auth.refresh_tokens where user_id = '%s' order by id;", u0.User.ID)).CombinedOutput()
	fmt.Printf("auth.refresh_tokens for this user (token|revoked|parent|updated_at):\n%s err=%v\n", string(out), err)
}
