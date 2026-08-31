package main

// verify/main.go — ADVERSARIAL VERIFICATION of E0-6.
//
// Every check here is a CONTROL: it tries to make a detector fire, or to establish that a
// "zero X" result could have been non-zero. Nothing here re-states the soak's claims; each mode
// answers one of the six interrogation points.
//
//	-mode bnull     (b) null control: is brigade.list_members actually refused with a stale JWT?
//	-mode tearctl   (c) torn-read detector positive control + a non-atomic writer differential
//	-mode contend   (c) REAL contention between two refreshers, plus the 10 s `unavailable` bound
//	-mode oneb      (f) one-behind / two-behind boundary, confirmed from server responses,
//	                    plus family survival through a LATER refresh
//	-mode holder    (internal) child that holds the sidecar lock for -hold ms
//	-mode racer     (internal) child that hammers the lock with a real refresh inside it

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	envPath = "/Users/rjae/Development/appshapes/brigade/.env.test"
	workDir = "/Users/rjae/Development/appshapes/brigade/.ignored/exp/E0-6/verify/work"
)

var results []string

func check(name string, ok bool, evidence string) {
	tag := "FAIL"
	if ok {
		tag = "PASS"
	}
	fmt.Printf("\n[%s] %s\n      %s\n", tag, name, evidence)
	results = append(results, fmt.Sprintf("%s %s :: %s", tag, name, evidence))
}

func note(name, evidence string) {
	fmt.Printf("\n[NOTE] %s\n      %s\n", name, evidence)
	results = append(results, fmt.Sprintf("NOTE %s :: %s", name, evidence))
}

func main() {
	mode := flag.String("mode", "", "bnull|tearctl|contend|oneb|echan|holder|racer")
	hold := flag.Int("hold", 0, "holder: ms to hold the sidecar lock")
	lockPath := flag.String("lock", "", "holder/racer: sidecar path")
	sess := flag.String("sess", "", "racer: session.json path")
	rounds := flag.Int("rounds", 0, "racer: rounds")
	tag := flag.String("tag", "", "racer: label")
	out := flag.String("out", "", "racer/holder: ndjson path")
	flag.Parse()
	env := loadEnv(envPath)
	_ = os.MkdirAll(workDir, 0o755)

	switch *mode {
	case "bnull":
		modeBNull(env)
	case "tearctl":
		modeTearCtl(env)
	case "contend":
		modeContend(env)
	case "oneb":
		modeOneBehind(env)
	case "echan":
		modeEChan(env)
	case "p303":
		modeP303(env)
	case "holder":
		modeHolder(*lockPath, *hold, *out)
	case "racer":
		modeRacer(env, *lockPath, *sess, *rounds, *tag, *out)
	default:
		fatal("pick -mode")
	}

	if *mode != "holder" && *mode != "racer" {
		fmt.Printf("\n================ SUMMARY (%s) ================\n", *mode)
		for _, r := range results {
			fmt.Println(r)
		}
	}
}

// ================================================================= (b) NULL CONTROL
//
// The soak's (b) rests entirely on: "brigade.list_members, which PostgREST refuses with PGRST303
// on an expired JWT". If that premise is false — if the RPC answers 200 to a stale, absent or
// anonymous credential — then 1888 HTTP 200s prove nothing about token validity.
// This mode establishes the premise experimentally instead of assuming it.

func modeBNull(env Env) {
	ctx := context.Background()
	section("(b) NULL CONTROL — build a real fixture, then attack list_members with bad credentials")

	a, ar := signUpAnonymous(ctx, env, env.PublishableKey, "sign up A (team owner)", nil, nil)
	if ar.Status != 200 {
		fatal("signup A: %d", ar.Status)
	}
	tr := rpc(ctx, env, env.PublishableKey, a.AccessToken, "create_team",
		map[string]any{"p_name": "v-" + randHex(4), "p_human_label": "v-a"}, "both", "create_team (A)")
	if tr.Status != 200 {
		fatal("create_team: %d", tr.Status)
	}
	teamID, _ := jsonGet[string](tr.Body, "team_id")
	secret, _ := jsonGet[string](tr.Body, "join_secret")
	if teamID == "" {
		// tolerate an array-shaped answer
		var arr []map[string]any
		if json.Unmarshal(tr.Body, &arr) == nil && len(arr) > 0 {
			teamID, _ = arr[0]["team_id"].(string)
			secret, _ = arr[0]["join_secret"].(string)
		}
	}
	fmt.Printf("\nfixture: team_id=%s\n", teamID)

	c, cr := signUpAnonymous(ctx, env, env.PublishableKey, "sign up C (NOT a member)", nil, nil)
	if cr.Status != 200 {
		fatal("signup C: %d", cr.Status)
	}
	_ = secret

	callHdr := func(label string, hdr map[string]string) (int, string) {
		h := map[string]string{
			"apikey": env.PublishableKey, "Content-Type": "application/json",
			"Accept-Profile": "brigade", "Content-Profile": "brigade",
		}
		for k, v := range hdr {
			if v == "" {
				delete(h, k)
			} else {
				h[k] = v
			}
		}
		r := do(ctx, label, "POST", env.APIURL+"/rest/v1/rpc/list_members", h,
			map[string]any{"p_team_id": teamID})
		code := ""
		if e, ok := parsePgError(r.Body); ok {
			code = e.Code
		}
		return r.Status, code
	}
	call := func(label, jwt string) (int, string) {
		return callHdr(label, map[string]string{"Authorization": "Bearer " + jwt})
	}

	type row struct {
		what   string
		status int
		code   string
		body   string
	}
	var rows []row
	add := func(what string, st int, code string) { rows = append(rows, row{what, st, code, ""}) }

	// 1. POSITIVE CONTROL — a fresh, valid token must be accepted.
	st, code := call("A: FRESH valid access token (positive control)", a.AccessToken)
	add("fresh valid JWT (A, team member)", st, code)
	freshOK := st == 200

	// 2. THE decisive control — a REAL, server-issued access token that has genuinely expired.
	//    Taken from the finished soak's own profile so it is exactly the artefact under discussion.
	realExpired := ""
	var realExpAgo int64
	if b, err := os.ReadFile("/Users/rjae/Development/appshapes/brigade/.ignored/exp/E0-6/run/profiles/default/session.json"); err == nil {
		var s Session
		if json.Unmarshal(b, &s) == nil {
			realExpired = s.AccessToken
			realExpAgo = time.Now().Unix() - s.ExpiresAt
		}
	}
	expiredRejected := false
	if realExpired != "" && realExpAgo > 0 {
		st, code = call(fmt.Sprintf("REAL GoTrue access token from the soak profile, expired %ds ago", realExpAgo), realExpired)
		add(fmt.Sprintf("REAL expired GoTrue JWT (%ds past exp)", realExpAgo), st, code)
		expiredRejected = st != 200
	} else {
		note("b-real-expired-token", "no genuinely expired real token was available to test with")
	}

	// 3. A correctly-SIGNED token whose exp is in the past (same condition, minted so it is repeatable).
	mintedExpired := shortLivedTokenFor(env, a, -60*time.Second)
	st, code = call("minted, correctly signed, exp 60 s in the PAST", mintedExpired)
	add("minted expired JWT (valid signature, exp-60s)", st, code)
	mintedRejected := st != 200

	// 4. A token that expires WHILE we watch: valid now, refused 4 s later. Same token, two answers.
	shortTok := shortLivedTokenFor(env, a, 3*time.Second)
	st1, c1 := call("minted 3 s token, used IMMEDIATELY", shortTok)
	time.Sleep(4500 * time.Millisecond)
	st2, c2 := call("the SAME token, 4.5 s later (now past exp)", shortTok)
	add("3s token used immediately", st1, c1)
	add("SAME token after it expired", st2, c2)
	flip := st1 == 200 && st2 != 200

	// 5. No Authorization header at all — apikey only. If this answers 200 the whole probe is vacuous.
	st, code = callHdr("NO Authorization header, apikey only", map[string]string{"Authorization": ""})
	add("no Authorization header (apikey only)", st, code)
	noAuthRejected := st != 200

	// 6. Authorization: Bearer <anon JWT> — the un-authenticated role.
	st, code = call("Authorization: Bearer <anon key> (role=anon)", env.AnonJWT)
	add("anon-role JWT", st, code)
	anonRejected := st != 200

	// 7. Structurally valid JWT with a BAD signature.
	bad := mintJWT("not-the-jwt-secret-not-the-jwt-secret-xx", map[string]any{
		"aud": "authenticated", "role": "authenticated", "sub": a.User.ID,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()})
	st, code = call("valid shape, WRONG signature", bad)
	add("bad-signature JWT", st, code)
	badSigRejected := st != 200

	// 8. Garbage.
	st, code = call("garbage string as bearer token", "not.a.jwt")
	add("garbage bearer", st, code)
	garbageRejected := st != 200

	// 9. A DIFFERENT valid principal who is not on the team — does the RPC discriminate by identity,
	//    or would any authenticated token do?
	st, code = call("C: fresh valid token, NOT a member of the team", c.AccessToken)
	add("valid JWT of a non-member", st, code)

	section("(b) null-control matrix")
	for _, r := range rows {
		fmt.Printf("  %-46s -> HTTP %-4d %s\n", r.what, r.status, r.code)
	}

	check("b-null-control-rpc-is-jwt-gated",
		freshOK && expiredRejected && mintedRejected && noAuthRejected && anonRejected && badSigRejected && garbageRejected,
		fmt.Sprintf("list_members answers 200 to a fresh token but REFUSES every degraded credential: real expired GoTrue token (%ds past exp) rejected=%v, minted-expired rejected=%v, no Authorization rejected=%v, anon-role rejected=%v, bad signature rejected=%v, garbage rejected=%v. The soak's (b) premise holds: a 200 from this RPC is a server-side statement that the JWT was valid",
			realExpAgo, expiredRejected, mintedRejected, noAuthRejected, anonRejected, badSigRejected, garbageRejected))
	check("b-same-token-flips-at-exp", flip,
		fmt.Sprintf("the identical token answered HTTP %d %s while inside its lifetime and HTTP %d %s 4.5 s later — the refusal tracks exp, not the token's shape", st1, c1, st2, c2))
}

// ================================================================= (c) TORN-READ DETECTOR CONTROL
//
// "0 torn reads in 732916 reads" is only evidence if (i) the detector can fire, and (ii) the
// experiment had the statistical power to catch tearing had it existed. Note that the soak's
// power comes from its 8 FILE WRITES, not from its 732916 reads: a read can only tear during a
// write. This mode makes the detector fire on demand and then runs a NON-ATOMIC writer at high
// rate under the same prober, so "atomic = 0 catches" can be compared against a positive.

func modeTearCtl(env Env) {
	_ = env
	section("(c) TORN-READ DETECTOR — can it fire at all?")

	dir := filepath.Join(workDir, "tear")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "session.json")

	// A realistic, complete session.json to mutilate.
	good := Session{AccessToken: "aaa.bbb.ccc", TokenType: "bearer", ExpiresIn: 300,
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix(), RefreshToken: "rrrrrrrr"}
	good.User.ID = "20cc426f-1fbb-4b50-9576-4e55000345d6"
	good.User.Role = "authenticated"
	goodBytes, _ := json.Marshal(good)
	fmt.Printf("  reference session.json is %d bytes\n", len(goodBytes))

	type tcase struct {
		name  string
		bytes []byte
		mode  os.FileMode
	}
	partial, _ := json.Marshal(map[string]any{ // valid JSON, but a field the adapter needs is gone
		"access_token": "aaa.bbb.ccc", "token_type": "bearer", "expires_in": 300,
		"expires_at": good.ExpiresAt, "user": map[string]any{"id": good.User.ID}})
	cases := []tcase{
		{"intact file (negative control — detector must NOT fire)", goodBytes, 0o600},
		{"truncated to 50% (a torn write)", goodBytes[:len(goodBytes)/2], 0o600},
		{"truncated to 90% (a nearly-complete torn write)", goodBytes[:len(goodBytes)*9/10], 0o600},
		{"truncated to 1 byte", goodBytes[:1], 0o600},
		{"zero-length file (rename raced by a create)", []byte{}, 0o600},
		{"valid JSON, refresh_token missing", partial, 0o600},
		{"intact bytes but mode 0644", goodBytes, 0o644},
	}
	firedParse, firedMissing, firedMode := 0, 0, 0
	negOK := false
	for i, c := range cases {
		_ = os.Remove(path)
		_ = os.WriteFile(path, c.bytes, c.mode)
		_ = os.Chmod(path, c.mode)
		rr := readSession(path)
		fired := rr.ParseErr != "" || len(rr.MissingList) > 0 || rr.Mode != 0o600
		fmt.Printf("  %-52s ok=%-5v parse_err=%-22s missing=%v mode=%04o  DETECTOR_FIRED=%v\n",
			c.name, rr.OK, truncate(rr.ParseErr, 20), rr.MissingList, rr.Mode, fired)
		if i == 0 {
			negOK = rr.OK && rr.Mode == 0o600 && !fired
		} else {
			if rr.ParseErr != "" {
				firedParse++
			}
			if len(rr.MissingList) > 0 {
				firedMissing++
			}
			if rr.Mode != 0o600 {
				firedMode++
			}
		}
	}
	check("c-detector-can-fire", negOK && firedParse >= 4 && firedMissing >= 1 && firedMode >= 1,
		fmt.Sprintf("the intact file passes cleanly (negative control ok=%v) while %d/6 mutilations trip the parse detector, %d trip the missing-field detector and %d trip the mode detector. The soak's '0 parse failures, 0 missing fields, 0 non-0600' is therefore a real zero and not a dead detector", negOK, firedParse, firedMissing, firedMode))

	// ---- the differential: same prober, same rate, atomic writer vs non-atomic writer.
	section("(c) DIFFERENTIAL — the same 2 ms prober against an atomic and a non-atomic writer")

	runWriter := func(label string, atomic bool, writes int) (reads, caught int, examples []string) {
		d := filepath.Join(workDir, "diff-"+label)
		_ = os.RemoveAll(d)
		_ = os.MkdirAll(d, 0o700)
		pth := filepath.Join(d, "session.json")
		_ = os.WriteFile(pth, goodBytes, 0o600)

		stop := make(chan struct{})
		var mu sync.Mutex
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { // the exact prober loop the soak used, same 2 ms cadence
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rr := readSession(pth)
				mu.Lock()
				reads++
				if rr.ParseErr != "" || len(rr.MissingList) > 0 {
					caught++
					if len(examples) < 3 {
						examples = append(examples, truncate(rr.ParseErr+strings.Join(rr.MissingList, ","), 60))
					}
				}
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
			}
		}()

		for i := 0; i < writes; i++ {
			s := good
			s.RefreshToken = fmt.Sprintf("rt-%08d", i)
			b, _ := json.Marshal(s)
			if atomic {
				_ = writeAtomic(pth, b) // the design under test
			} else {
				// what the design AVOIDS: truncate in place, write in two parts with a gap.
				f, err := os.OpenFile(pth, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
				if err == nil {
					half := len(b) / 2
					_, _ = f.Write(b[:half])
					time.Sleep(3 * time.Millisecond) // the tearing window
					_, _ = f.Write(b[half:])
					_ = f.Close()
				}
			}
			time.Sleep(8 * time.Millisecond)
		}
		close(stop)
		wg.Wait()
		return
	}

	const writes = 400
	nr, nc, nex := runWriter("nonatomic", false, writes)
	ar, ac, _ := runWriter("atomic", true, writes)
	fmt.Printf("  NON-ATOMIC writer: %d writes, %d lock-free reads, %d torn reads caught  e.g. %v\n", writes, nr, nc, nex)
	fmt.Printf("  ATOMIC    writer: %d writes, %d lock-free reads, %d torn reads caught\n", writes, ar, ac)
	check("c-atomic-write-differential", nc > 0 && ac == 0,
		fmt.Sprintf("under an identical 2 ms prober, a NON-atomic in-place rewrite is caught tearing %d times in %d writes (%d reads), while the design's CreateTemp+fsync+rename is caught %d times in %d writes (%d reads). The prober and the detector demonstrably work; the atomic write is what makes the count zero", nc, writes, nr, ac, writes, ar))
	note("c-statistical-power",
		fmt.Sprintf("the soak's power came from its 8 file rotations, not from its 732916 reads — a read can only tear during a write, and 732916 reads spread over 8 writes is ~114000 reads per write of a file that is only ever swapped by rename. This differential supplies the missing power: %d writes at a 10x tighter cadence, still 0 catches for the atomic writer against %d for the non-atomic one", writes, nc))

	// ---- stray temp files after a burst of atomic writes
	d := filepath.Join(workDir, "diff-atomic")
	ents, _ := os.ReadDir(d)
	stray := 0
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
		if strings.HasSuffix(e.Name(), ".tmp") {
			stray++
		}
	}
	check("c-no-stray-temp-files", stray == 0,
		fmt.Sprintf("after %d atomic writes the profile directory holds exactly %v — %d stray .tmp files", writes, names, stray))
}

// ================================================================= (c) REAL CONTENTION
//
// The soak observed 7897 acquisitions with ZERO contended (every one succeeded on attempt 1), and
// attributed that to the skip rule. Two facts here: first, the harness's contention prober ticked
// every 250 ms while the refreshers ticked at 1 s and 3 s — commensurate periods started within
// 250 ms of each other, so the probe's phase relative to a refresh is FIXED and it can never land
// inside the ~46 ms hold. Zero contention was structurally guaranteed, not measured. Second, this
// mode produces real, jittered contention between two REFRESHERS and measures what it costs.

func modeContend(env Env) {
	section("(c) REAL CONTENTION — two refreshers racing the same sidecar with random phase")

	ctx := context.Background()
	dir := filepath.Join(workDir, "contend")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.Chmod(dir, 0o700)
	p := profile{dir: dir}

	a, ar := signUpAnonymous(ctx, env, env.PublishableKey, "sign up the contention principal", nil, nil)
	if ar.Status != 200 {
		fatal("signup: %d", ar.Status)
	}
	if err := writeSession(p.sessionPath(), a, ar.Body); err != nil {
		fatal("write session: %v", err)
	}
	if lk, err := acquireLock(p.lockPath(), false); err == nil {
		lk.release()
	}

	// ---- 1. the 10 s `unavailable` bound. One process holds for 12 s; we try to acquire.
	section("(c-bound) the 10 s LOCK_NB bound must actually fire")
	outPath := filepath.Join(dir, "holder.ndjson")
	self, _ := os.Executable()
	hc := exec.Command(self, "-mode=holder", "-lock="+p.lockPath(), "-hold=13000", "-out="+outPath)
	hc.Stdout, hc.Stderr = os.Stdout, os.Stderr
	if err := hc.Start(); err != nil {
		fatal("start holder: %v", err)
	}
	// wait until the holder really has it
	for i := 0; i < 200; i++ {
		if held, h, _ := tryLockNB(p.lockPath()); held {
			break
		} else if h != nil {
			h.release()
		}
		time.Sleep(20 * time.Millisecond)
	}
	held, h, _ := tryLockNB(p.lockPath())
	if h != nil {
		h.release()
	}
	t0 := time.Now()
	lk, err := acquireLock(p.lockPath(), false)
	waited := time.Since(t0)
	gotUnavail := err != nil && strings.Contains(err.Error(), "unavailable")
	tries := 0
	if lk != nil {
		tries = lk.Attempts
		if err == nil {
			lk.release()
		}
	}
	fmt.Printf("  holder confirmed holding (third-process LOCK_NB held=%v); acquire returned err=%v after %v in %d attempts\n",
		held, err, waited.Round(time.Millisecond), tries)
	_ = hc.Wait()
	check("c-10s-bound-fires", held && gotUnavail && waited > 9*time.Second && waited < 11*time.Second,
		fmt.Sprintf("with a genuine 13 s holder confirmed by a third process's LOCK_NB (held=%v), acquireLock gave up with %q after %v and %d LOCK_NB attempts — the plan's 10 s bound is real and reachable, not dead code. (A caller that waits the full bound therefore returns `unavailable` rather than blocking forever.)",
			held, errStr(err), waited.Round(time.Millisecond), tries))

	// ---- 2. real refresher-vs-refresher contention, jittered so no phase lock is possible.
	section("(c-race) two real refreshers, forced refresh under the lock, random jitter")
	racerOut := filepath.Join(dir, "race.ndjson")
	_ = os.Remove(racerOut)
	const rounds = 40
	var cmds []*exec.Cmd
	for _, tg := range []string{"r1", "r2"} {
		c := exec.Command(self, "-mode=racer", "-lock="+p.lockPath(), "-sess="+p.sessionPath(),
			"-rounds="+fmt.Sprint(rounds), "-tag="+tg, "-out="+racerOut)
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			fatal("start racer: %v", err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		_ = c.Wait()
	}

	type rec struct {
		Tag      string  `json:"tag"`
		WaitUS   int64   `json:"wait_us"`
		Tries    int     `json:"tries"`
		HoldMS   float64 `json:"hold_ms"`
		Status   int     `json:"status"`
		Err      string  `json:"err"`
		SentRT   string  `json:"sent_rt"`
		GotRT    string  `json:"got_rt"`
		Refresh  float64 `json:"refresh_ms"`
		Skipped  bool    `json:"skipped"`
		Persist  bool    `json:"persisted"`
		ErrCode  string  `json:"error_code"`
		Sequence int     `json:"seq"`
	}
	var recs []rec
	b, _ := os.ReadFile(racerOut)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r rec
		if json.Unmarshal([]byte(line), &r) == nil {
			recs = append(recs, r)
		}
	}
	var contended []rec
	var waits []float64
	var holds []float64
	byTag := map[string]int{}
	status200, statusOther := 0, 0
	codes := map[string]int{}
	for _, r := range recs {
		byTag[r.Tag]++
		waits = append(waits, float64(r.WaitUS)/1000)
		if r.Tries > 1 {
			contended = append(contended, r)
		}
		if r.HoldMS > 0 {
			holds = append(holds, r.HoldMS)
		}
		if r.Status == 200 {
			status200++
		} else if r.Status != 0 {
			statusOther++
			codes[r.ErrCode]++
		}
	}
	sort.Float64s(waits)
	sort.Float64s(holds)
	pct := func(v []float64, q float64) float64 {
		if len(v) == 0 {
			return 0
		}
		i := int(q * float64(len(v)-1))
		return v[i]
	}
	var cw []float64
	for _, r := range contended {
		cw = append(cw, float64(r.WaitUS)/1000)
	}
	sort.Float64s(cw)
	fmt.Printf("  acquisitions=%d (per racer %v)\n", len(recs), byTag)
	fmt.Printf("  refreshes: %d HTTP 200, %d non-200 %v\n", status200, statusOther, codes)
	fmt.Printf("  lock HOLD (acquire..release, includes the refresh round trip): median %.1f ms, max %.1f ms\n",
		pct(holds, 0.5), pct(holds, 1.0))
	fmt.Printf("  lock WAIT all:       median %.3f ms  p95 %.3f ms  max %.3f ms\n", pct(waits, 0.5), pct(waits, 0.95), pct(waits, 1.0))
	if len(cw) > 0 {
		fmt.Printf("  lock WAIT CONTENDED: n=%d  min %.1f ms  median %.1f ms  max %.1f ms\n", len(cw), cw[0], pct(cw, 0.5), cw[len(cw)-1])
	}
	check("c-real-contention-occurred", len(contended) >= 5,
		fmt.Sprintf("%d of %d acquisitions by two independent refresher PROCESSES needed more than one LOCK_NB attempt, i.e. genuine overlap at the lock. The soak itself had 0 of 7897 — a distribution of zeros is evidence of no contention, not of a fast lock", len(contended), len(recs)))
	if len(cw) > 0 {
		check("c-contended-wait-is-the-poll-interval", cw[0] >= 95 && pct(cw, 0.5) < 220,
			fmt.Sprintf("every contended wait is one or more whole 100 ms polls (min %.1f ms, median %.1f ms, max %.1f ms) while the lock is only HELD %.1f ms (median). The cost of contention is the design's LOCK_NB-every-100 ms poll, not the holder: a blocking flock, or a 5-10 ms poll, would cut a contended wait by roughly half",
				cw[0], pct(cw, 0.5), cw[len(cw)-1], pct(holds, 0.5)))
	}
	if len(recs) > 0 {
		check("c-refresh-chain-survives-contention", statusOther == 0,
			fmt.Sprintf("%d serialized refreshes across two racing processes on one file, %d HTTP 200, %d failures %v — mutual exclusion held under real contention, no refresh_token_already_used", len(recs), status200, statusOther, codes))
	}
}

func modeHolder(lockPath string, holdMS int, out string) {
	lk, err := acquireLock(lockPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "holder: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  [holder pid=%d] holding the sidecar for %d ms\n", os.Getpid(), holdMS)
	time.Sleep(time.Duration(holdMS) * time.Millisecond)
	lk.release()
	fmt.Printf("  [holder pid=%d] released\n", os.Getpid())
	_ = out
}

// modeRacer is a REAL second Brigade process: it runs the design's own ensureFresh with Force, so
// every round takes the lock and holds it across a live refresh round trip.
func modeRacer(env Env, lockPath, sessPath string, rounds int, tag, out string) {
	ctx := context.Background()
	p := profile{dir: filepath.Dir(sessPath)}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fatal("racer out: %v", err)
	}
	defer f.Close()
	rnd := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(os.Getpid())))
	for i := 0; i < rounds; i++ {
		// random phase: this is exactly what the soak's commensurate tickers could not produce
		time.Sleep(time.Duration(20+rnd.Intn(120)) * time.Millisecond)
		rr := readSession(p.sessionPath())
		t0 := time.Now()
		_, o := ensureFresh(ctx, env, p, rr.S, freshOpts{Margin: refreshMarginDefault, Persist: true, Force: true})
		rec := map[string]any{
			"tag": tag, "seq": i, "pid": os.Getpid(),
			"wait_us": o.LockWait.Microseconds(), "tries": o.LockTries,
			"hold_ms": float64(time.Since(t0).Microseconds()) / 1000,
			"status": o.Status, "sent_rt": o.SentRT, "got_rt": o.GotRT,
			"refresh_ms": float64(o.Elapsed.Microseconds()) / 1000,
			"skipped":    o.Skipped, "persisted": o.Persisted, "error_code": o.ErrorCode,
			"lock_err": o.LockErr, "t": time.Now().UTC().Format(time.RFC3339Nano),
		}
		b, _ := json.Marshal(rec)
		_, _ = f.Write(append(b, '\n'))
	}
}

// ================================================================= (f) ONE-BEHIND, FROM THE SERVER
//
// The soak's (f) claim is that a non-persisting process leaves the file one step behind and the next
// writer redeems it. Two things must be true and must come from the SERVER, not from inference:
// the answer to a one-behind token must carry the ACTIVE token, and the family must still work
// afterwards — a LATER refresh with the rotated token must still succeed.

func modeOneBehind(env Env) {
	ctx := context.Background()
	section("(f) one-behind / two-behind boundary on a throwaway principal, from server responses")

	s0, r0 := signUpAnonymous(ctx, env, env.PublishableKey, "sign up the boundary principal", nil, nil)
	if r0.Status != 200 {
		fatal("signup: %d", r0.Status)
	}
	rt0 := s0.RefreshToken
	fmt.Printf("\n  rt0=%s\n", tokID(rt0))

	// step 1
	s1, rr1 := refresh(ctx, env, env.PublishableKey, rt0, "refresh #1: rt0 -> rt1")
	if rr1.Status != 200 {
		fatal("refresh1: %d", rr1.Status)
	}
	rt1 := s1.RefreshToken
	fmt.Printf("  rt1=%s\n", tokID(rt1))

	fmt.Println("\n  sleeping 12 s so the next redemption is past GoTrue's 10 s reuse interval …")
	time.Sleep(12 * time.Second)

	// step 2
	s2, rr2 := refresh(ctx, env, env.PublishableKey, rt1, "refresh #2: rt1 -> rt2 (rt1 is now one behind)")
	if rr2.Status != 200 {
		fatal("refresh2: %d", rr2.Status)
	}
	rt2 := s2.RefreshToken
	fmt.Printf("  rt2=%s\n", tokID(rt2))

	fmt.Println("\n  sleeping 12 s …")
	time.Sleep(12 * time.Second)

	// ONE BEHIND: present rt1 while rt2 is active. The answer must carry rt2 — from the server.
	sOB, rOB := refresh(ctx, env, env.PublishableKey, rt1, "ONE BEHIND: present rt1 while rt2 is active")
	oneBehindOK := rOB.Status == 200 && sOB.RefreshToken == rt2
	fmt.Printf("  one-behind answer: HTTP %d, refresh_token returned = %s (rt2 = %s) => same=%v\n",
		rOB.Status, tokID(sOB.RefreshToken), tokID(rt2), sOB.RefreshToken == rt2)
	check("f-one-behind-answered-with-active-token-by-the-server", oneBehindOK,
		fmt.Sprintf("presenting the one-step-behind token rt1 returned HTTP %d and the response body's refresh_token is byte-identical to the ACTIVE token rt2 (sha256 prefix %s). This is read off the server's answer, not inferred from the client's state", rOB.Status, tokID(rt2)))

	// FAMILY SURVIVAL: a LATER refresh with the rotated (active) token must still succeed.
	fmt.Println("\n  sleeping 12 s …")
	time.Sleep(12 * time.Second)
	s3, rr3 := refresh(ctx, env, env.PublishableKey, rt2, "FAMILY SURVIVAL: refresh again with the ACTIVE rt2 after the one-behind redemption")
	familyOK := rr3.Status == 200 && s3.RefreshToken != "" && s3.RefreshToken != rt2
	rt3 := s3.RefreshToken
	fmt.Printf("  rt3=%s\n", tokID(rt3))
	check("f-family-survives-a-later-refresh", familyOK,
		fmt.Sprintf("after the one-behind redemption, a LATER refresh with the rotated token rt2 still answered HTTP %d and rotated on to a NEW token %s. The chain is alive, not merely un-erroring", rr3.Status, tokID(rt3)))

	// and the new token is genuinely usable server-side
	st, code := func() (int, string) {
		r := do(ctx, "use the post-one-behind access token against GoTrue /user", "GET", env.APIURL+"/auth/v1/user",
			map[string]string{"apikey": env.PublishableKey, "Authorization": "Bearer " + s3.AccessToken}, nil)
		return r.Status, ""
	}()
	check("f-post-recovery-token-is-accepted-by-the-server", st == 200,
		fmt.Sprintf("the access token obtained after the one-behind recovery is accepted by GoTrue /user with HTTP %d%s", st, code))

	// TWO BEHIND
	fmt.Println("\n  sleeping 12 s …")
	time.Sleep(12 * time.Second)
	_, rTB := refresh(ctx, env, env.PublishableKey, rt0, "TWO+ BEHIND: present the original rt0 (now 3 steps behind)")
	tbCode := authErrorCode(rTB.Body)
	fmt.Printf("  two-behind answer: HTTP %d error_code=%q\n", rTB.Status, tbCode)

	// does that revoke the family? the plan says yes; check the ACTIVE token immediately after.
	time.Sleep(1 * time.Second)
	s4, rr4 := refresh(ctx, env, env.PublishableKey, rt3, "IMMEDIATELY AFTER the two-behind refusal: the ACTIVE token")
	revoked := rr4.Status != 200
	fmt.Printf("  active token after the two-behind refusal: HTTP %d (family revoked=%v)\n", rr4.Status, revoked)
	_ = s4
	check("f-two-behind-is-refused", rTB.Status != 200 && tbCode != "",
		fmt.Sprintf("a token two or more steps behind is refused: HTTP %d error_code=%q", rTB.Status, tbCode))
	if revoked {
		note("f-two-behind-revokes-family", fmt.Sprintf("the ACTIVE token was refused (HTTP %d) straight after the two-behind attempt — the family WAS revoked, matching the plan's text", rr4.Status))
	} else {
		note("f-two-behind-does-NOT-revoke-family", fmt.Sprintf("CONTRADICTS the plan's premise: after the two-behind refusal the ACTIVE token still answered HTTP 200. GoTrue v2.196.0 as configured here refuses the stale token WITHOUT revoking the family. The soak reported the same; it reproduces. The plan's 'a token two or more steps behind revokes the family' is stricter than the observed behaviour — safe to design against, but not what the server does"))
	}
}

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
