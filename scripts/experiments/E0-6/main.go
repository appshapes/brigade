package main

// E0-6: token-refresh coexistence and the session.json flock (plan 5.1, lines 538-549).
//
// Three OS processes share ONE profile directory and one session.json:
//
//	-mode=long    keeps a Realtime channel open, refreshes on the 90 s margin, pushes access_token
//	              on the channel after each refresh, drains its inbox, and probes an authenticated
//	              RPC continuously.
//	-mode=short   the CLI case: started fresh every 20 s, loads session.json, makes ONE RPC, exits.
//	-mode=inmem   the sandbox case: the profile directory is denied to it by sandbox-exec, so it
//	              refreshes IN MEMORY, uses the new access token and never persists.
//	-mode=victim  a short-lived writer that parks inside the critical section on purpose, so the
//	              (d) kill can be shown to have landed there.
//
// -mode=orchestrate (the default) builds the fixture through the real RPCs, spawns the three,
// drives the (d) kill and the (e) stack restart, probes for torn reads throughout, and prints the
// PASS/FAIL report from the shared event log.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------------------------------------------------------------- reporting (E0-1/E0-2 house style)

type resultRow struct {
	ID       string
	Pass     bool
	Record   bool
	Evidence string
}

var (
	results  []resultRow
	resultMu sync.Mutex
)

func check(id string, pass bool, evidence string) {
	resultMu.Lock()
	results = append(results, resultRow{id, pass, false, evidence})
	resultMu.Unlock()
	st := "PASS"
	if !pass {
		st = "FAIL"
	}
	fmt.Printf("[%s] %s — %s\n", st, id, redact(evidence))
}

func record(id, evidence string) {
	resultMu.Lock()
	results = append(results, resultRow{id, true, true, evidence})
	resultMu.Unlock()
	fmt.Printf("[RECORD] %s — %s\n", id, redact(evidence))
}

func summarise() int {
	section("summary")
	failed := 0
	for _, r := range results {
		if !r.Pass {
			failed++
			fmt.Printf("FAILED: %s — %s\n", r.ID, redact(r.Evidence))
		}
	}
	asserts := 0
	for _, r := range results {
		if !r.Record {
			asserts++
		}
	}
	fmt.Printf("%d assertions (%d recorded observations), %d failed\n", asserts, len(results)-asserts, failed)
	return failed
}

// ---------------------------------------------------------------- flags

var (
	mode        = "orchestrate"
	workDir     = "/Users/rjae/Development/appshapes/brigade/.ignored/exp/E0-6/run"
	envPath     = "/Users/rjae/Development/appshapes/brigade/.env.test"
	dur         = 30 * time.Minute
	selfPath    string
	margin      = refreshMarginDefault
	inmemMargin = 150 * time.Second
	holdMS      = 0
	fixturePath string
	killAtFrac  = 0.30
	restartFrac = 0.72
	doRestart   = true
	probeEvery  = 2 * time.Second
	tickEvery   = 1 * time.Second
)

// fixture is what the orchestrator hands the children.
type fixture struct {
	TeamID     string `json:"team_id"`
	RecipSID   string `json:"recip_sid"`
	SenderSID  string `json:"sender_sid"`
	ProfileDir string `json:"profile_dir"`
	EventsPath string `json:"events_path"`
	UID        string `json:"uid"`
}

func loadFixture(path string) fixture {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		fatal("parse fixture: %v", err)
	}
	return f
}

func main() {
	flag.StringVar(&mode, "mode", mode, "orchestrate | long | short | inmem | victim")
	flag.StringVar(&workDir, "work", workDir, "run directory (profile, event log, child logs)")
	flag.StringVar(&envPath, "env", envPath, "path to .env.test")
	flag.DurationVar(&dur, "dur", dur, "soak duration")
	flag.DurationVar(&margin, "margin", margin, "refresh margin for the writers")
	flag.DurationVar(&inmemMargin, "inmem-margin", inmemMargin, "refresh margin for the sandboxed in-memory process")
	flag.IntVar(&holdMS, "hold-ms", holdMS, "victim: ms to park inside the critical section after the refresh answer")
	flag.StringVar(&fixturePath, "fixture", "", "path to fixture.json (children)")
	flag.BoolVar(&doRestart, "restart", doRestart, "run the stack stop/start milestone")
	flag.Float64Var(&killAtFrac, "kill-at", killAtFrac, "fraction of the soak at which the (d) kill runs")
	flag.Float64Var(&restartFrac, "restart-at", restartFrac, "fraction of the soak at which the (e) restart runs")
	flag.DurationVar(&probeEvery, "probe-every", probeEvery, "liveness RPC interval")
	flag.Parse()

	selfPath, _ = os.Executable()
	env := loadEnv(envPath)

	switch mode {
	case "orchestrate":
		os.Exit(orchestrate(env))
	case "long":
		runLong(env)
	case "short":
		runShort(env)
	case "inmem":
		runInMem(env)
	case "victim":
		runVictim(env)
	default:
		fatal("unknown -mode %q", mode)
	}
}

// ---------------------------------------------------------------- shared child plumbing

func childCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; cancel() }()
	return ctx, cancel
}

// probe is the (b) evidence: one authenticated RPC that a PostgREST with an expired JWT refuses with
// PGRST303. Inspecting `exp` would only prove what the client believes; this proves what the server did.
func probe(ctx context.Context, env Env, jwt, teamID string) (int, string) {
	r := rpcQuiet(ctx, env, env.PublishableKey, jwt, "list_members", map[string]any{"p_team_id": teamID})
	if r.Status == 200 {
		return 200, ""
	}
	e, ok := parsePgError(r.Body)
	if ok {
		return r.Status, e.Code
	}
	return r.Status, strings.TrimSpace(truncate(string(r.Body), 120))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------- (long) the long-lived process

func runLong(env Env) {
	fx := loadFixture(fixturePath)
	p := profile{dir: fx.ProfileDir}
	el := openEventLog(fx.EventsPath, "long", false)
	defer el.close()
	ctx, cancel := childCtx()
	defer cancel()

	rr := readSession(p.sessionPath())
	if !rr.OK {
		el.emit("fatal", map[string]any{"why": "initial read", "parse_err": rr.ParseErr, "missing": rr.MissingList})
		fatal("long: initial session.json unreadable")
	}
	cur := rr.S
	el.emit("start", map[string]any{"rt": tokID(cur.RefreshToken), "at": tokID(cur.AccessToken), "remaining_s": int(remaining(cur).Seconds())})

	topic := "realtime:brigade:session:" + fx.RecipSID

	// ---- the channel
	var (
		connMu   sync.Mutex
		phx      *Phx
		joinRef  string
		bcast    = make(chan Msg, 256)
		lastAT   = tokID(cur.AccessToken)
		chanUp   bool
		joinFail int
	)

	pump := func(p *Phx, stop chan struct{}) {
		for {
			select {
			case <-stop:
				return
			case m, ok := <-p.in:
				if !ok {
					connMu.Lock()
					chanUp = false
					connMu.Unlock()
					el.emit("channel_down", map[string]any{})
					return
				}
				if m.Event == "broadcast" {
					select {
					case bcast <- m:
					default:
					}
				}
				if m.Event == "phx_close" || m.Event == "phx_error" {
					el.emit("channel_event", map[string]any{"event": m.Event, "payload": truncate(string(m.Payload), 200)})
				}
			}
		}
	}

	var stop chan struct{}
	connect := func(reason string) bool {
		connMu.Lock()
		if stop != nil {
			close(stop)
			stop = nil
		}
		if phx != nil {
			phx.close()
			phx = nil
		}
		chanUp = false
		connMu.Unlock()
		backoff := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
		for i := 0; i < 60; i++ {
			if ctx.Err() != nil {
				return false
			}
			np, err := dial(ctx, env, env.PublishableKey, "1.0.0", "long")
			if err == nil {
				ref, rep, ok := np.join(ctx, topic, JoinConfig{Private: true, AccessToken: cur.AccessToken}, 20*time.Second)
				st, _ := jsonGet[string](rep.Payload, "status")
				if ok && st == "ok" {
					sc := make(chan struct{})
					connMu.Lock()
					phx, joinRef, stop, chanUp = np, ref, sc, true
					connMu.Unlock()
					go pump(np, sc)
					el.emit("channel_join", map[string]any{"reason": reason, "attempt": i, "topic": topic})
					return true
				}
				joinFail++
				el.emit("channel_join_refused", map[string]any{"reason": reason, "attempt": i, "status": st,
					"payload": truncate(string(rep.Payload), 200)})
				np.close()
			} else {
				el.emit("channel_dial_failed", map[string]any{"reason": reason, "attempt": i, "err": err.Error()})
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(backoff[min(i, len(backoff)-1)]):
			}
		}
		return false
	}
	connect("initial")

	pushAccessToken := func(jwt string) {
		connMu.Lock()
		pp, jr, up := phx, joinRef, chanUp
		connMu.Unlock()
		if pp == nil || !up {
			el.emit("access_token_push", map[string]any{"ok": false, "why": "no channel"})
			return
		}
		_, err := pp.push(ctx, &jr, topic, "access_token", map[string]any{"access_token": jwt})
		el.emit("access_token_push", map[string]any{"ok": err == nil, "err": errStr(err), "at": tokID(jwt)})
	}

	// ---- the drain (plan 5.7): fetch_inbox then ack_messages
	drainSeen := map[string]bool{}
	pausePath := filepath.Join(filepath.Dir(fx.EventsPath), "drain.paused")
	drain := func(note string) {
		if _, err := os.Stat(pausePath); err == nil {
			return // the orchestrator is deliberately letting the inbox fill up across the outage
		}
		r := rpcQuiet(ctx, env, env.PublishableKey, cur.AccessToken, "fetch_inbox",
			map[string]any{"p_session_id": fx.RecipSID, "p_limit": 100})
		if r.Status != 200 {
			e, _ := parsePgError(r.Body)
			el.emit("drain_error", map[string]any{"note": note, "status": r.Status, "code": e.Code})
			return
		}
		var envs []map[string]any
		_ = json.Unmarshal(r.Body, &envs)
		if len(envs) == 0 {
			return
		}
		ids := make([]string, 0, len(envs))
		fresh := 0
		for _, e := range envs {
			id, _ := e["message_id"].(string)
			if id == "" {
				continue
			}
			ids = append(ids, id)
			if !drainSeen[id] {
				drainSeen[id] = true
				fresh++
			}
		}
		ar := rpcQuiet(ctx, env, env.PublishableKey, cur.AccessToken, "ack_messages",
			map[string]any{"p_session_id": fx.RecipSID, "p_message_ids": ids})
		el.emit("drain", map[string]any{"note": note, "n": len(ids), "fresh": fresh, "ack_status": ar.Status,
			"ids": strings.Join(ids, ",")})
	}

	// ---- tickers
	tick := time.NewTicker(tickEvery)
	probeT := time.NewTicker(probeEvery)
	hbT := time.NewTicker(25 * time.Second)
	sessHbT := time.NewTicker(30 * time.Second)
	drainT := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	defer probeT.Stop()
	defer hbT.Stop()
	defer sessHbT.Stop()
	defer drainT.Stop()

	doRefresh := func(force bool, note string) {
		ns, out := ensureFresh(ctx, env, p, cur, freshOpts{Margin: margin, Persist: true, Force: force})
		el.emitRefresh(note, out)
		if out.Session.AccessToken != "" {
			cur = ns
		}
		if tokID(cur.AccessToken) != lastAT {
			lastAT = tokID(cur.AccessToken)
			pushAccessToken(cur.AccessToken)
			// the channel must survive the refresh and the push: check it a moment later
			time.Sleep(1500 * time.Millisecond)
			connMu.Lock()
			up := chanUp
			connMu.Unlock()
			el.emit("post_refresh_channel", map[string]any{"up": up, "at": lastAT})
		}
	}

	deadline := time.Now().Add(dur + 90*time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			el.emit("stop", map[string]any{"why": "signal"})
			return
		case <-tick.C:
			if remaining(cur) < margin {
				doRefresh(false, "margin")
			}
		case <-probeT.C:
			st, code := probe(ctx, env, cur.AccessToken, fx.TeamID)
			el.emit("probe", map[string]any{"status": st, "code": code,
				"remaining_s": int(remaining(cur).Seconds()), "at": tokID(cur.AccessToken)})
			if code == "PGRST303" {
				el.emit("pgrst303", map[string]any{})
				doRefresh(true, "pgrst303")
			}
		case <-hbT.C:
			connMu.Lock()
			pp, up := phx, chanUp
			connMu.Unlock()
			if pp != nil && up {
				_, err := pp.push(ctx, nil, "phoenix", "heartbeat", map[string]any{})
				if err != nil {
					el.emit("heartbeat_err", map[string]any{"err": err.Error()})
				}
			} else if !up {
				el.emit("reconnecting", map[string]any{})
				connect("channel down")
			}
		case <-sessHbT.C:
			r := rpcQuiet(ctx, env, env.PublishableKey, cur.AccessToken, "session_heartbeat",
				map[string]any{"p_session_id": fx.RecipSID, "p_activity": "idle"})
			if r.Status != 200 {
				e, _ := parsePgError(r.Body)
				el.emit("session_heartbeat_err", map[string]any{"status": r.Status, "code": e.Code})
			}
		case m := <-bcast:
			id, _ := jsonGet[string](m.Payload, "payload", "message_id")
			el.emit("broadcast", map[string]any{"message_id": id})
			drain("broadcast")
		case <-drainT.C:
			drain("timer")
		}
	}
	el.emit("stop", map[string]any{"why": "deadline"})
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------- (short) the CLI case

func runShort(env Env) {
	fx := loadFixture(fixturePath)
	p := profile{dir: fx.ProfileDir}
	el := openEventLog(fx.EventsPath, "short", false)
	defer el.close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	rr := readSession(p.sessionPath())
	el.emit("read", map[string]any{"ok": rr.OK, "parse_err": rr.ParseErr, "missing": rr.MissingList,
		"mode": fmt.Sprintf("%04o", rr.Mode), "stat_err": rr.StatErr, "read_err": rr.ReadErr,
		"bytes": len(rr.Raw)})
	if !rr.OK {
		el.emit("exit", map[string]any{"code": 4, "why": "unparsable session.json"})
		os.Exit(4)
	}
	cur, out := ensureFresh(ctx, env, p, rr.S, freshOpts{Margin: margin, Persist: true})
	el.emitRefresh("cli", out)

	st, code := probe(ctx, env, cur.AccessToken, fx.TeamID)
	el.emit("probe", map[string]any{"status": st, "code": code,
		"remaining_s": int(remaining(cur).Seconds()), "at": tokID(cur.AccessToken)})
	if code == "PGRST303" {
		el.emit("pgrst303", map[string]any{})
		cur, out = ensureFresh(ctx, env, p, cur, freshOpts{Margin: margin, Persist: true, Force: true})
		el.emitRefresh("cli-pgrst303", out)
		st, code = probe(ctx, env, cur.AccessToken, fx.TeamID)
		el.emit("probe", map[string]any{"status": st, "code": code, "retry": true, "at": tokID(cur.AccessToken)})
	}
	el.emit("exit", map[string]any{"code": 0, "probe_status": st})
}

// ---------------------------------------------------------------- (inmem) the sandbox case

func runInMem(env Env) {
	fx := loadFixture(fixturePath)
	p := profile{dir: fx.ProfileDir}
	el := openEventLog(fx.EventsPath, "inmem", false)
	defer el.close()
	ctx, cancel := childCtx()
	defer cancel()

	// Prove the sandbox is real before anything else: a write into the profile directory must fail.
	probeFile := filepath.Join(p.dir, ".sandbox-write-probe")
	werr := os.WriteFile(probeFile, []byte("x"), 0o600)
	el.emit("sandbox_write_probe", map[string]any{"denied": werr != nil, "err": errStr(werr)})
	if werr == nil {
		_ = os.Remove(probeFile)
	}
	// And that the atomic write's first step is the one that fails, i.e. the read-only fallback triggers.
	if tf, terr := os.CreateTemp(p.dir, ".session-*.tmp"); terr == nil {
		el.emit("sandbox_createtemp_probe", map[string]any{"denied": false})
		tf.Close()
		os.Remove(tf.Name())
	} else {
		el.emit("sandbox_createtemp_probe", map[string]any{"denied": true, "err": terr.Error()})
	}
	// flock on a read-only fd must still work, or the sandboxed process cannot honour the protocol.
	if lk, lerr := acquireLock(p.lockPath(), true); lerr == nil {
		el.emit("sandbox_flock_probe", map[string]any{"ok": true, "wait_us": lk.Waited.Microseconds()})
		lk.release()
	} else {
		el.emit("sandbox_flock_probe", map[string]any{"ok": false, "err": lerr.Error()})
	}

	rr := readSession(p.sessionPath())
	if !rr.OK {
		el.emit("fatal", map[string]any{"why": "initial read"})
		os.Exit(4)
	}
	var mem Session
	el.emit("start", map[string]any{"rt": tokID(rr.S.RefreshToken), "remaining_s": int(remaining(rr.S).Seconds())})

	tick := time.NewTicker(3 * time.Second)
	probeT := time.NewTicker(probeEvery)
	defer tick.Stop()
	defer probeT.Stop()

	// current returns the token this process would actually use for a command.
	current := func() Session {
		if mem.AccessToken != "" && remaining(mem) > 0 {
			return mem
		}
		return readSession(p.sessionPath()).S
	}

	deadline := time.Now().Add(dur + 90*time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			el.emit("stop", map[string]any{"why": "signal"})
			return
		case <-tick.C:
			var memp *Session
			if mem.AccessToken != "" {
				m := mem
				memp = &m
			}
			ns, out := ensureFresh(ctx, env, p, current(), freshOpts{Margin: inmemMargin, Persist: false, Mem: memp})
			el.emitRefresh("inmem", out) // every acquisition, so the (c) lock-wait distribution is complete
			if out.Did && out.Status == 200 {
				mem = ns
			}
		case <-probeT.C:
			c := current()
			st, code := probe(ctx, env, c.AccessToken, fx.TeamID)
			el.emit("probe", map[string]any{"status": st, "code": code,
				"remaining_s": int(remaining(c).Seconds()), "at": tokID(c.AccessToken),
				"in_memory": mem.AccessToken != "" && tokID(mem.AccessToken) == tokID(c.AccessToken)})
			if code == "PGRST303" {
				el.emit("pgrst303", map[string]any{})
				var memp *Session
				if mem.AccessToken != "" {
					m := mem
					memp = &m
				}
				ns, out := ensureFresh(ctx, env, p, c, freshOpts{Margin: inmemMargin, Persist: false, Force: true, Mem: memp})
				el.emitRefresh("inmem-pgrst303", out)
				if out.Did && out.Status == 200 {
					mem = ns
				}
			}
		}
	}
	el.emit("stop", map[string]any{"why": "deadline"})
}

// ---------------------------------------------------------------- (victim) the (d) kill target

// runVictim takes the lock, refreshes, then parks inside the critical section between the refresh
// answer and the atomic write — the worst moment to die, because the rotated token is not yet on
// disk. It announces each step through the marker file so the killer knows exactly where it is.
func runVictim(env Env) {
	fx := loadFixture(fixturePath)
	p := profile{dir: fx.ProfileDir}
	el := openEventLog(fx.EventsPath, "victim", false)
	defer el.close()
	ctx := context.Background()
	marker := filepath.Join(filepath.Dir(fx.EventsPath), "victim.state")
	setState := func(s string) {
		_ = os.WriteFile(marker, []byte(fmt.Sprintf("%s %d %s", s, os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))), 0o644)
	}
	setState("starting")

	lk, err := acquireLock(p.lockPath(), false)
	if err != nil {
		el.emit("victim_lock_err", map[string]any{"err": err.Error()})
		setState("lock-failed")
		os.Exit(1)
	}
	el.emit("victim_locked", map[string]any{"wait_us": lk.Waited.Microseconds(), "tries": lk.Attempts})
	setState("locked")

	rr := readSession(p.sessionPath())
	el.emit("victim_read", map[string]any{"ok": rr.OK, "rt": tokID(rr.S.RefreshToken)})
	t0 := time.Now()
	ns, r := refreshQuiet(ctx, env, env.PublishableKey, rr.S.RefreshToken)
	el.emit("victim_refresh", map[string]any{"status": r.Status, "sent_rt": tokID(rr.S.RefreshToken),
		"got_rt": tokID(ns.RefreshToken), "elapsed_ms": time.Since(t0).Milliseconds(),
		"error_code": authErrorCode(r.Body)})
	// The token is now rotated SERVER-SIDE and not yet on disk. Still holding the lock.
	setState("refreshed-holding-lock")
	el.emit("victim_holding", map[string]any{"hold_ms": holdMS})
	time.Sleep(time.Duration(holdMS) * time.Millisecond)

	// Only reached if the kill missed.
	if r.Status == 200 {
		werr := writeSession(p.sessionPath(), ns, r.Body)
		el.emit("victim_persisted", map[string]any{"err": errStr(werr)})
	}
	setState("released")
	lk.release()
	el.emit("victim_exit", map[string]any{"killed": false})
}

// ---------------------------------------------------------------- orchestrator

func orchestrate(env Env) int {
	ctx, cancel := context.WithTimeout(context.Background(), dur+15*time.Minute)
	defer cancel()

	runStart := time.Now()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		fatal("mkdir work: %v", err)
	}
	profDir := filepath.Join(workDir, "profiles", "default")
	if err := os.MkdirAll(profDir, 0o700); err != nil {
		fatal("mkdir profile: %v", err)
	}
	if err := os.Chmod(profDir, 0o700); err != nil {
		fatal("chmod profile: %v", err)
	}
	logsDir := filepath.Join(workDir, "logs")
	_ = os.MkdirAll(logsDir, 0o755)
	eventsPath := filepath.Join(workDir, "events.ndjson")
	_ = os.Remove(eventsPath)
	el := openEventLog(eventsPath, "orch", true)
	defer el.close()
	p := profile{dir: profDir}

	section("fixture (built through the real RPCs)")

	// principal A owns the shared profile; it is the one whose session.json the three processes share.
	a, ar := signUpAnonymous(ctx, env, env.PublishableKey, "sign up principal A (the shared profile)", nil, nil)
	if ar.Status != 200 {
		fatal("sign-up A failed: %d", ar.Status)
	}
	if err := writeSession(p.sessionPath(), a, ar.Body); err != nil {
		fatal("write session.json: %v", err)
	}
	// create the sidecar up front so the sandboxed reader can open it O_RDONLY
	if lk, err := acquireLock(p.lockPath(), false); err == nil {
		lk.release()
	} else {
		fatal("create sidecar: %v", err)
	}
	fmt.Printf("session.json written: %s\n", p.sessionPath())

	tr := rpc(ctx, env, env.PublishableKey, a.AccessToken, "create_team",
		map[string]any{"p_name": "e06-" + randHex(4), "p_human_label": "e06-long"}, "both", "create_team (principal A)")
	if tr.Status != 200 {
		fatal("create_team failed: %d", tr.Status)
	}
	teamID, _ := jsonGet[string](tr.Body, "team_id")
	secret, _ := jsonGet[string](tr.Body, "join_secret")

	rs := rpc(ctx, env, env.PublishableKey, a.AccessToken, "register_session",
		map[string]any{"p_team_id": teamID, "p_name": "e06-recipient", "p_activity": "idle", "p_lease_seconds": 120},
		"both", "register_session (the long-lived process's session)")
	if rs.Status != 200 {
		fatal("register_session recipient failed: %d", rs.Status)
	}
	recipSID, _ := jsonGet[string](rs.Body, "session_id")

	// principal B is the orchestrator's own sender; it never touches session.json.
	b, br := signUpAnonymous(ctx, env, env.PublishableKey, "sign up principal B (the sender; separate credential)", nil, nil)
	if br.Status != 200 {
		fatal("sign-up B failed: %d", br.Status)
	}
	jr := rpc(ctx, env, env.PublishableKey, b.AccessToken, "join_team",
		map[string]any{"p_join_secret": secret, "p_human_label": "e06-sender"}, "both", "join_team (principal B)")
	if st, _ := jsonGet[string](jr.Body, "status"); st != "joined" {
		fatal("join_team status=%q", st)
	}
	rs2 := rpc(ctx, env, env.PublishableKey, b.AccessToken, "register_session",
		map[string]any{"p_team_id": teamID, "p_name": "e06-sender", "p_activity": "idle", "p_lease_seconds": 600},
		"both", "register_session (the sender's session)")
	senderSID, _ := jsonGet[string](rs2.Body, "session_id")

	fx := fixture{TeamID: teamID, RecipSID: recipSID, SenderSID: senderSID, ProfileDir: profDir,
		EventsPath: eventsPath, UID: a.User.ID}
	fxb, _ := json.MarshalIndent(fx, "", "  ")
	fixturePath = filepath.Join(workDir, "fixture.json")
	if err := os.WriteFile(fixturePath, fxb, 0o644); err != nil {
		fatal("write fixture: %v", err)
	}

	// senderTok keeps principal B's own token fresh (its own private credential, no file, no lock).
	bTok := b
	sendMu := sync.Mutex{}
	refreshB := func() {
		if remaining(bTok) > margin {
			return
		}
		ns, r := refreshQuiet(ctx, env, env.PublishableKey, bTok.RefreshToken)
		if r.Status == 200 {
			bTok = ns
			el.emit("sender_refresh", map[string]any{"ok": true})
		} else {
			el.emit("sender_refresh", map[string]any{"ok": false, "status": r.Status, "code": authErrorCode(r.Body)})
		}
	}

	sentIDs := []string{}
	sendOne := func(note string) (string, string) {
		sendMu.Lock()
		defer sendMu.Unlock()
		refreshB()
		key := fmt.Sprintf("e06-%s-%d", note, time.Now().UnixNano())
		r := rpcQuiet(ctx, env, env.PublishableKey, bTok.AccessToken, "send_message",
			map[string]any{"p_sender_session_id": senderSID, "p_recipient_session_id": recipSID,
				"p_body": "e06 " + note, "p_idempotency_key": key})
		if r.Status != 200 {
			e, _ := parsePgError(r.Body)
			el.emit("send", map[string]any{"note": note, "ok": false, "status": r.Status, "code": e.Code,
				"msg": truncate(e.Message, 120)})
			return "", e.Code
		}
		id, _ := jsonGet[string](r.Body, "message_id")
		sentIDs = append(sentIDs, id)
		el.emit("send", map[string]any{"note": note, "ok": true, "message_id": id})
		return id, ""
	}

	// ---------------- build the binary the children run
	binPath := filepath.Join(workDir, "e06")
	bc := exec.Command("go", "build", "-o", binPath, ".")
	bc.Dir = filepath.Dir(workDir)
	if out, err := bc.CombinedOutput(); err != nil {
		fatal("build child binary: %v\n%s", err, out)
	}

	spawn := func(name string, sandboxed bool, extra ...string) *exec.Cmd {
		args := []string{"-mode=" + name, "-fixture=" + fixturePath, "-env=" + envPath,
			"-dur=" + dur.String(), "-margin=" + margin.String(), "-inmem-margin=" + inmemMargin.String(),
			"-probe-every=" + probeEvery.String()}
		args = append(args, extra...)
		var c *exec.Cmd
		if sandboxed {
			// The real thing, not a simulation: sandbox-exec denies file-write* under the profile
			// directory for this process only, exactly as the Bash sandbox denies ~/.config.
			prof := fmt.Sprintf(`(version 1)(allow default)(deny file-write* (subpath %q))`, profDir)
			c = exec.Command("sandbox-exec", append([]string{"-p", prof, binPath}, args...)...)
		} else {
			c = exec.Command(binPath, args...)
		}
		lf, err := os.Create(filepath.Join(logsDir, fmt.Sprintf("%s-%d.log", name, time.Now().UnixNano()%1e7)))
		if err == nil {
			c.Stdout, c.Stderr = lf, lf
		}
		if err := c.Start(); err != nil {
			fatal("spawn %s: %v", name, err)
		}
		el.emit("spawn", map[string]any{"proc": name, "pid": c.Process.Pid, "sandboxed": sandboxed})
		return c
	}

	section("processes")
	longC := spawn("long", false)
	inmemC := spawn("inmem", true)
	defer func() {
		for _, c := range []*exec.Cmd{longC, inmemC} {
			if c != nil && c.Process != nil {
				_ = c.Process.Signal(syscall.SIGTERM)
			}
		}
	}()

	// ---------------- the (c) tearing prober: read session.json without the lock, as fast as it will go
	type tearStats struct {
		mu        sync.Mutex
		reads     int
		parseErr  int
		missing   int
		modeBad   int
		statErr   int
		readErr   int
		examples  []string
		modesSeen map[string]int
		distinct  map[string]int
	}
	ts := &tearStats{modesSeen: map[string]int{}, distinct: map[string]int{}}
	tearStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-tearStop:
				return
			default:
			}
			rr := readSession(p.sessionPath())
			ts.mu.Lock()
			ts.reads++
			if rr.ParseErr != "" {
				ts.parseErr++
				if len(ts.examples) < 5 {
					ts.examples = append(ts.examples, "parse: "+truncate(rr.ParseErr, 100)+" bytes="+truncate(string(rr.Raw), 80))
				}
			} else if len(rr.MissingList) > 0 {
				ts.missing++
				if len(ts.examples) < 5 {
					ts.examples = append(ts.examples, "missing: "+strings.Join(rr.MissingList, ","))
				}
			}
			if rr.ReadErr != "" {
				ts.readErr++
			}
			if rr.StatErr != "" {
				ts.statErr++
			} else {
				m := fmt.Sprintf("%04o", rr.Mode)
				ts.modesSeen[m]++
				if rr.Mode != 0o600 {
					ts.modeBad++
				}
			}
			if rr.S.RefreshToken != "" {
				ts.distinct[tokID(rr.S.RefreshToken)]++
			}
			ts.mu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// also watch the profile DIRECTORY mode and for stray temp files left behind
	dirStop := make(chan struct{})
	var dirModeBad, strayTmp int
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-dirStop:
				return
			case <-t.C:
				if fi, err := os.Stat(profDir); err == nil && fi.Mode().Perm() != 0o700 {
					dirModeBad++
				}
				ents, _ := os.ReadDir(profDir)
				for _, e := range ents {
					if strings.HasSuffix(e.Name(), ".tmp") {
						strayTmp++
					}
				}
			}
		}
	}()

	// ---------------- the (c) lock-contention prober: take and release the sidecar lock steadily, so the
	// wait distribution is measured against thousands of acquisitions instead of the handful the
	// refreshers happen to make, and so a refresh that holds the lock across an HTTP round trip is
	// actually raced rather than assumed never to be.
	lockStop := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-lockStop:
				return
			case <-t.C:
				lk, err := acquireLock(p.lockPath(), false)
				d := map[string]any{"wait_us": int64(0), "tries": 0}
				if lk != nil {
					d["wait_us"] = lk.Waited.Microseconds()
					d["tries"] = lk.Attempts
				}
				if err != nil {
					d["err"] = err.Error()
				} else {
					time.Sleep(time.Millisecond) // hold it briefly, the way a read-and-decide would
					lk.release()
				}
				el.emit("lockprobe", d)
			}
		}
	}()

	// ---------------- the short-lived process, every 20 s
	shortStop := make(chan struct{})
	var shortRuns int
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-shortStop:
				return
			case <-t.C:
				c := spawn("short", false)
				shortRuns++
				go func() { _ = c.Wait() }()
			}
		}
	}()

	// ---------------- milestones
	killAt := time.Duration(float64(dur) * killAtFrac)
	restartAt := time.Duration(float64(dur) * restartFrac)

	var (
		killLandedInside   bool
		killOtherLockedOut = true
		killEvidence       string
		restartRejoined    bool
		restartDrained     bool
		restartEvidence    string
		survivedRefresh    bool
	)

	// ---- (d)
	runKill := func() {
		section("(d) kill one process MID-REFRESH, inside the critical section")
		marker := filepath.Join(workDir, "victim.state")
		_ = os.Remove(marker)
		v := spawn("victim", false, "-hold-ms=6000")
		state := func() string {
			b, err := os.ReadFile(marker)
			if err != nil {
				return ""
			}
			return string(b)
		}
		// wait for the victim to be inside the critical section, past the refresh answer
		got := false
		for i := 0; i < 600; i++ {
			if strings.HasPrefix(state(), "refreshed-holding-lock") {
				got = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !got {
			killEvidence = "the victim never reported reaching the critical section: state=" + state()
			_ = v.Process.Kill()
			return
		}
		// PROOF the lock is held right now, from a third process's point of view
		heldBefore, h, herr := tryLockNB(p.lockPath())
		if h != nil {
			h.release()
		}
		tKill := time.Now()
		_ = v.Process.Signal(syscall.SIGKILL)
		_ = v.Wait()
		// PROOF the kernel released it on death: LOCK_NB now succeeds, and how fast
		var freeAfter time.Duration
		gotFree := false
		for i := 0; i < 2000; i++ {
			held, h2, err := tryLockNB(p.lockPath())
			if err == nil && !held && h2 != nil {
				freeAfter = time.Since(tKill)
				gotFree = true
				h2.release()
				break
			}
			if h2 != nil {
				h2.release()
			}
			time.Sleep(time.Millisecond)
		}
		killLandedInside = heldBefore && got
		killOtherLockedOut = !gotFree
		survived := state()
		survivedTag := survived
		if f := strings.Fields(survived); len(f) > 0 {
			survivedTag = f[0]
		}
		killEvidence = fmt.Sprintf("victim pid=%d reached %q; a third process's LOCK_NB before the kill: held=%v (err=%v); SIGKILL; LOCK_NB free again after %v (succeeded=%v); the victim never wrote %q, so it died between the refresh answer and the atomic write",
			v.Process.Pid, survivedTag, heldBefore, herr, freeAfter.Round(time.Microsecond), gotFree, "released")
		el.emit("kill", map[string]any{"held_before": heldBefore, "free_after_us": freeAfter.Microseconds(),
			"got_free": gotFree, "state": survived})
	}

	// ---- (e)
	runRestart := func() {
		section("(e) stack stop/start: the channel must rejoin and the inbox must drain")
		// Pause the long-lived process's drain so the messages sent now are still waiting when the
		// stack comes back; otherwise it drains them within 3 s and the recovery proves nothing.
		pausePath := filepath.Join(workDir, "drain.paused")
		if err := os.WriteFile(pausePath, []byte("1"), 0o644); err != nil {
			el.emit("drain_pause_err", map[string]any{"err": err.Error()})
		}
		time.Sleep(4 * time.Second)
		pending := []string{}
		for i := 0; i < 3; i++ {
			id, _ := sendOne(fmt.Sprintf("pre-restart-%d", i))
			if id != "" {
				pending = append(pending, id)
			}
			time.Sleep(500 * time.Millisecond)
		}
		names := []string{"supabase_rest_brigade", "supabase_realtime_brigade", "supabase_auth_brigade", "supabase_kong_brigade"}
		run := func(args ...string) (string, error) {
			c := exec.Command("docker", args...)
			out, err := c.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		t0 := time.Now()
		o1, e1 := run(append([]string{"stop"}, names...)...)
		o2, e2 := run("stop", "supabase_db_brigade")
		el.emit("stack_stop", map[string]any{"err1": errStr(e1), "err2": errStr(e2),
			"out": truncate(o1+" "+o2, 200), "took_s": int(time.Since(t0).Seconds())})
		time.Sleep(10 * time.Second)
		t1 := time.Now()
		o3, e3 := run("start", "supabase_db_brigade")
		time.Sleep(8 * time.Second)
		o4, e4 := run(append([]string{"start"}, names...)...)
		el.emit("stack_start", map[string]any{"err1": errStr(e3), "err2": errStr(e4), "out": truncate(o3+" "+o4, 200)})
		up := false
		for i := 0; i < 150; i++ {
			r := doQuiet(ctx, "GET", env.APIURL+"/auth/v1/health", map[string]string{"apikey": env.PublishableKey}, nil)
			if r.Status == 200 {
				up = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		upTook := time.Since(t1)
		outage := time.Since(t0)
		el.emit("stack_up", map[string]any{"up": up, "took_s": int(upTook.Seconds()), "outage_s": int(outage.Seconds())})

		// everything from here has to be the long-lived process recovering on its own
		joinCut := time.Now()
		_ = os.Remove(pausePath)
		time.Sleep(5 * time.Second)
		newID, code := sendOne("post-restart")
		if code != "" || newID == "" {
			time.Sleep(5 * time.Second)
			newID, _ = sendOne("post-restart-retry")
		}
		postID := newID
		if newID != "" {
			pending = append(pending, newID)
		}
		gotBroadcast := false
		deadline := time.Now().Add(180 * time.Second)
		for time.Now().Before(deadline) {
			evs, _ := readEvents(eventsPath)
			rejoin, drained := false, map[string]bool{}
			for _, e := range evs {
				if e.Proc != "long" || e.when().Before(joinCut) {
					continue
				}
				if e.Kind == "channel_join" {
					rejoin = true
				}
				if e.Kind == "broadcast" && postID != "" && e.str("message_id") == postID {
					gotBroadcast = true
				}
				if e.Kind == "drain" {
					for _, mid := range strings.Split(e.str("ids"), ",") {
						drained[mid] = true
					}
				}
			}
			all := len(pending) > 0
			for _, mid := range pending {
				if !drained[mid] {
					all = false
				}
			}
			restartRejoined = restartRejoined || rejoin
			if rejoin && all {
				restartDrained = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		restartEvidence = fmt.Sprintf("the stack was stopped and started with `docker stop`/`docker start` on the five supabase_*_brigade containers (the supabase CLI is not installed in this environment); the outage lasted %s and /auth/v1/health answered 200 again %s after the first container was started. %d messages were queued undrained across the outage (the drain was paused on purpose) plus one sent after it; the long-lived process rejoined %s by itself=%v, the post-restart message arrived on the rejoined channel as a broadcast=%v, and every queued message was drained through fetch_inbox/ack_messages=%v",
			outage.Round(time.Second), upTook.Round(time.Second), len(pending)-1,
			"realtime:brigade:session:"+recipSID, restartRejoined, gotBroadcast, restartDrained)
	}

	// ---------------- steady state
	section(fmt.Sprintf("soak: %s (jwt_expiry=300 s, writer margin=%s, sandboxed margin=%s)", dur, margin, inmemMargin))
	sendT := time.NewTicker(30 * time.Second)
	progT := time.NewTicker(60 * time.Second)
	defer sendT.Stop()
	defer progT.Stop()
	killDone, restartDone := false, false
	for {
		el2 := time.Since(runStart)
		if el2 >= dur {
			break
		}
		if !killDone && el2 >= killAt {
			killDone = true
			runKill()
			continue
		}
		if doRestart && !restartDone && el2 >= restartAt {
			restartDone = true
			runRestart()
			continue
		}
		select {
		case <-ctx.Done():
			check("context", false, "the orchestrator context expired before the soak finished")
			return 1
		case <-sendT.C:
			sendOne("steady")
		case <-progT.C:
			ts.mu.Lock()
			reads, pe, mi := ts.reads, ts.parseErr, ts.missing
			ts.mu.Unlock()
			evs, _ := readEvents(eventsPath)
			nRef, nAlready := 0, 0
			for _, e := range evs {
				if e.Kind == "refresh" && e.boolean("did") {
					nRef++
					if e.str("error_code") == "refresh_token_already_used" {
						nAlready++
					}
				}
			}
			fmt.Printf("T+%s  reads=%d parse_err=%d missing=%d  refreshes=%d already_used=%d  short_runs=%d\n",
				el2.Round(time.Second), reads, pe, mi, nRef, nAlready, shortRuns)
		case <-time.After(time.Second):
		}
	}

	// ---------------- shut down and collect
	close(shortStop)
	close(lockStop)
	close(tearStop)
	close(dirStop)
	for _, c := range []*exec.Cmd{longC, inmemC} {
		if c.Process != nil {
			_ = c.Process.Signal(syscall.SIGTERM)
		}
	}
	time.Sleep(3 * time.Second)
	for _, c := range []*exec.Cmd{longC, inmemC} {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	}
	el.emit("soak_end", map[string]any{"ran_s": int(time.Since(runStart).Seconds())})

	// ---------------- (f) boundary probe on a throwaway family (never the soak's own)
	section("(f-boundary) how much margin the design has: one-behind vs two-behind on a throwaway principal")
	{
		z, zr := signUpAnonymous(ctx, env, env.PublishableKey, "throwaway principal for the boundary probe", nil, nil)
		if zr.Status != 200 {
			record("f-boundary", "could not sign up a throwaway principal for the boundary probe")
		} else {
			rt0 := z.RefreshToken
			step := func(what, rt string) (Session, Resp) {
				s2, r := refreshQuiet(ctx, env, env.PublishableKey, rt)
				fmt.Printf("  %-34s sent=%s -> HTTP %d %s got=%s\n", what, tokID(rt), r.Status,
					authErrorCode(r.Body), tokID(s2.RefreshToken))
				return s2, r
			}
			s1, r1 := step("rt0 (fresh)", rt0)
			time.Sleep(12 * time.Second) // past GoTrue's 10 s reuse interval, so this is the one-behind rule
			s2, r2 := step("rt0 again, now ONE behind", rt0)
			s3, r3 := step("rt1 (the active one)", s1.RefreshToken)
			time.Sleep(12 * time.Second)
			_, r4 := step("rt0 again, now TWO behind", rt0)
			_, r5 := step("rt2 (active) after the 2-behind", s3.RefreshToken)
			record("f-boundary", fmt.Sprintf(
				"one step behind: HTTP %d, and the answer carried the ACTIVE refresh token (identical to the one the earlier refresh got: %v). two steps behind: HTTP %d code=%q. The active token presented straight afterwards answered HTTP %d code=%q, so on this build the two-behind attempt was REFUSED but did not take the family down with it. (r1=%d r3=%d)",
				r2.Status, tokID(s2.RefreshToken) == tokID(s1.RefreshToken), r4.Status, authErrorCode(r4.Body),
				r5.Status, authErrorCode(r5.Body), r1.Status, r3.Status))
		}
	}

	// ================================================================ report
	evs, badLines := readEvents(eventsPath)
	fmt.Printf("\nevent log: %s (%d events, %d unparsable lines)\n", eventsPath, len(evs), len(badLines))

	type refEv struct {
		e     event
		proc  string
		at    time.Time
		sent  string
		got   string
		code  string
		stat  int
		pers  bool
		note  string
		waitU float64
		tries float64
	}
	var refs []refEv
	var lockWaits []float64
	var lockTries []float64
	var lockErrs []string
	already := 0
	byOutcome := map[string]int{}
	skipped := 0
	for _, e := range evs {
		if e.Kind != "refresh" {
			continue
		}
		lockWaits = append(lockWaits, e.num("lock_wait_us"))
		lockTries = append(lockTries, e.num("lock_tries"))
		if le := e.str("lock_err"); le != "" {
			lockErrs = append(lockErrs, e.Proc+": "+le)
		}
		if e.boolean("skipped") {
			skipped++
			byOutcome["skipped (another process had a fresher token)"]++
			continue
		}
		if !e.boolean("did") {
			continue
		}
		code := e.str("error_code")
		st := int(e.num("status"))
		key := fmt.Sprintf("HTTP %d", st)
		if st <= 0 {
			key = "transport error (the backend was down for the (e) restart)"
		} else if code != "" {
			key += " " + code
		}
		byOutcome[key]++
		if code == "refresh_token_already_used" {
			already++
		}
		refs = append(refs, refEv{e: e, proc: e.Proc, at: e.when(), sent: e.str("sent_rt"), got: e.str("got_rt"),
			code: code, stat: st, pers: e.boolean("persisted"), note: e.str("note"),
			waitU: e.num("lock_wait_us"), tries: e.num("lock_tries")})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].at.Before(refs[j].at) })

	// ---- (a)
	section("(a) refresh_token_already_used over the whole soak")
	var outKeys []string
	for k := range byOutcome {
		outKeys = append(outKeys, k)
	}
	sort.Strings(outKeys)
	for _, k := range outKeys {
		fmt.Printf("  %-56s %d\n", k, byOutcome[k])
	}
	fmt.Printf("\n  every refresh, in order (rt ids are sha256 prefixes, never tokens):\n")
	for _, r := range refs {
		fmt.Printf("  %s  %-6s %-14s sent=%s got=%s HTTP %d %s persisted=%v lock_wait=%.1fms tries=%.0f\n",
			r.at.Format("15:04:05"), r.proc, r.note, r.sent, r.got, r.stat, r.code, r.pers, r.waitU/1000, r.tries)
	}
	aEvidence := fmt.Sprintf("%d refreshes actually issued across the three processes (%d skipped under the lock because another process had already stored a fresher token); outcomes: %v; refresh_token_already_used = %d",
		len(refs), skipped, byOutcome, already)
	check("a-no-already-used", already == 0, aEvidence)

	// ---- (b)
	section("(b) both processes always hold a valid token (server-side proof)")
	type probeStat struct {
		total, ok, expired, unauth, unavailable int
		codes                                   map[string]int
		worstRemaining                          int
	}
	ps := map[string]*probeStat{}
	for _, e := range evs {
		if e.Kind != "probe" {
			continue
		}
		s := ps[e.Proc]
		if s == nil {
			s = &probeStat{codes: map[string]int{}, worstRemaining: 1 << 30}
			ps[e.Proc] = s
		}
		s.total++
		st := int(e.num("status"))
		c := e.str("code")
		switch {
		case st == 200:
			s.ok++
		case st < 0 || st == 502 || st == 503 || st == 504:
			// the backend was not there at all — the (e) outage, not a credential problem
			s.unavailable++
			s.codes["backend_unavailable"]++
		default:
			if c == "" {
				c = fmt.Sprintf("http_%d", st)
			}
			s.codes[c]++
			if c == "PGRST303" {
				s.expired++
			}
			if st == 401 || c == "PGRST301" || c == "42501" || c == "28000" {
				s.unauth++
			}
		}
		if rem := int(e.num("remaining_s")); rem < s.worstRemaining {
			s.worstRemaining = rem
		}
	}
	var pKeys []string
	for k := range ps {
		pKeys = append(pKeys, k)
	}
	sort.Strings(pKeys)
	bAllOK := true
	var bParts []string
	for _, k := range pKeys {
		s := ps[k]
		fmt.Printf("  %-6s list_members: %d calls, %d HTTP 200, %d PGRST303 (JWT expired), %d unauthenticated, %d while the backend was down for (e); codes: %v; smallest exp margin seen: %ds\n",
			k, s.total, s.ok, s.expired, s.unauth, s.unavailable, s.codes, s.worstRemaining)
		bParts = append(bParts, fmt.Sprintf("%s %d/%d ok (PGRST303=%d, unauthenticated=%d, backend-down=%d, min margin %ds)",
			k, s.ok, s.total, s.expired, s.unauth, s.unavailable, s.worstRemaining))
		if s.expired > 0 || s.unauth > 0 || s.total == 0 || s.ok == 0 {
			bAllOK = false
		}
	}
	// count the short-lived runs separately: each is a whole process life
	shortExits := 0
	shortBad := 0
	for _, e := range evs {
		if e.Proc == "short" && e.Kind == "exit" {
			shortExits++
			st := int(e.num("probe_status"))
			if st != 200 && st > 0 && st != 502 && st != 503 && st != 504 {
				shortBad++
			}
		}
	}
	bEvidence := fmt.Sprintf("an authenticated RPC (brigade.list_members, which PostgREST refuses with PGRST303 on an expired JWT) called continuously by every process: %s; the short-lived process ran %d separate times and %d of those exited with a credential failure (calls made while the stack was deliberately down for (e) are counted as backend-down, not as an invalid token)",
		strings.Join(bParts, "; "), shortExits, shortBad)
	check("b-always-valid", bAllOK && shortExits > 0 && shortBad == 0, bEvidence)

	// ---- (c)
	section("(c) the file is never torn, stays 0600, and the lock does not wait")
	ts.mu.Lock()
	reads, pe, mi, mb, se, re := ts.reads, ts.parseErr, ts.missing, ts.modeBad, ts.statErr, ts.readErr
	modes := ts.modesSeen
	distinct := len(ts.distinct)
	examples := ts.examples
	ts.mu.Unlock()
	fmt.Printf("  lock-free reads of session.json by the prober: %d\n", reads)
	fmt.Printf("    parse failures (a torn read would show here): %d\n", pe)
	fmt.Printf("    reads missing a required field:               %d\n", mi)
	fmt.Printf("    read errors / stat errors:                    %d / %d\n", re, se)
	fmt.Printf("    file modes observed:                          %v (non-0600: %d)\n", modes, mb)
	fmt.Printf("    profile dir mode != 0700 samples:             %d;  stray .tmp files seen: %d\n", dirModeBad, strayTmp)
	fmt.Printf("    distinct refresh tokens observed on disk:     %d\n", distinct)
	for _, ex := range examples {
		fmt.Printf("    example failure: %s\n", ex)
	}
	var probeWaits []float64
	probeUncontended, probeErrs := 0, 0
	for _, e := range evs {
		if e.Kind != "lockprobe" {
			continue
		}
		probeWaits = append(probeWaits, e.num("wait_us"))
		lockWaits = append(lockWaits, e.num("wait_us"))
		lockTries = append(lockTries, e.num("tries"))
		if e.num("tries") <= 1 {
			probeUncontended++
		}
		if e.str("err") != "" {
			probeErrs++
		}
	}
	sort.Float64s(lockWaits)
	med, max, p95 := 0.0, 0.0, 0.0
	uncontended := 0
	for _, t := range lockTries {
		if t <= 1 {
			uncontended++
		}
	}
	if n := len(lockWaits); n > 0 {
		med = lockWaits[n/2]
		max = lockWaits[n-1]
		p95 = lockWaits[(n*95)/100]
	}
	fmt.Printf("  lock acquisitions: %d;  median %.3f ms, p95 %.3f ms, max %.3f ms;  uncontended (1 LOCK_NB attempt): %d/%d\n",
		len(lockWaits), med/1000, p95/1000, max/1000, uncontended, len(lockWaits))
	over := 0
	for _, w := range lockWaits {
		if w > 5000 {
			over++
		}
	}
	fmt.Printf("  acquisitions that waited more than 5 ms: %d;  `unavailable` (10 s bound hit): %d %v\n", over, len(lockErrs)+probeErrs, lockErrs)
	fmt.Printf("  of which the standing contention prober contributed %d acquisitions (%d uncontended, %d failed)\n", len(probeWaits), probeUncontended, probeErrs)
	cEvidence := fmt.Sprintf("%d lock-free reads: %d parse failures, %d missing a required field, %d read errors; modes seen %v (non-0600: %d), profile dir non-0700 samples %d, stray temp files %d; %d lock acquisitions with median %.3f ms / p95 %.3f ms / max %.3f ms, %d uncontended, %d over 5 ms, %d hit the 10 s bound",
		reads, pe, mi, re, modes, mb, dirModeBad, strayTmp, len(lockWaits), med/1000, p95/1000, max/1000, uncontended, over, len(lockErrs))
	check("c-no-tearing", pe == 0 && mi == 0 && re == 0 && mb == 0 && dirModeBad == 0, cEvidence)
	check("c-lock-bound", len(lockErrs)+probeErrs == 0, fmt.Sprintf("no acquisition hit the 10 s `unavailable` bound (%d acquisitions)", len(lockWaits)))
	if max > 5000 {
		record("c-lock-wait", fmt.Sprintf("the max wait was %.1f ms, not 'a few ms': the design polls LOCK_NB every 100 ms, so ANY contention costs at least one 100 ms poll, and a contended acquisition also has to wait out the holder's refresh round trip", max/1000))
	}

	// ---- (d)
	section("(d) kill mid-refresh")
	if !killDone {
		record("d-kill", "the (d) milestone was not scheduled in this run")
	} else {
		check("d-kill-in-critical-section", killLandedInside && !killOtherLockedOut, killEvidence)
	}
	// and the long-lived process must have carried on afterwards
	var afterKill int
	var killTime time.Time
	for _, e := range evs {
		if e.Kind == "kill" {
			killTime = e.when()
		}
	}
	if !killTime.IsZero() {
		for _, e := range evs {
			if e.Proc == "long" && e.Kind == "probe" && e.when().After(killTime) && int(e.num("status")) == 200 {
				afterKill++
			}
		}
	}
	if killDone {
		check("d-other-not-locked-out", afterKill > 0,
			fmt.Sprintf("after the SIGKILL the long-lived process made %d further HTTP 200 authenticated RPCs, and every later lock acquisition is in the distribution above", afterKill))
	}

	// ---- (e)
	section("(e) the channel survives a refresh + access_token push, and the stack restart")
	// find refreshes by `long` that were followed by a push and a still-open channel
	pushOK, pushTotal, chanUpAfter := 0, 0, 0
	for _, e := range evs {
		if e.Proc == "long" && e.Kind == "access_token_push" {
			pushTotal++
			if e.boolean("ok") {
				pushOK++
			}
		}
		if e.Proc == "long" && e.Kind == "post_refresh_channel" && e.boolean("up") {
			chanUpAfter++
		}
	}
	bcasts, drains := 0, 0
	for _, e := range evs {
		if e.Proc == "long" && e.Kind == "broadcast" {
			bcasts++
		}
		if e.Proc == "long" && e.Kind == "drain" {
			drains += int(e.num("fresh"))
		}
	}
	survivedRefresh = pushTotal > 0 && pushOK == pushTotal && chanUpAfter == pushTotal
	eEvidence := fmt.Sprintf("%d access_token pushes on the open channel after a refresh, %d written without error, and the channel was still up 1.5 s later in %d of them; %d broadcasts arrived on that channel and %d distinct messages were drained through fetch_inbox/ack_messages over the soak",
		pushTotal, pushOK, chanUpAfter, bcasts, drains)
	check("e-channel-survives-refresh", survivedRefresh && bcasts > 0, eEvidence)
	if doRestart {
		check("e-restart-rejoin-drain", restartRejoined && restartDrained, restartEvidence)
	} else {
		record("e-restart-rejoin-drain", "the restart milestone was skipped (-restart=false)")
	}

	// ---- (f)
	section("(f) the in-memory refresher never breaks the persisted family")
	// pair every in-memory refresh with the next writer refresh that presented the SAME refresh token
	type pair struct {
		imAt          time.Time
		imSent, imGot string
		wAt           time.Time
		wProc         string
		wGot          string
		wStatus       int
		wCode         string
		gap           time.Duration
	}
	var pairs []pair
	for i, r := range refs {
		if r.proc != "inmem" || r.stat != 200 {
			continue
		}
		for _, w := range refs[i+1:] {
			if w.proc == "inmem" || w.stat == 0 {
				continue
			}
			if w.sent != r.sent {
				continue
			}
			pairs = append(pairs, pair{imAt: r.at, imSent: r.sent, imGot: r.got, wAt: w.at, wProc: w.proc,
				wGot: w.got, wStatus: w.stat, wCode: w.code, gap: w.at.Sub(r.at)})
			break
		}
	}
	oneBehindOK, oneBehindBad, lateOneBehind := 0, 0, 0
	for _, pr := range pairs {
		tag := "MINTED A NEW TOKEN"
		if pr.wGot == pr.imGot {
			tag = "answered with the ACTIVE token (one-behind)"
		}
		fmt.Printf("  inmem %s refreshed %s->%s without persisting; %s later %s presented %s (gap %s) -> HTTP %d %s got=%s  %s\n",
			pr.imAt.Format("15:04:05"), pr.imSent, pr.imGot, pr.wAt.Format("15:04:05"), pr.wProc, pr.imSent,
			pr.gap.Round(time.Second), pr.wStatus, pr.wCode, pr.wGot, tag)
		if pr.wStatus == 200 && pr.wCode == "" {
			oneBehindOK++
			if pr.gap > 10*time.Second && pr.wGot == pr.imGot {
				lateOneBehind++
			}
		} else {
			oneBehindBad++
		}
	}
	fEvidence := fmt.Sprintf("%d times the sandboxed process refreshed in memory and left the older refresh token in the file; the next writer presented that older token %d times successfully and %d times with an error; %d of the successes came more than 10 s later (outside GoTrue's reuse interval), so they are the one-behind rule and not reuse deduplication",
		len(pairs), oneBehindOK, oneBehindBad, lateOneBehind)
	check("f-one-behind", len(pairs) > 0 && oneBehindBad == 0 && lateOneBehind > 0, fEvidence)

	// in-memory family health: the sandboxed process must never have persisted anything
	imPersist := 0
	for _, e := range evs {
		if e.Proc == "inmem" && e.Kind == "refresh" && e.boolean("persisted") {
			imPersist++
		}
	}
	sbDenied, sbFlock := false, false
	for _, e := range evs {
		if e.Kind == "sandbox_write_probe" && e.boolean("denied") {
			sbDenied = true
		}
		if e.Kind == "sandbox_flock_probe" && e.boolean("ok") {
			sbFlock = true
		}
	}
	check("f-inmem-never-persists", imPersist == 0 && sbDenied,
		fmt.Sprintf("the sandboxed process persisted %d times; sandbox-exec really denied a write into the profile directory=%v; it could still take flock(LOCK_EX) on the sidecar through an O_RDONLY fd=%v",
			imPersist, sbDenied, sbFlock))

	if len(badLines) > 0 {
		record("event-log-integrity", fmt.Sprintf("%d unparsable lines in the shared append-only event log (concurrent O_APPEND writes): %v", len(badLines), badLines[:min(3, len(badLines))]))
	} else {
		record("event-log-integrity", fmt.Sprintf("%d events from 4 process kinds, 0 unparsable lines in the shared O_APPEND log", len(evs)))
	}

	fmt.Printf("\nran %s; %d refresh cycles issued\n", time.Since(runStart).Round(time.Second), len(refs))
	return summarise()
}
