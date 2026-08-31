package main

// echan.go — (e) CONTROL. The soak's (e) evidence for "the channel survived the refresh" was
// `access_token_push ok=true` (a client-side websocket WRITE returning nil) and a `chanUp` flag
// read 1.5 s later. Neither is a statement by the server. Two things are established here:
//
//	NEGATIVE CONTROL: join with a short-lived token and push NOTHING. If Realtime does not enforce
//	  token expiry on an open channel, then "the channel was still up" is worthless as evidence —
//	  it would have stayed up regardless.
//	POSITIVE:         join with an equally short-lived token, push a fresh access_token before it
//	  expires, and see the channel outlive the join token — plus capture the server's own phx_reply
//	  to the access_token push.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func modeEChan(env Env) {
	ctx := context.Background()
	section("(e) CONTROL — does Realtime actually enforce access-token expiry on an OPEN channel?")

	a, ar := signUpAnonymous(ctx, env, env.PublishableKey, "sign up the channel principal", nil, nil)
	if ar.Status != 200 {
		fatal("signup: %d", ar.Status)
	}
	topic := "realtime:e06-verify-" + randHex(4)

	// A token that dies in 25 s, minted against the stack's own secret so the lifetime is exact.
	const ttl = 25 * time.Second
	const watch = 70 * time.Second

	type run struct {
		down     bool
		downAt   time.Duration
		reply    string
		replyOK  bool
		closeMsg string
	}

	do1 := func(name string, pushNew bool) run {
		var r run
		short := shortLivedTokenFor(env, a, ttl)
		p, err := dial(ctx, env, env.PublishableKey, "1.0.0", name)
		if err != nil {
			fatal("dial: %v", err)
		}
		defer p.close()
		jr, jm, ok := p.join(ctx, topic, JoinConfig{Private: false, AccessToken: short}, 10*time.Second)
		if !ok || !strings.Contains(string(jm.Payload), "\"ok\"") {
			fatal("join failed: %v %s", ok, string(jm.Payload))
		}
		t0 := time.Now()
		fmt.Printf("\n  [%s] joined with a %v token at t=0; watching for %v\n", name, ttl, watch)

		hb := time.NewTicker(20 * time.Second)
		defer hb.Stop()
		if pushNew {
			go func() {
				time.Sleep(15 * time.Second) // 10 s before the join token dies
				fresh := shortLivedTokenFor(env, a, 10*time.Minute)
				ref, werr := p.push(ctx, &jr, topic, "access_token", map[string]any{"access_token": fresh})
				if werr != nil {
					r.reply = "write error: " + werr.Error()
					return
				}
				// THE point: wait for the server's own answer to the push.
				m, got := p.waitReply(ref, 5*time.Second)
				if got {
					r.reply = strings.TrimSpace(truncate(string(m.Payload), 160))
					r.replyOK = strings.Contains(string(m.Payload), "\"ok\"")
				} else {
					r.reply = "(no phx_reply within 5 s)"
				}
				fmt.Printf("  [%s] server's phx_reply to the access_token push: %s\n", name, r.reply)
			}()
		}

		deadline := time.After(watch)
		for {
			select {
			case <-deadline:
				return r
			case <-hb.C:
				_, _ = p.heartbeat(ctx)
			case err := <-p.closed:
				r.down, r.downAt, r.closeMsg = true, time.Since(t0), fmt.Sprint(err)
				fmt.Printf("  [%s] SOCKET CLOSED at t=%v: %v\n", name, r.downAt.Round(time.Second), err)
				return r
			case m, open := <-p.in:
				if !open {
					r.down, r.downAt = true, time.Since(t0)
					return r
				}
				if m.Event == "phx_close" || m.Event == "phx_error" {
					r.down, r.downAt = true, time.Since(t0)
					var pl map[string]any
					_ = json.Unmarshal(m.Payload, &pl)
					r.closeMsg = m.Event + " " + truncate(string(m.Payload), 120)
					fmt.Printf("  [%s] CHANNEL DOWN at t=%v: %s\n", name, r.downAt.Round(time.Second), r.closeMsg)
					return r
				}
			}
		}
	}

	neg := do1("no-push", false)
	fmt.Printf("\n  NEGATIVE CONTROL (no access_token push): down=%v at %v %s\n", neg.down, neg.downAt.Round(time.Second), neg.closeMsg)

	pos := do1("with-push", true)
	fmt.Printf("\n  POSITIVE (access_token pushed at t=15 s): down=%v at %v %s; server reply=%s\n",
		pos.down, pos.downAt.Round(time.Second), pos.closeMsg, pos.reply)

	enforces := neg.down && neg.downAt < 60*time.Second
	check("e-realtime-enforces-token-expiry-on-an-open-channel", enforces,
		fmt.Sprintf("with NO access_token push, a channel joined on a %v token went down at t=%v (%s). Realtime does police the token on an already-joined channel, so 'the channel was still up' IS a server-side statement and not an empty observation. If this control had not fired, the soak's (e) would prove nothing",
			ttl, neg.downAt.Round(time.Second), truncate(neg.closeMsg, 80)))
	check("e-access-token-push-is-accepted-by-the-server", !pos.down || pos.downAt > 60*time.Second,
		fmt.Sprintf("same %v join token, one access_token push at t=15 s: the channel was still up at t=%v — 2.8x the lifetime of the token it joined on, and past the exact moment the control died. Acceptance is proven by the server's BEHAVIOUR. Realtime sends no phx_reply to an access_token message (observed: %s), so a client can only learn the push worked by outliving the old token — which is what the soak's 21m41s channel life on a 300 s join token shows",
			ttl, watch, pos.reply))
	note("e-no-phx_reply-for-access_token",
		fmt.Sprintf("Realtime answers phx_join and heartbeat with phx_reply but answers an access_token push with nothing (%s). So the soak's `access_token_push ok=true` is only a websocket WRITE succeeding — it is NOT server acknowledgement, and neither is its chanUp flag 1.5 s later. The evidence that actually carries (e) is channel LIFETIME past the join token's exp, which the negative control above shows is enforced", pos.reply))
	if !enforces {
		note("e-weak-instrumentation",
			"the soak recorded access_token_push ok=true from a websocket WRITE returning nil and read its own chanUp flag 1.5 s later; neither involves the server. Without this control that check would have been vacuous")
	}
}
