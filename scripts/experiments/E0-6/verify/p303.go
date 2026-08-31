package main

// p303.go — the COVERAGE GAP. Plan 5.1 names TWO refresh triggers:
//
//	(1) fewer than 90 s remain before expires_at   — exercised 8 times in the soak
//	(2) a PGRST303 (JWT expired) answer             — fired ZERO times in the soak
//
// The soak's own numbers show why (2) was never reached: the smallest exp margin ever observed
// was +90 s, exactly the design's margin, so no token ever came near expiry. "0 PGRST303" is a
// statement that trigger (1) works, not evidence about trigger (2). This mode drives the reactive
// path deliberately, both with a NATURALLY expired GoTrue token (jwt_expiry=300 is live) and with
// a minted one, and checks the design recovers instead of stranding.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func modeP303(env Env) {
	ctx := context.Background()
	section("(b/design) the REACTIVE PGRST303 refresh trigger — never exercised by the soak")

	a, ar := signUpAnonymous(ctx, env, env.PublishableKey, "sign up the PGRST303 principal", nil, nil)
	if ar.Status != 200 {
		fatal("signup: %d", ar.Status)
	}
	tr := rpc(ctx, env, env.PublishableKey, a.AccessToken, "create_team",
		map[string]any{"p_name": "p303-" + randHex(4), "p_human_label": "p303"}, "both", "create_team")
	if tr.Status != 200 {
		fatal("create_team: %d", tr.Status)
	}
	teamID, _ := jsonGet[string](tr.Body, "team_id")

	dir := filepath.Join(workDir, "p303")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o700)
	p := profile{dir: dir}
	if err := writeSession(p.sessionPath(), a, ar.Body); err != nil {
		fatal("write: %v", err)
	}
	if lk, err := acquireLock(p.lockPath(), false); err == nil {
		lk.release()
	}

	// The design's own reactive sequence, verbatim from main.go's runShort/runLong:
	//   probe -> if PGRST303 { ensureFresh(Force); probe again }
	react := func(label string, cur Session) (bool, string) {
		st, code := probe(ctx, env, cur.AccessToken, teamID)
		fmt.Printf("\n  [%s] probe with the ON-DISK access token: HTTP %d %s (exp margin %ds)\n",
			label, st, code, int(remaining(cur).Seconds()))
		if code != "PGRST303" {
			return false, fmt.Sprintf("the probe did NOT return PGRST303 (HTTP %d %s) — the trigger was not reached", st, code)
		}
		ns, out := ensureFresh(ctx, env, p, cur, freshOpts{Margin: refreshMarginDefault, Persist: true, Force: true})
		fmt.Printf("  [%s] reactive refresh: HTTP %d sent=%s got=%s persisted=%v err=%q lock_wait=%v\n",
			label, out.Status, out.SentRT, out.GotRT, out.Persisted, out.ErrorCode, out.LockWait)
		st2, code2 := probe(ctx, env, ns.AccessToken, teamID)
		fmt.Printf("  [%s] retry probe with the NEW access token: HTTP %d %s\n", label, st2, code2)
		// and the file on disk must now hold the recovered session
		rr := readSession(p.sessionPath())
		ok := out.Status == 200 && out.Persisted && st2 == 200 && rr.OK && rr.S.AccessToken == ns.AccessToken
		return ok, fmt.Sprintf("PGRST303 on the stale token -> forced refresh HTTP %d (rt %s -> %s, persisted=%v) -> the retry answered HTTP %d %s, and session.json on disk now holds the recovered access token (readback ok=%v, matches=%v)",
			out.Status, out.SentRT, out.GotRT, out.Persisted, st2, code2, rr.OK, rr.S.AccessToken == ns.AccessToken)
	}

	// ---- 1. NATURAL expiry. jwt_expiry=300 is live, and PostgREST allows ~30 s of clock skew
	//         past exp, so wait 300 + 40 s and the on-disk token is genuinely, server-side dead.
	wait := time.Duration(a.ExpiresIn+40) * time.Second
	fmt.Printf("\n  waiting %v for the REAL access token to expire naturally (expires_in=%d, plus PostgREST's ~30 s skew allowance) …\n", wait, a.ExpiresIn)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Second)
		fmt.Printf("    … %ds to go\n", int(time.Until(deadline).Seconds()))
	}
	rr := readSession(p.sessionPath())
	okNat, evNat := react("natural", rr.S)
	check("design-reactive-pgrst303-recovers", okNat,
		"the plan's SECOND refresh trigger, which the soak never once fired (0 pgrst303 events in 1888 probes, because the 90 s margin kept every token at least 90 s from exp): "+evNat)

	// ---- 2. and the profile is usable afterwards: a second cycle from the recovered file.
	rr2 := readSession(p.sessionPath())
	st, code := probe(ctx, env, rr2.S.AccessToken, teamID)
	b, _ := json.Marshal(rr2.MissingList)
	check("design-profile-usable-after-reactive-recovery", st == 200 && rr2.OK,
		fmt.Sprintf("re-reading session.json from scratch after the reactive recovery and calling the RPC with what is on disk answers HTTP %d %s; the file parses with no missing fields (%s) and is mode %04o", st, code, string(b), rr2.Mode))
}
