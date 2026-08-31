package main

// E0-2: broadcast-from-the-database end to end, with a Go Phoenix client.
//
//	-mode=fast  items (e) 50 concurrent senders / exactly-once drain,
//	                  (f) the "first subscription received nothing" race,
//	                  (h) revocation (leave_team RPC and direct SQL).
//	-mode=soak  item  (i) 30 minutes: two token refreshes + access_token pushes,
//	                  the 66 s heartbeat rule, and a supabase stop/start with
//	                  rejoin and drain. NOT run by -mode=fast.
//
// Ground truth: supabase/migrations/20260830120100_brigade_realtime.sql and
// 20260830120000_brigade_schema.sql. Fixtures are built through the real RPCs only.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ---------------------------------------------------------------- reporting

type resultRow struct {
	ID       string
	Pass     bool
	Record   bool // observation only: never affects the exit code
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

// record prints an observation that is deliberately not an assertion (item (f)).
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

// ---------------------------------------------------------------- utilities

func randHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func rows(b []byte) []map[string]any {
	var r []map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		return nil
	}
	return r
}

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

func pgMsg(r Resp) string {
	e, ok := parsePgError(r.Body)
	if !ok {
		return string(r.Body)
	}
	return e.Message
}

// psql runs one statement as postgres in the local stack container (setup / inspection only).
func psql(sql string) (string, error) {
	cmd := exec.Command("docker", "exec", "-i", "supabase_db_brigade",
		"psql", "-U", "postgres", "-d", "postgres", "-X", "-q", "-A", "-t", "-c", sql)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func psqlMust(sql string) string {
	out, err := psql(sql)
	if err != nil {
		fatal("psql failed: %v (%s)", err, out)
	}
	return out
}

// ---------------------------------------------------------------- token holder

type tok struct {
	mu sync.Mutex
	s  Session
}

func newTok(s Session) *tok { return &tok{s: s} }

func (t *tok) jwt() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.AccessToken
}

func (t *tok) session() Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s
}

func (t *tok) set(s Session) {
	t.mu.Lock()
	t.s = s
	t.mu.Unlock()
}

// ---------------------------------------------------------------- fixtures through the real RPCs

type principal struct {
	sess Session
	tk   *tok
	uid  string
}

// sender is one member principal plus the session it sends from.
type sender struct {
	p   principal
	sid string
}

func newPrincipalQuiet(ctx context.Context, env Env) principal {
	r := doQuiet(ctx, "POST", env.APIURL+"/auth/v1/signup", authHeaders(env, env.PublishableKey),
		map[string]any{"data": map[string]any{}, "gotrue_meta_security": map[string]any{}})
	if r.Status != 200 {
		fatal("anonymous sign-up failed: %d %s", r.Status, redact(string(r.Body)))
	}
	var s Session
	_ = json.Unmarshal(r.Body, &s)
	return principal{sess: s, tk: newTok(s), uid: s.User.ID}
}

func createTeamQuiet(ctx context.Context, env Env, p principal, name, label string) (teamID, secret string) {
	r := rpcQuiet(ctx, env, env.PublishableKey, p.tk.jwt(), "create_team",
		map[string]any{"p_name": name, "p_human_label": label})
	mustOK(r, "create_team")
	return mustStr(r.Body, "create_team", "team_id"), mustStr(r.Body, "create_team", "join_secret")
}

func joinTeamQuiet(ctx context.Context, env Env, p principal, secret, label string) {
	r := rpcQuiet(ctx, env, env.PublishableKey, p.tk.jwt(), "join_team",
		map[string]any{"p_join_secret": secret, "p_human_label": label})
	mustOK(r, "join_team")
	if st, _ := jsonGet[string](r.Body, "status"); st != "joined" {
		fatal("join_team status=%q body=%s", st, redact(string(r.Body)))
	}
}

func registerSessionQuiet(ctx context.Context, env Env, p principal, teamID, name string) string {
	r := rpcQuiet(ctx, env, env.PublishableKey, p.tk.jwt(), "register_session",
		map[string]any{"p_team_id": teamID, "p_name": name})
	mustOK(r, "register_session "+name)
	return mustStr(r.Body, "register_session", "session_id")
}

// member signs up, joins the team and registers one session.
func newMember(ctx context.Context, env Env, teamID, secret, label, sessName string) (principal, string) {
	p := newPrincipalQuiet(ctx, env)
	joinTeamQuiet(ctx, env, p, secret, label)
	return p, registerSessionQuiet(ctx, env, p, teamID, sessName)
}

// ---------------------------------------------------------------- socket subscriber
//
// phoenix.go's waitFor/waitReply consume p.in, so only one reader may exist. sub is that
// single reader: it records EVERY frame in arrival order (so a broadcast that lands during
// the join handshake is never swallowed) and lets several waiters scan the same log.

type sub struct {
	p        *Phx
	name     string
	mu       sync.Mutex
	msgs     []Msg
	notify   chan struct{}
	done     bool
	doneAt   time.Time
	doneErr  error
	joinTopc string
	joinRef  string
}

func newSub(p *Phx, name string) *sub {
	s := &sub{p: p, name: name, notify: make(chan struct{})}
	go func() {
		for m := range p.in {
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			close(s.notify)
			s.notify = make(chan struct{})
			s.mu.Unlock()
		}
		var err error
		select {
		case err = <-p.closed:
		default:
		}
		s.mu.Lock()
		s.done, s.doneAt, s.doneErr = true, time.Now(), err
		close(s.notify)
		s.notify = make(chan struct{})
		s.mu.Unlock()
	}()
	return s
}

func (s *sub) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs)
}

func (s *sub) closed() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done, s.doneAt
}

// waitFrom scans the recorded log from index `from` for pred, then blocks for more.
// Returns the match, the next index to scan from, and whether it matched.
func (s *sub) waitFrom(from int, timeout time.Duration, pred func(Msg) bool) (Msg, int, bool) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		for i := from; i < len(s.msgs); i++ {
			if pred(s.msgs[i]) {
				m := s.msgs[i]
				s.mu.Unlock()
				return m, i + 1, true
			}
		}
		from = len(s.msgs)
		w, done := s.notify, s.done
		s.mu.Unlock()
		if done {
			return Msg{}, from, false
		}
		left := time.Until(deadline)
		if left <= 0 {
			return Msg{}, from, false
		}
		t := time.NewTimer(left)
		select {
		case <-w:
			t.Stop()
		case <-t.C:
			return Msg{}, from, false
		}
	}
}

// joinRaw writes a phx_join using phoenix.go's own payload builder and ref counter; the
// reply is awaited through sub instead of Phx.waitReply so nothing is discarded.
func joinRaw(ctx context.Context, p *Phx, topic string, jc JoinConfig) (string, error) {
	ref := p.nextRef()
	pb, _ := json.Marshal(joinPayload(jc))
	var wire []byte
	if p.vsn == "2.0.0" {
		wire, _ = json.Marshal([]any{ref, ref, topic, "phx_join", json.RawMessage(pb)})
	} else {
		wire, _ = json.Marshal(map[string]any{"topic": topic, "event": "phx_join",
			"payload": json.RawMessage(pb), "ref": ref, "join_ref": ref})
	}
	fmt.Printf("%s [%s] >> %s\n", p.ts(), p.name, redact(string(wire)))
	return ref, p.c.Write(ctx, websocket.MessageText, wire)
}

func isReply(ref string) func(Msg) bool {
	return func(m Msg) bool { return m.Event == "phx_reply" && m.Ref != nil && *m.Ref == ref }
}

func replyStatus(m Msg) (string, string) {
	st, _ := jsonGet[string](m.Payload, "status")
	reason, _ := jsonGet[string](m.Payload, "response", "reason")
	return st, reason
}

// join pushes phx_join and waits for its reply through the sub log.
func (s *sub) join(ctx context.Context, topic string, jc JoinConfig, timeout time.Duration) (Msg, bool) {
	ref, err := joinRaw(ctx, s.p, topic, jc)
	if err != nil {
		return Msg{}, false
	}
	s.joinTopc, s.joinRef = topic, ref
	m, _, ok := s.waitFrom(0, timeout, isReply(ref))
	return m, ok
}

func (s *sub) heartbeat(ctx context.Context, timeout time.Duration) bool {
	from := s.mark()
	ref, err := s.p.push(ctx, nil, "phoenix", "heartbeat", map[string]any{})
	if err != nil {
		return false
	}
	_, _, ok := s.waitFrom(from, timeout, isReply(ref))
	return ok
}

func (s *sub) pushAccessToken(ctx context.Context, jwt string) error {
	_, err := s.p.push(ctx, &s.joinRef, s.joinTopc, "access_token", map[string]any{"access_token": jwt})
	return err
}

// broadcastID extracts payload.payload.message_id from a broadcast frame (text or binary).
func broadcastID(m Msg) (string, int64, bool) {
	if m.Event != "broadcast" {
		return "", 0, false
	}
	id, ok := jsonGet[string](m.Payload, "payload", "message_id")
	if !ok {
		return "", 0, false
	}
	var seq int64
	if f, ok2 := jsonGet[float64](m.Payload, "payload", "seq"); ok2 {
		seq = int64(f)
	} else if s, ok2 := jsonGet[string](m.Payload, "payload", "seq"); ok2 {
		n, _ := strconv.ParseInt(s, 10, 64)
		seq = n
	}
	return id, seq, true
}

// ---------------------------------------------------------------- drain (plan 5.7)

type drainer struct {
	ctx     context.Context
	env     Env
	tk      *tok
	session string
	trigger chan struct{}

	mu        sync.Mutex
	order     []string       // message_ids in the order drain() yielded them
	pageIDs   [][]string     // the same ids grouped by fetch_inbox page
	pageAt    []time.Time    // when each page was yielded (to prove the drain raced the senders)
	seen      map[string]int // message_id -> number of times yielded
	dups      []string
	pages     int
	runs      int
	ackErrs   []string
	fetchErrs []string
	paused    bool
}

func newDrainer(ctx context.Context, env Env, tk *tok, session string) *drainer {
	return &drainer{ctx: ctx, env: env, tk: tk, session: session,
		trigger: make(chan struct{}, 1), seen: map[string]int{}}
}

// kick coalesces: at most one queued run behind the one in flight (plan 5.6).
func (d *drainer) kick() {
	select {
	case d.trigger <- struct{}{}:
	default:
	}
}

func (d *drainer) setPaused(v bool) {
	d.mu.Lock()
	d.paused = v
	d.mu.Unlock()
}

func (d *drainer) isPaused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

func (d *drainer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.order)
}

func (d *drainer) snapshot() ([]string, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := append([]string(nil), d.order...)
	dd := append([]string(nil), d.dups...)
	return o, dd
}

// once: page fetch_inbox in batches of 100 until a short page, acking each page.
func (d *drainer) once() {
	if d.isPaused() {
		return
	}
	d.mu.Lock()
	d.runs++
	d.mu.Unlock()
	for {
		r := rpcQuiet(d.ctx, d.env, d.env.PublishableKey, d.tk.jwt(), "fetch_inbox",
			map[string]any{"p_session_id": d.session, "p_limit": 100})
		if r.Status != 200 {
			d.mu.Lock()
			if len(d.fetchErrs) < 20 {
				d.fetchErrs = append(d.fetchErrs, fmt.Sprintf("HTTP %d %s", r.Status, pgMsg(r)))
			}
			d.mu.Unlock()
			return
		}
		rs := rows(r.Body)
		if len(rs) == 0 {
			return
		}
		ids := make([]string, 0, len(rs))
		for _, m := range rs {
			id, _ := m["message_id"].(string)
			ids = append(ids, id)
		}
		// Record BEFORE acking: if an ack is lost the rows come back and the duplicate is measured.
		d.mu.Lock()
		d.pages++
		d.pageIDs = append(d.pageIDs, append([]string(nil), ids...))
		d.pageAt = append(d.pageAt, time.Now())
		for _, id := range ids {
			d.seen[id]++
			if d.seen[id] > 1 {
				d.dups = append(d.dups, id)
			}
			d.order = append(d.order, id)
		}
		d.mu.Unlock()

		ra := rpcQuiet(d.ctx, d.env, d.env.PublishableKey, d.tk.jwt(), "ack_messages",
			map[string]any{"p_session_id": d.session, "p_message_ids": ids})
		if ra.Status != 200 {
			d.mu.Lock()
			if len(d.ackErrs) < 20 {
				d.ackErrs = append(d.ackErrs, fmt.Sprintf("HTTP %d %s", ra.Status, pgMsg(ra)))
			}
			d.mu.Unlock()
		}
		if len(rs) < 100 {
			return
		}
	}
}

func (d *drainer) run(stop <-chan struct{}, safety time.Duration) {
	t := time.NewTicker(safety)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-d.trigger:
			d.once()
		case <-t.C:
			d.once()
		}
	}
}

// ---------------------------------------------------------------- sending

type sendResult struct {
	messageID string
	dup       bool
	retries   int
	err       string
	at        time.Time
}

// inflightNow/inflightPeak measure how many send_message RPCs are actually in flight at once, so
// "50 CONCURRENT senders" is a measurement rather than an assumption about the barrier.
var (
	inflightNow  int64
	inflightPeak int64
)

func inflightReset() { atomic.StoreInt64(&inflightNow, 0); atomic.StoreInt64(&inflightPeak, 0) }
func inflightMax() int64 {
	return atomic.LoadInt64(&inflightPeak)
}

// sendOnce is one send_message RPC.
func sendOnce(ctx context.Context, env Env, tk *tok, from, to, body, key string) Resp {
	n := atomic.AddInt64(&inflightNow, 1)
	for {
		peak := atomic.LoadInt64(&inflightPeak)
		if n <= peak || atomic.CompareAndSwapInt64(&inflightPeak, peak, n) {
			break
		}
	}
	defer atomic.AddInt64(&inflightNow, -1)
	return rpcQuiet(ctx, env, env.PublishableKey, tk.jwt(), "send_message", map[string]any{
		"p_sender_session_id": from, "p_recipient_session_id": to,
		"p_body": body, "p_idempotency_key": key})
}

// sendWithBackoff retries ONLY the two inbox-pressure limits (recipient_inbox_full,
// sender_quota_for_recipient). Every other failure is reported as-is.
func sendWithBackoff(ctx context.Context, env Env, tk *tok, from, to, body, key string, d *drainer, maxTries int) sendResult {
	for i := 0; i < maxTries; i++ {
		r := sendOnce(ctx, env, tk, from, to, body, key)
		if r.Status == 200 {
			id, _ := jsonGet[string](r.Body, "message_id")
			dup, _ := jsonGet[bool](r.Body, "duplicate")
			return sendResult{messageID: id, dup: dup, retries: i, at: time.Now()}
		}
		msg := pgMsg(r)
		if strings.Contains(msg, "rate_limited:recipient_inbox_full") || strings.Contains(msg, "rate_limited:sender_quota_for_recipient") {
			if d != nil {
				d.kick()
			}
			time.Sleep(time.Duration(120+i*40) * time.Millisecond)
			continue
		}
		return sendResult{retries: i, err: fmt.Sprintf("HTTP %d %s", r.Status, msg)}
	}
	return sendResult{retries: maxTries, err: "gave up after " + strconv.Itoa(maxTries) + " inbox-pressure retries"}
}

// ---------------------------------------------------------------- main

var (
	projectDir = "/Users/rjae/Development/appshapes/brigade"
	// The Supabase CLI is not installed on this machine; the project drives it through npx
	// (plan 12: "Not installed: … the Supabase CLI (use `npx supabase`)").
	supabaseCmd = "npx supabase"
	excluded    = "studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor"
	soakRestart = true
)

func main() {
	mode := flag.String("mode", "fast", "fast | soak")
	envPath := flag.String("env", ".env.test", "env file")
	senders := flag.Int("senders", 50, "(e) concurrent sender sessions")
	perSender := flag.Int("per-sender", 4, "(e) messages per sender")
	attempts := flag.Int("attempts", 20, "(f) race attempts")
	vsn := flag.String("vsn", "1.0.0", "phoenix serializer version")
	soakMin := flag.Float64("soak-minutes", 30, "(i) soak duration in minutes")
	flag.StringVar(&supabaseCmd, "supabase-cmd", supabaseCmd, "how to invoke the Supabase CLI for the soak's stack restart")
	flag.StringVar(&excluded, "supabase-exclude", excluded, "-x service list for `supabase start` (must match how the stack was started)")
	flag.StringVar(&projectDir, "project-dir", projectDir, "directory the Supabase CLI runs in")
	flag.BoolVar(&soakRestart, "soak-restart", soakRestart, "run the supabase stop/start milestone inside the soak")
	flag.Parse()

	env := loadEnv(*envPath)
	fmt.Printf("E0-2 -mode=%s against %s\n", *mode, env.APIURL)
	fmt.Println("run at:", time.Now().Format(time.RFC3339))

	switch *mode {
	case "fast":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		runFast(ctx, env, *vsn, *senders, *perSender, *attempts)
	case "soak":
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*soakMin*60+900)*time.Second)
		defer cancel()
		runSoak(ctx, env, time.Duration(*soakMin*60)*time.Second)
	default:
		fatal("unknown mode %q (want fast or soak)", *mode)
	}

	if summarise() > 0 {
		os.Exit(1)
	}
}

// ================================================================ fast mode

func runFast(ctx context.Context, env Env, vsn string, nSenders, perSender, nAttempts int) {
	tag := randHex(3)

	// ---------------- fixtures ----------------
	section("fixtures: one team, one recipient session, " + strconv.Itoa(nSenders) + " sender principals (all via the real RPCs)")

	recip := newPrincipalQuiet(ctx, env)
	teamID, secret := createTeamQuiet(ctx, env, recip, "E02 "+tag, "recipient")
	sidR := registerSessionQuiet(ctx, env, recip, teamID, "e02-recipient")
	fmt.Printf("team=%s recipient_uid=%s recipient_session=%s\n", teamID, recip.uid, sidR)

	sd := make([]sender, nSenders)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	t0 := time.Now()
	for i := 0; i < nSenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p, sid := newMember(ctx, env, teamID, secret, fmt.Sprintf("sender-%02d", i), fmt.Sprintf("e02-s%02d", i))
			sd[i] = sender{p, sid}
		}(i)
	}
	wg.Wait()
	uniqUID := map[string]bool{}
	uniqSID := map[string]bool{}
	for _, s := range sd {
		uniqUID[s.p.uid] = true
		uniqSID[s.sid] = true
	}
	check("setup-senders", len(uniqUID) == nSenders && len(uniqSID) == nSenders && !uniqUID[recip.uid],
		fmt.Sprintf("%d distinct sender principals and %d distinct sender sessions built in %s; recipient principal is none of them",
			len(uniqUID), len(uniqSID), time.Since(t0).Round(time.Millisecond)))

	// ---------------- self-test: do the exactly-once detectors fire at all? ----------------
	expDetector(ctx, env, teamID, secret, tag)

	// ---------------- (e) 50 concurrent senders ----------------
	expE(ctx, env, vsn, recip, sidR, sd, perSender, tag)

	// ---------------- (f) first-subscription race ----------------
	expF(ctx, env, vsn, teamID, secret, nAttempts, tag)

	// ---------------- (h) revocation ----------------
	expH(ctx, env, vsn, teamID, secret, sd[0].p, sd[0].sid, tag)
}

// ---------------------------------------------------------------- detector self-test
//
// (e)'s verdict is only worth as much as its detectors. Before measuring anything, prove that the
// harness would actually NOTICE a lost message and a duplicated one: poison the inbox behind the
// drain's back and require the exact same comparisons that (e) runs to fail in the expected way.
// Without this, "0 missing, 0 duplicates" could equally well mean the comparison is broken.

func expDetector(ctx context.Context, env Env, teamID, secret, tag string) {
	section("self-test: prove the exactly-once detectors FIRE on an injected fault (a run with no faults proves nothing on its own)")

	rp, rsid := newMember(ctx, env, teamID, secret, "detect-recipient", "e02-detect-r")
	sp, ssid := newMember(ctx, env, teamID, secret, "detect-sender", "e02-detect-s")

	var ids []string
	for i := 0; i < 4; i++ {
		r := sendWithBackoff(ctx, env, sp.tk, ssid, rsid, fmt.Sprintf("detector %d", i),
			fmt.Sprintf("e02-%s-det-%d", tag, i), nil, 5)
		if r.err != "" {
			check("selftest-setup", false, "detector fixture send failed: "+r.err)
			return
		}
		ids = append(ids, r.messageID)
	}
	check("selftest-setup", len(ids) == 4, fmt.Sprintf("detector fixture: 4 messages sent to a private recipient session (%s)", rsid))

	// --- FAULT 1: a message that vanishes from the inbox before the drain ever sees it. ---
	// Marking it 'injected' is exactly what a silently-lost delivery looks like to fetch_inbox.
	lost := ids[1]
	psqlMust(fmt.Sprintf("update brigade.messages set delivery_state='injected', injected_at=now() where id='%s'", lost))

	d := newDrainer(ctx, env, rp.tk, rsid) // the recipient principal owns the session it drains
	d.once()
	got, _ := d.snapshot()
	gotSet := map[string]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	var missing []string
	for _, id := range ids {
		if !gotSet[id] {
			missing = append(missing, id)
		}
	}
	check("selftest-detects-loss", len(got) == 3 && len(missing) == 1 && missing[0] == lost,
		fmt.Sprintf("a message removed from the inbox behind the drain's back IS detected: drain yielded %d/4, the completeness comparison reports %d missing and names the poisoned id=%v. "+
			"So (e)'s \"missing (gap) = 0\" is a real negative, not a comparison that cannot fail",
			len(got), len(missing), len(missing) == 1 && missing[0] == lost))

	// --- FAULT 2: a message redelivered after it was already yielded (a lost ack). ---
	redelivered := ids[0]
	psqlMust(fmt.Sprintf("update brigade.messages set delivery_state='accepted', injected_at=null where id='%s'", redelivered))
	d.once()
	_, dupIDs := d.snapshot()
	d.mu.Lock()
	seenTwice := d.seen[redelivered]
	d.mu.Unlock()
	check("selftest-detects-duplicate", len(dupIDs) == 1 && dupIDs[0] == redelivered && seenTwice == 2,
		fmt.Sprintf("a redelivered message (a lost ack) IS detected: the duplicate list has %d entry naming the redelivered id=%v, seen count=%d. "+
			"So (e)'s \"0 message_ids yielded more than once\" is a real negative",
			len(dupIDs), len(dupIDs) == 1 && dupIDs[0] == redelivered, seenTwice))

	// --- FAULT 3: the database cross-check must also notice an unacked row. ---
	psqlMust(fmt.Sprintf("update brigade.messages set delivery_state='accepted', injected_at=null where id='%s'", ids[3]))
	out := psqlMust(fmt.Sprintf("select count(*) from brigade.messages where recipient_session_id='%s' and delivery_state<>'injected'", rsid))
	check("selftest-detects-unacked", out == "1",
		fmt.Sprintf("the e-db-rows cross-check would notice an undelivered row: with one row forced back to 'accepted', the not-injected count is %q (expected \"1\")", out))
	psqlMust(fmt.Sprintf("update brigade.messages set delivery_state='injected', injected_at=now() where recipient_session_id='%s'", rsid))
}

// ---------------------------------------------------------------- (e)

func expE(ctx context.Context, env Env, vsn string, recip principal, sidR string, sd []sender, perSender int, tag string) {

	total := len(sd) * perSender
	section(fmt.Sprintf("(e) %d concurrent sender sessions -> one recipient, %d messages each (%d total); drain must be exactly-once",
		len(sd), perSender, total))

	topic := "realtime:brigade:session:" + sidR
	p, err := dial(ctx, env, env.PublishableKey, vsn, "recipient")
	if err != nil {
		fatal("dial: %v", err)
	}
	defer p.close()
	s := newSub(p, "recipient")
	rep, ok := s.join(ctx, topic, JoinConfig{Private: true, AccessToken: recip.tk.jwt()}, 15*time.Second)
	st, reason := replyStatus(rep)
	check("e-join", ok && st == "ok", fmt.Sprintf("private join of the recipient's own topic %s: ok=%v status=%q reason=%q", topic, ok, st, reason))
	if !ok || st != "ok" {
		return
	}
	tJoinOK := time.Now()

	d := newDrainer(ctx, env, recip.tk, sidR)
	stop := make(chan struct{})
	go d.run(stop, 300*time.Millisecond)

	// broadcast watcher: the drain trigger, and the (unasserted) arrival-order record.
	type arrival struct {
		id  string
		seq int64
		at  time.Time
	}
	var bmu sync.Mutex
	var arrivals []arrival
	bstop := make(chan struct{})
	go func() {
		from := 0
		for {
			select {
			case <-bstop:
				return
			default:
			}
			m, next, got := s.waitFrom(from, 500*time.Millisecond, func(m Msg) bool { _, _, ok := broadcastID(m); return ok })
			from = next
			if !got {
				continue
			}
			id, seq, _ := broadcastID(m)
			bmu.Lock()
			arrivals = append(arrivals, arrival{id, seq, m.At})
			bmu.Unlock()
			d.kick()
		}
	}()

	// fire all senders at once
	sent := make([][]sendResult, len(sd))
	firstIssued := make([]time.Time, len(sd)) // when each goroutine issued its FIRST send
	start := make(chan struct{})
	var wg sync.WaitGroup
	inflightReset()
	tSend := time.Now()
	for i := range sd {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out := make([]sendResult, perSender)
			for j := 0; j < perSender; j++ {
				key := fmt.Sprintf("e02-%s-s%02d-m%d", tag, i, j)
				if j == 0 {
					firstIssued[i] = time.Now()
				}
				out[j] = sendWithBackoff(ctx, env, sd[i].p.tk, sd[i].sid, sidR,
					fmt.Sprintf("e concurrent %02d/%d", i, j), key, d, 60)
				time.Sleep(time.Duration(20+j*30) * time.Millisecond)
			}
			sent[i] = out
		}(i)
	}
	tBarrier := time.Now()
	close(start)
	wg.Wait()
	sendElapsed := time.Since(tSend)
	peak := inflightMax()

	// Concurrency is MEASURED, not assumed: the peak number of send_message RPCs simultaneously in
	// flight, and the wall-clock spread between the first and last goroutine issuing its first send.
	// 50 sequential senders would show peak=1 and a spread as wide as the whole run.
	var fmin, fmax time.Time
	for _, t := range firstIssued {
		if t.IsZero() {
			continue
		}
		if fmin.IsZero() || t.Before(fmin) {
			fmin = t
		}
		if fmax.IsZero() || t.After(fmax) {
			fmax = t
		}
	}
	spread := fmax.Sub(fmin)
	check("e-concurrency", peak >= int64(len(sd))/2 && spread < 200*time.Millisecond,
		fmt.Sprintf("MEASURED concurrency: peak %d send_message RPCs in flight simultaneously (of %d senders; sequential senders would peak at 1); "+
			"all %d goroutines issued their first send within %s of each other",
			peak, len(sd), len(sd), spread.Round(time.Millisecond)))

	sentIDs := map[string]bool{}
	var sendErrs []string
	dups, retries := 0, 0
	for i := range sent {
		for j, r := range sent[i] {
			retries += r.retries
			if r.err != "" {
				sendErrs = append(sendErrs, fmt.Sprintf("s%02d/m%d: %s", i, j, r.err))
				continue
			}
			if r.dup {
				dups++
			}
			sentIDs[r.messageID] = true
		}
	}
	check("e-sends", len(sendErrs) == 0 && len(sentIDs) == total && dups == 0,
		fmt.Sprintf("%d/%d sends returned 200 with %d distinct message_ids (duplicate=true on %d); %d inbox-pressure retries; barrier->last send %s; errors: %v",
			total-len(sendErrs), total, len(sentIDs), dups, retries, sendElapsed.Round(time.Millisecond),
			func() any {
				if len(sendErrs) == 0 {
					return "none"
				}
				if len(sendErrs) > 5 {
					return sendErrs[:5]
				}
				return sendErrs
			}()))
	_ = tBarrier

	// let the drain finish
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		d.kick()
		if d.count() >= len(sentIDs) {
			// one more pass to catch anything late
			time.Sleep(700 * time.Millisecond)
			d.kick()
			time.Sleep(700 * time.Millisecond)
			if d.count() >= len(sentIDs) {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	close(bstop)
	close(stop)
	time.Sleep(200 * time.Millisecond)

	order, dupIDs := d.snapshot()
	d.mu.Lock()
	pages, runs, ackErrs, fetchErrs := d.pages, d.runs, d.ackErrs, d.fetchErrs
	tLastSend := tSend.Add(sendElapsed)
	pagesDuringBurst, envDuringBurst := 0, 0
	for i, t := range d.pageAt {
		if t.Before(tLastSend) {
			pagesDuringBurst++
			envDuringBurst += len(d.pageIDs[i])
		}
	}
	d.mu.Unlock()

	// Exactly-once is only interesting if the drain was running WHILE the senders were writing. If the
	// drain had only started after the last send returned it would be draining a static table.
	check("e-drain-concurrent-with-sends", pagesDuringBurst > 0 && envDuringBurst > 0,
		fmt.Sprintf("the drain raced the senders rather than draining a settled table: %d of %d fetch_inbox pages (%d of %d envelopes) were yielded BEFORE the last send returned, i.e. inside the %s send burst",
			pagesDuringBurst, pages, envDuringBurst, len(order), sendElapsed.Round(time.Millisecond)))

	// --- exactly-once assertions ---
	check("e-drain-no-duplicate", len(dupIDs) == 0,
		fmt.Sprintf("drain yielded %d envelopes over %d fetch_inbox pages (%d drain runs); %d message_ids yielded more than once%s",
			len(order), pages, runs, len(dupIDs), func() string {
				if len(dupIDs) == 0 {
					return ""
				}
				n := len(dupIDs)
				if n > 5 {
					n = 5
				}
				return "; first: " + strings.Join(dupIDs[:n], ",")
			}()))

	drained := map[string]bool{}
	for _, id := range order {
		drained[id] = true
	}
	var missing []string
	for id := range sentIDs {
		if !drained[id] {
			missing = append(missing, id)
		}
	}
	var extra []string
	for id := range drained {
		if !sentIDs[id] {
			extra = append(extra, id)
		}
	}
	// len(sentIDs)==total is re-required here so the set comparison can never be trivially true over a
	// run the rate limiter silently truncated.
	check("e-drain-complete", total > 0 && len(sentIDs) == total && len(missing) == 0 && len(extra) == 0 && len(order) == len(sentIDs),
		fmt.Sprintf("sent %d of the %d intended, drained %d envelopes / %d distinct; missing (gap) = %d, unexpected = %d",
			len(sentIDs), total, len(order), len(drained), len(missing), len(extra)))

	check("e-drain-clean", len(ackErrs) == 0 && len(fetchErrs) == 0,
		fmt.Sprintf("fetch_inbox errors=%v ack_messages errors=%v", fetchErrs, ackErrs))

	// --- database cross-check: the drain order IS ascending seq, and every row is injected ---
	dbOut := psqlMust(fmt.Sprintf(
		"select id||' '||seq||' '||delivery_state from brigade.messages where recipient_session_id='%s' order by seq", sidR))
	var dbIDs []string
	var dbSeqs []int64
	allInjected := true
	for _, line := range strings.Split(dbOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		dbIDs = append(dbIDs, f[0])
		n, _ := strconv.ParseInt(f[1], 10, 64)
		dbSeqs = append(dbSeqs, n)
		if f[2] != "injected" {
			allInjected = false
		}
	}
	// Set equality against the database, not just a count: a lost row plus a stray row would cancel out
	// in a count comparison.
	dbSet := map[string]bool{}
	for _, id := range dbIDs {
		dbSet[id] = true
	}
	dbMissing, dbExtra := 0, 0
	for id := range sentIDs {
		if !dbSet[id] {
			dbMissing++
		}
	}
	for id := range dbSet {
		if !sentIDs[id] {
			dbExtra++
		}
	}
	check("e-db-rows", len(dbIDs) == len(sentIDs) && dbMissing == 0 && dbExtra == 0 && allInjected,
		fmt.Sprintf("brigade.messages for the recipient session: %d rows (sent %d); the row-id SET equals the sent set (missing %d, unexpected %d); every row delivery_state='injected' (i.e. acked by the drain)=%v",
			len(dbIDs), len(sentIDs), dbMissing, dbExtra, allInjected))

	seqOfID := map[string]int64{}
	for i, id := range dbIDs {
		seqOfID[id] = dbSeqs[i]
	}
	contiguous := len(dbSeqs) > 0 && dbSeqs[len(dbSeqs)-1]-dbSeqs[0]+1 == int64(len(dbSeqs))

	// What fetch_inbox guarantees is the ordering WITHIN a page (`order by m.seq`).
	d.mu.Lock()
	pageIDs := append([][]string(nil), d.pageIDs...)
	d.mu.Unlock()
	badPage := -1
	for pi, page := range pageIDs {
		var prev int64 = -1
		for _, id := range page {
			sq, ok := seqOfID[id]
			if !ok || (prev >= 0 && sq <= prev) {
				badPage = pi
				break
			}
			prev = sq
		}
		if badPage >= 0 {
			break
		}
	}
	check("e-drain-page-order", badPage == -1,
		fmt.Sprintf("every one of the %d fetch_inbox pages is strictly ascending in seq (first offending page index %d); seq range %d..%d over %d rows, contiguous=%v",
			len(pageIDs), badPage, func() int64 {
				if len(dbSeqs) > 0 {
					return dbSeqs[0]
				}
				return -1
			}(), func() int64 {
				if len(dbSeqs) > 0 {
					return dbSeqs[len(dbSeqs)-1]
				}
				return -1
			}(), len(dbSeqs), contiguous))

	// ACROSS pages the order is NOT globally ascending, and must not be relied on: plan 5.7 says a
	// transaction holding seq 41 can commit after 42 is already visible, which is why the drain has
	// no `seq > last` watermark. Measured, not asserted.
	backSteps, maxBack := 0, int64(0)
	var globalPrev int64 = -1
	for _, id := range order {
		sq, ok := seqOfID[id]
		if !ok {
			continue
		}
		if globalPrev >= 0 && sq < globalPrev {
			backSteps++
			if globalPrev-sq > maxBack {
				maxBack = globalPrev - sq
			}
		}
		globalPrev = sq
	}
	record("e-drain-cross-page-seq", fmt.Sprintf(
		"RECORDED (plan 5.7): the drain order across pages went BACKWARDS in seq %d times over %d envelopes in %d pages, "+
			"largest backward step %d. A lower seq committing after a higher one is exactly why the cursor is "+
			"\"the set of delivery_state='accepted' rows\" and never `seq > last`; exactly-once holds regardless (see e-drain-*).",
		backSteps, len(order), len(pageIDs), maxBack))

	// --- broadcast arrival order: RECORDED, never asserted ---
	bmu.Lock()
	arr := append([]arrival(nil), arrivals...)
	bmu.Unlock()
	seqOf := seqOfID
	inversions, unknown := 0, 0
	var prev int64 = -1
	bseen := map[string]int{}
	for _, a := range arr {
		bseen[a.id]++
		sq, ok := seqOf[a.id]
		if !ok {
			unknown++
			continue
		}
		if prev >= 0 && sq < prev {
			inversions++
		}
		prev = sq
	}
	bdup := 0
	for _, n := range bseen {
		if n > 1 {
			bdup++
		}
	}
	record("e-broadcast-order", fmt.Sprintf(
		"RECORDED (not asserted): %d broadcasts arrived for %d sent messages (%d distinct, %d ids seen twice, %d unmatched); "+
			"%d of %d consecutive pairs arrived out of seq order; broadcast payload carries seq, so arrival order is observable and is NOT the delivery order",
		len(arr), len(sentIDs), len(bseen), bdup, unknown, inversions, max(0, len(arr)-1)))
	// where do the missed broadcasts sit relative to the join? (item (f)'s race, seen from here)
	missEarly, missLate, hitEarly, hitLate := 0, 0, 0, 0
	for i := range sent {
		for _, r := range sent[i] {
			if r.err != "" {
				continue
			}
			early := r.at.Sub(tJoinOK) < time.Second
			if bseen[r.messageID] > 0 {
				if early {
					hitEarly++
				} else {
					hitLate++
				}
			} else {
				if early {
					missEarly++
				} else {
					missLate++
				}
			}
		}
	}
	record("e-broadcast-coverage", fmt.Sprintf(
		"RECORDED: %d/%d sent messages produced a broadcast the recipient saw (%.1f%%). Split by when the send landed relative to join-ok: "+
			"within the first second %d seen / %d missed; after the first second %d seen / %d missed. "+
			"The drain, not the broadcast, is the delivery path — see item (f).",
		len(bseen), len(sentIDs), 100*float64(len(bseen))/float64(max(1, len(sentIDs))),
		hitEarly, missEarly, hitLate, missLate))
}

// ---------------------------------------------------------------- (f)

func expF(ctx context.Context, env Env, vsn, teamID, secret string, attempts int, tag string) {
	section(fmt.Sprintf("(f) the \"first subscription received nothing\" race: %d attempts, each on a FRESH session and a FRESH socket", attempts))

	// Four timing classes around the join, each with its own pair of sender sessions so no
	// per-session 20/min limit is approached:
	//   pre     — sent before the socket exists at all
	//   during  — fired at the same instant as the phx_join push
	//   post    — sent the instant the join reply lands (no other work in between)
	//   settled — sent 1 s after join ok; the control that proves the channel does deliver
	rp, _ := newMember(ctx, env, teamID, secret, "race-recipient", "e02-race-r0")
	classes := []string{"pre", "during", "post", "settled"}
	senders := map[string][]sender{}
	for _, c := range classes {
		for k := 0; k < 2; k++ {
			pp, sid := newMember(ctx, env, teamID, secret, "race-"+c+strconv.Itoa(k), fmt.Sprintf("e02-race-%s%d", c, k))
			senders[c] = append(senders[c], sender{pp, sid})
		}
	}

	missed := map[string]int{}
	arrived := map[string]int{}
	firstBcastDelays := []time.Duration{}
	postIssuedAt := []time.Duration{} // join-ok -> post send issued
	sentTotal := 0
	drainOK := 0
	var drainShort []string
	var joinFails []string
	// The four classes name INTENT. The race is about when a message actually COMMITTED relative to
	// join-ok, which is only knowable after the fact from when send_message returned. Every send whose
	// RPC returned strictly after join ok had a committed row while the channel was already joined —
	// if such a message produces no broadcast, that IS the "first subscription received nothing" race.
	committedAfterJoin, committedAfterJoinMissed := 0, 0
	var committedAfterJoinMissDetail []string
	var duringReturnAt []time.Duration

	for a := 0; a < attempts; a++ {
		sid := registerSessionQuiet(ctx, env, rp, teamID, fmt.Sprintf("e02-race-r%02d", a))
		topic := "realtime:brigade:session:" + sid
		k := a % 2
		ids := map[string]string{}
		res := map[string]sendResult{}
		send := func(class, note string) sendResult {
			r := sendWithBackoff(ctx, env, senders[class][k].p.tk, senders[class][k].sid, sid,
				"race "+note, fmt.Sprintf("e02-%s-f%02d-%s", tag, a, class), nil, 5)
			if r.err != "" {
				fatal("(f) %s-send failed: %s", class, r.err)
			}
			return r
		}

		// (1) BEFORE the socket even exists
		res["pre"] = send("pre", "pre")
		ids["pre"] = res["pre"].messageID

		p, err := dial(ctx, env, env.PublishableKey, vsn, fmt.Sprintf("race%02d", a))
		if err != nil {
			fatal("(f) dial: %v", err)
		}
		s := newSub(p, "race")

		// (2) DURING the join handshake: fired at the same instant as the phx_join push,
		// and deliberately NOT waited for, so (3) is not delayed by its round trip.
		fire := make(chan struct{})
		durDone := make(chan sendResult, 1)
		go func() {
			<-fire
			durDone <- send("during", "during")
		}()
		close(fire)
		rep, ok := s.join(ctx, topic, JoinConfig{Private: true, AccessToken: rp.tk.jwt()}, 15*time.Second)
		st, reason := replyStatus(rep)
		if !ok || st != "ok" {
			joinFails = append(joinFails, fmt.Sprintf("attempt %d: ok=%v status=%q reason=%q", a, ok, st, reason))
			<-durDone
			p.close()
			continue
		}
		tJoinOK := time.Now()

		// (3) IMMEDIATELY after join ok — nothing else happens in between
		postIssuedAt = append(postIssuedAt, time.Since(tJoinOK))
		res["post"] = send("post", "post")
		ids["post"] = res["post"].messageID
		res["during"] = <-durDone
		ids["during"] = res["during"].messageID
		duringReturnAt = append(duringReturnAt, res["during"].at.Sub(tJoinOK))

		// (4) SETTLED control: 1 s after join ok
		if d := time.Until(tJoinOK.Add(time.Second)); d > 0 {
			time.Sleep(d)
		}
		res["settled"] = send("settled", "settled")
		ids["settled"] = res["settled"].messageID
		sentTotal += 4

		// collect broadcasts for 2.5 s after the settled send
		seenB := map[string]time.Time{}
		from := 0
		bdeadline := time.Now().Add(2500 * time.Millisecond)
		for time.Now().Before(bdeadline) {
			m, next, got := s.waitFrom(from, time.Until(bdeadline), func(m Msg) bool { _, _, ok := broadcastID(m); return ok })
			from = next
			if !got {
				break
			}
			id, _, _ := broadcastID(m)
			if _, dup := seenB[id]; !dup {
				seenB[id] = m.At
			}
		}
		var firstB time.Time
		for _, t := range seenB {
			if firstB.IsZero() || t.Before(firstB) {
				firstB = t
			}
		}
		if !firstB.IsZero() {
			firstBcastDelays = append(firstBcastDelays, firstB.Sub(tJoinOK))
		}
		for _, c := range classes {
			if _, got := seenB[ids[c]]; got {
				arrived[c]++
			} else {
				missed[c]++
			}
			// The commit-time classification, independent of the class label. A send whose RPC
			// returned after join ok had its row committed on an already-joined channel.
			if r, ok := res[c]; ok && r.err == "" && r.at.After(tJoinOK) {
				committedAfterJoin++
				if _, got := seenB[ids[c]]; !got {
					committedAfterJoinMissed++
					committedAfterJoinMissDetail = append(committedAfterJoinMissDetail,
						fmt.Sprintf("attempt %d class %s committed +%dms after join ok, no broadcast", a, c, r.at.Sub(tJoinOK).Milliseconds()))
				}
			}
		}

		// drain on join-ok: does it cover whatever the broadcast missed?
		d := newDrainer(ctx, env, rp.tk, sid)
		d.once()
		got, _ := d.snapshot()
		gotSet := map[string]bool{}
		for _, id := range got {
			gotSet[id] = true
		}
		var miss []string
		for _, c := range classes {
			if !gotSet[ids[c]] {
				miss = append(miss, c)
			}
		}
		if len(miss) == 0 && len(got) == len(classes) {
			drainOK++
		} else {
			drainShort = append(drainShort, fmt.Sprintf("attempt %d: drained %d/%d, missing %v", a, len(got), len(classes), miss))
		}
		bstat := func(c string) string {
			if t, ok := seenB[ids[c]]; ok {
				return fmt.Sprintf("%s=+%dms", c, t.Sub(tJoinOK).Milliseconds())
			}
			return c + "=MISS"
		}
		fmt.Printf("  (f) attempt %2d: broadcasts %s %s %s %s | drain yielded %d/%d\n",
			a, bstat("pre"), bstat("during"), bstat("post"), bstat("settled"), len(got), len(classes))
		p.close()
	}

	check("f-joins", len(joinFails) == 0, fmt.Sprintf("all %d race attempts joined their fresh topic: %v", attempts, func() any {
		if len(joinFails) == 0 {
			return "yes"
		}
		return joinFails
	}()))

	check("f-drain-covers", drainOK == attempts-len(joinFails) && len(drainShort) == 0,
		fmt.Sprintf("drain() on join-ok returned all %d messages (pre-join, during-handshake, immediately-post-join-ok, settled) on %d/%d attempts; shortfalls: %v",
			len(classes), drainOK, attempts-len(joinFails), func() any {
				if len(drainShort) == 0 {
					return "none"
				}
				return drainShort
			}()))

	stat := func(ds []time.Duration) string {
		if len(ds) == 0 {
			return "n/a"
		}
		sorted := append([]time.Duration(nil), ds...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		var sum time.Duration
		for _, d := range sorted {
			sum += d
		}
		return fmt.Sprintf("min=%dms median=%dms max=%dms", sorted[0].Milliseconds(),
			sorted[len(sorted)/2].Milliseconds(), sorted[len(sorted)-1].Milliseconds())
	}
	n := attempts - len(joinFails)
	record("f-race", fmt.Sprintf(
		"RECORDED, never a flip: %d attempts x 4 messages (%d sends), one fresh session and fresh socket each. "+
			"message_accepted broadcast arrived within 2.5 s for: "+
			"sent-BEFORE-the-socket-existed %d/%d (missed %d); "+
			"sent-DURING-the-join-handshake %d/%d (missed %d); "+
			"sent-IMMEDIATELY-after-join-ok %d/%d (missed %d); "+
			"sent-1s-after-join-ok (control) %d/%d (missed %d). "+
			"A miss in the third class is the \"first subscription received nothing\" race: %s. "+
			"The post send was issued %s after the join reply; the first broadcast ever seen on a socket arrived %s after join ok.",
		attempts, sentTotal,
		arrived["pre"], n, missed["pre"],
		arrived["during"], n, missed["during"],
		arrived["post"], n, missed["post"],
		arrived["settled"], n, missed["settled"],
		func() string {
			if missed["post"] == 0 {
				return fmt.Sprintf("it did NOT reproduce in %d attempts", n)
			}
			return fmt.Sprintf("REPRODUCED on %d of %d attempts", missed["post"], n)
		}(),
		stat(postIssuedAt), stat(firstBcastDelays)))

	// The same 80 sends re-classified by MEASURED commit time rather than by intent. This is the
	// statistic the race is actually about, and it is what makes "not reproduced" falsifiable: if the
	// during-class sends all returned BEFORE join ok, the window was never exercised and the negative
	// result would be worthless.
	record("f-race-by-commit-time", fmt.Sprintf(
		"RECORDED: re-classified by when send_message actually RETURNED (i.e. the row was committed) rather than by intended class: "+
			"%d of %d sends committed strictly AFTER join ok; of those %d produced no broadcast within 2.5 s%s. "+
			"The during-handshake send's RPC returned %s relative to join ok (negative = it committed before the join reply landed, "+
			"which is what makes it a pre-join message in practice).",
		committedAfterJoin, sentTotal, committedAfterJoinMissed,
		func() string {
			if len(committedAfterJoinMissDetail) == 0 {
				return " (so the post-join window was exercised and never dropped a message)"
			}
			n := len(committedAfterJoinMissDetail)
			if n > 5 {
				n = 5
			}
			return "; first: " + strings.Join(committedAfterJoinMissDetail[:n], " | ")
		}(),
		stat(duringReturnAt)))

	// A negative result is only honest if the window was actually entered. Assert that it was.
	check("f-window-exercised", committedAfterJoin >= 2*n,
		fmt.Sprintf("the post-join-ok window was genuinely exercised: %d of %d sends had their row committed AFTER join ok (at least the post and settled class on every one of the %d attempts), "+
			"so \"the race did not reproduce\" is a statement about %d real opportunities, not about a window that was never entered",
			committedAfterJoin, sentTotal, n, committedAfterJoin))
}

// ---------------------------------------------------------------- (h)

func expH(ctx context.Context, env Env, vsn, teamID, secret string, sndP principal, sndSid, tag string) {
	section("(h) revocation: Unauthorized on the principal's OWN topic at the next join, and an open channel closed by the next access_token push")

	// An OUTSIDER: an authenticated principal with no membership anywhere near this team. Its refusal
	// on the victim's topic is the reference string a revoked principal must match exactly — otherwise
	// "Unauthorized" could be any unrelated failure that happens to contain the word.
	outsider := newPrincipalQuiet(ctx, env)

	// joinOnce dials a FRESH socket and attempts one join, so a refusal can never be a stale or
	// half-closed connection. Returns (status, reason, elapsed).
	joinOnce := func(name, topic, jwt string) (string, string, time.Duration) {
		pp, err := dial(ctx, env, env.PublishableKey, vsn, name)
		if err != nil {
			fatal("dial %s: %v", name, err)
		}
		defer pp.close()
		ss := newSub(pp, name)
		t := time.Now()
		rep, _ := ss.join(ctx, topic, JoinConfig{Private: true, AccessToken: jwt}, 20*time.Second)
		st, reason := replyStatus(rep)
		return st, reason, time.Since(t)
	}

	// two victims: one revoked through the leave_team RPC, one through direct SQL (membership only,
	// session left open, which isolates the membership clause of brigade.owns_session_topic).
	for _, variant := range []string{"leave_team", "sql"} {
		vp, vsid := newMember(ctx, env, teamID, secret, "victim-"+variant, "e02-victim-"+variant)
		topic := "realtime:brigade:session:" + vsid

		p, err := dial(ctx, env, env.PublishableKey, vsn, "victim-"+variant)
		if err != nil {
			fatal("dial: %v", err)
		}
		s := newSub(p, "victim-"+variant)
		rep, ok := s.join(ctx, topic, JoinConfig{Private: true, AccessToken: vp.tk.jwt()}, 15*time.Second)
		st, reason := replyStatus(rep)
		check("h-"+variant+"-join-before", ok && st == "ok",
			fmt.Sprintf("BEFORE revocation, the victim joins its own topic: ok=%v status=%q reason=%q", ok, st, reason))

		// control: the open channel really delivers
		from := s.mark()
		ctl := sendWithBackoff(ctx, env, sndP.tk, sndSid, vsid, "control before revocation",
			fmt.Sprintf("e02-%s-h-%s-ctl", tag, variant), nil, 5)
		gotCtl := false
		if ctl.err == "" {
			_, _, gotCtl = s.waitFrom(from, 5*time.Second, func(m Msg) bool {
				id, _, ok := broadcastID(m)
				return ok && id == ctl.messageID
			})
		}
		check("h-"+variant+"-control", ctl.err == "" && gotCtl,
			fmt.Sprintf("BEFORE revocation the open channel receives a message_accepted broadcast for message %s: %v (send err=%q)", ctl.messageID, gotCtl, ctl.err))

		// BASELINE, taken while the victim is still active: what a genuine non-member is told when it
		// asks for this exact topic. The post-revocation refusal must be this same string.
		oSt, oReason, oTook := joinOnce("outsider-"+variant, topic, outsider.tk.jwt())
		check("h-"+variant+"-outsider-baseline", oSt == "error" && strings.Contains(oReason, "Unauthorized"),
			fmt.Sprintf("BASELINE: an authenticated principal with NO membership joining the victim's topic is refused status=%q reason=%q after %s — this is the reference refusal a revoked principal must match",
				oSt, oReason, oTook.Round(time.Millisecond)))

		// revoke
		switch variant {
		case "leave_team":
			r := rpcQuiet(ctx, env, env.PublishableKey, vp.tk.jwt(), "leave_team", map[string]any{"p_team_id": teamID})
			left, _ := jsonGet[bool](r.Body, "left")
			check("h-leave_team-rpc", r.Status == 200 && left,
				fmt.Sprintf("leave_team RPC: HTTP %d body=%s", r.Status, string(r.Body)))
		case "sql":
			out := psqlMust(fmt.Sprintf(
				"update brigade.memberships set status='revoked', revoked_at=now() where team_id='%s' and user_id='%s' returning status", teamID, vp.uid))
			check("h-sql-revoke", strings.Contains(out, "revoked"),
				fmt.Sprintf("direct SQL as postgres set the membership to revoked (session left OPEN, so only the membership clause of brigade.owns_session_topic changes): %q", out))
		}
		// The revocation must be COMMITTED and visible in the database before anything is concluded from
		// a refusal. Asserted, not merely printed.
		dbStatus := psqlMust(fmt.Sprintf(
			"select status from brigade.memberships where team_id='%s' and user_id='%s'", teamID, vp.uid))
		dbClosed := psqlMust(fmt.Sprintf(
			"select coalesce(closed_at::text,'open') from brigade.sessions where id='%s'", vsid))
		// The definer helper is the authority; ask it directly for this exact topic and principal.
		dbOwns := psqlMust(fmt.Sprintf(
			"select exists (select 1 from brigade.sessions s join brigade.memberships m on m.team_id=s.team_id and m.user_id=s.owner_id and m.status='active' where s.owner_id='%s' and s.closed_at is null and 'brigade:session:%s' = 'brigade:session:'||s.id::text)",
			vp.uid, vsid))
		check("h-"+variant+"-revoked-in-db", dbStatus == "revoked" && dbOwns == "f",
			fmt.Sprintf("the revocation is COMMITTED before the re-join is attempted: brigade.memberships.status=%q, session closed_at=%q, and the owns_session_topic predicate evaluated over the same rows returns %q (f = the topic is no longer owned)",
				dbStatus, dbClosed, dbOwns))

		// (2) the OPEN channel must be closed by the next access_token push
		ns, rr := refreshQuiet(ctx, env, env.PublishableKey, vp.tk.session().RefreshToken)
		if rr.Status != 200 {
			fatal("refresh for the victim failed: HTTP %d %s", rr.Status, redact(string(rr.Body)))
		}
		vp.tk.set(ns)
		// Rule out "the token is simply broken" as the explanation for everything that follows: the
		// refreshed JWT must still authenticate, and must still name the same principal.
		wh := authHeaders(env, env.PublishableKey)
		wh["Authorization"] = "Bearer " + ns.AccessToken
		who := doQuiet(ctx, "GET", env.APIURL+"/auth/v1/user", wh, nil)
		whoID, _ := jsonGet[string](who.Body, "id")
		claims := jwtClaims(ns.AccessToken)
		expOK := false
		if e, ok := claims["exp"].(float64); ok {
			expOK = time.Now().Before(time.Unix(int64(e), 0))
		}
		check("h-"+variant+"-token-still-valid", who.Status == 200 && whoID == vp.uid && expOK,
			fmt.Sprintf("the refreshed JWT used for the rest of this test is genuinely valid: GET /auth/v1/user -> HTTP %d, sub matches the victim principal=%v, exp in the future=%v (role=%v). So a later Unauthorized cannot be blamed on an expired or wrong token",
				who.Status, whoID == vp.uid, expOK, claims["role"]))
		from = s.mark()
		tPush := time.Now()
		if err := s.pushAccessToken(ctx, ns.AccessToken); err != nil {
			fmt.Printf("  access_token push write error: %v\n", err)
		}
		down, _, gotDown := s.waitFrom(from, 15*time.Second, func(m Msg) bool {
			if m.Event == "phx_close" || m.Event == "phx_error" {
				return true
			}
			if m.Event == "system" {
				sst, _ := jsonGet[string](m.Payload, "status")
				return sst == "error"
			}
			return false
		})
		closedSock, closedAt := s.closed()
		evid := "no channel-down frame and the socket stayed open"
		if gotDown {
			dst, _ := jsonGet[string](down.Payload, "status")
			dmsg, _ := jsonGet[string](down.Payload, "message")
			evid = fmt.Sprintf("event=%s status=%q message=%q after %s", down.Event, dst, dmsg, time.Since(tPush).Round(time.Millisecond))
		} else if closedSock {
			evid = fmt.Sprintf("websocket closed by the server %s after the push", closedAt.Sub(tPush).Round(time.Millisecond))
		}
		check("h-"+variant+"-channel-closed", gotDown || closedSock,
			fmt.Sprintf("after revocation, the access_token push on the ALREADY-OPEN channel brings it down: %s", evid))
		p.close()

		// (1) the next join of its OWN topic must be Unauthorized — on a FRESH socket with the freshly
		// refreshed, just-verified JWT, so neither a closed socket nor a bad token can explain it.
		st2, reason2, took2 := joinOnce("victim-"+variant+"-rejoin", topic, vp.tk.jwt())
		check("h-"+variant+"-join-after", st2 == "error" && strings.Contains(reason2, "Unauthorized"),
			fmt.Sprintf("AFTER revocation, the victim's join of its OWN topic %s: status=%q reason=%q (after %s; a refusal arrives only after the server's fixed 5 s backoff)",
				topic, st2, reason2, took2.Round(time.Millisecond)))

		// The refusal must be the SAME refusal a non-member gets — not some other error that merely
		// contains the word Unauthorized.
		check("h-"+variant+"-same-as-nonmember", st2 == oSt && reason2 == oReason && reason2 != "",
			fmt.Sprintf("the revoked principal's refusal is byte-identical to the non-member baseline taken on this same topic: equal=%v; revoked=%q vs non-member=%q",
				st2 == oSt && reason2 == oReason, reason2, oReason))

		// Positive control at the SAME moment: an untouched, still-active member joins its own topic.
		// Without this, every refusal above could be a realtime server that stopped authorizing anyone.
		hSt, hReason, _ := joinOnce("health-"+variant, "realtime:brigade:session:"+sndSid, sndP.tk.jwt())
		check("h-"+variant+"-server-healthy", hSt == "ok",
			fmt.Sprintf("CONTROL taken immediately after the refusal: an untouched active member still joins its own topic status=%q reason=%q — so the Unauthorized above is this principal's revocation, not a server that refuses everyone",
				hSt, hReason))
	}
}

// ================================================================ (i) soak mode
//
// NOT run by -mode=fast. Invocation:
//
//	cd /Users/rjae/Development/appshapes/brigade/.ignored/exp/E0-2 && go run . -mode=soak
//
// 30 minutes at vsn=1.0.0: heartbeat every 25 s (the server closes a silent socket after
// ~66 s), one message every 10 s round-robin over four sender sessions, drain on every
// broadcast plus a 5 s safety timer, two explicit token refreshes with their access_token
// pushes at T+10m and T+20m (the JWT lives 3600 s, so no natural expiry occurs inside the
// window), and a `supabase stop` / `supabase start` at T+25m driven from inside the soak,
// with messages deliberately left undrained across the restart so the rejoin's drain has
// something to recover. Progress lines are printed every 30 s. Exit non-zero on any failure.

func soakLog(t0 time.Time, format string, a ...any) {
	fmt.Printf("[%s | T+%6.1fs] %s\n", time.Now().Format("15:04:05"), time.Since(t0).Seconds(), fmt.Sprintf(format, a...))
}

func runSoak(ctx context.Context, env Env, dur time.Duration) {
	const vsn = "1.0.0"
	t0 := time.Now()
	tag := randHex(3)
	section(fmt.Sprintf("(i) soak: %s at vsn=%s", dur, vsn))

	// ---------------- fixtures ----------------
	recip := newPrincipalQuiet(ctx, env)
	teamID, secret := createTeamQuiet(ctx, env, recip, "E02 soak "+tag, "soak-recipient")
	sidR := registerSessionQuiet(ctx, env, recip, teamID, "e02-soak-recipient")
	var sd []sender
	for i := 0; i < 4; i++ {
		p, sid := newMember(ctx, env, teamID, secret, fmt.Sprintf("soak-s%d", i), fmt.Sprintf("e02-soak-s%d", i))
		sd = append(sd, sender{p, sid})
	}
	topic := "realtime:brigade:session:" + sidR
	soakLog(t0, "fixtures ready: team=%s recipient_session=%s senders=%d", teamID, sidR, len(sd))

	d := newDrainer(ctx, env, recip.tk, sidR)
	stopDrain := make(chan struct{})
	go d.run(stopDrain, 5*time.Second)
	defer close(stopDrain)

	// ---------------- connection state ----------------
	var connMu sync.Mutex
	var p *Phx
	var s *sub
	var bstop chan struct{}

	var stats struct {
		mu           sync.Mutex
		sent         int
		sendErrs     int
		broadcasts   int
		heartbeats   int
		hbMissed     int
		reconnects   int
		joinFails    int
		sendErrList  []string
		lastBroadcst time.Time
	}

	watchBroadcasts := func(sw *sub, stopCh chan struct{}) {
		from := 0
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			_, next, got := sw.waitFrom(from, 500*time.Millisecond, func(m Msg) bool { _, _, ok := broadcastID(m); return ok })
			from = next
			if got {
				stats.mu.Lock()
				stats.broadcasts++
				stats.lastBroadcst = time.Now()
				stats.mu.Unlock()
				d.kick()
			}
		}
	}

	// connect dials, joins and starts the broadcast watcher. Backoff 1,2,5,10 s (realtime-js).
	connect := func(reason string) bool {
		connMu.Lock()
		if bstop != nil {
			close(bstop)
			bstop = nil
		}
		if p != nil {
			p.close()
			p = nil
		}
		connMu.Unlock()
		backoff := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
		for i := 0; i < 40; i++ {
			np, err := dial(ctx, env, env.PublishableKey, vsn, "soak")
			if err != nil {
				soakLog(t0, "connect (%s) attempt %d: dial failed: %v", reason, i, err)
			} else {
				ns := newSub(np, "soak")
				rep, ok := ns.join(ctx, topic, JoinConfig{Private: true, AccessToken: recip.tk.jwt()}, 20*time.Second)
				st, rsn := replyStatus(rep)
				if ok && st == "ok" {
					stopCh := make(chan struct{})
					connMu.Lock()
					p, s, bstop = np, ns, stopCh
					connMu.Unlock()
					go watchBroadcasts(ns, stopCh)
					soakLog(t0, "connect (%s) attempt %d: joined %s", reason, i, topic)
					d.kick()
					return true
				}
				soakLog(t0, "connect (%s) attempt %d: join refused status=%q reason=%q", reason, i, st, rsn)
				stats.mu.Lock()
				stats.joinFails++
				stats.mu.Unlock()
				np.close()
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(backoff[min(i, len(backoff)-1)]):
			}
		}
		return false
	}

	if !connect("initial") {
		check("i-initial-join", false, "could not join the recipient topic at the start of the soak")
		return
	}
	check("i-initial-join", true, "joined "+topic+" at vsn="+vsn)

	// ---------------- tickers ----------------
	sendT := time.NewTicker(10 * time.Second)
	hbT := time.NewTicker(25 * time.Second)
	progT := time.NewTicker(30 * time.Second)
	defer sendT.Stop()
	defer hbT.Stop()
	defer progT.Stop()

	sendIdx := 0
	sendOneSoak := func(note string) sendResult {
		sn := sd[sendIdx%len(sd)]
		sendIdx++
		r := sendWithBackoff(ctx, env, sn.p.tk, sn.sid, sidR, "soak "+note,
			fmt.Sprintf("e02-%s-soak-%d", tag, sendIdx), d, 8)
		stats.mu.Lock()
		if r.err == "" {
			stats.sent++
		} else {
			stats.sendErrs++
			if len(stats.sendErrList) < 10 {
				stats.sendErrList = append(stats.sendErrList,
					fmt.Sprintf("T+%.0fs %s: %s", time.Since(t0).Seconds(), note, r.err))
			}
		}
		stats.mu.Unlock()
		return r
	}

	// deliverySince: send one message and require the drain to yield it within `within`.
	deliverySince := func(id string, note string, within time.Duration) (bool, time.Duration) {
		tStart := time.Now()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			d.kick()
			d.mu.Lock()
			_, ok := d.seen[id]
			d.mu.Unlock()
			if ok {
				return true, time.Since(tStart)
			}
			time.Sleep(250 * time.Millisecond)
		}
		_ = note
		return false, time.Since(tStart)
	}

	// milestones
	type milestone struct {
		at   time.Duration
		name string
		fn   func()
	}
	refreshAndPush := func(n int) {
		soakLog(t0, "milestone: token refresh #%d + access_token push", n)
		ns, rr := refreshQuiet(ctx, env, env.PublishableKey, recip.tk.session().RefreshToken)
		if rr.Status != 200 {
			check(fmt.Sprintf("i-refresh%d", n), false, fmt.Sprintf("refresh #%d failed: HTTP %d %s", n, rr.Status, redact(string(rr.Body))))
			return
		}
		old := recip.tk.jwt()
		recip.tk.set(ns)
		claims := jwtClaims(ns.AccessToken)
		connMu.Lock()
		cs := s
		connMu.Unlock()
		var pushErr error
		down := false
		if cs != nil {
			from := cs.mark()
			pushErr = cs.pushAccessToken(ctx, ns.AccessToken)
			// The push re-runs brigade.owns_session_topic immediately; an active member must NOT be dropped.
			if _, _, got := cs.waitFrom(from, 3*time.Second, func(m Msg) bool {
				if m.Event == "phx_close" || m.Event == "phx_error" {
					return true
				}
				if m.Event == "system" {
					sst, _ := jsonGet[string](m.Payload, "status")
					return sst == "error"
				}
				return false
			}); got {
				down = true
			}
			if c, _ := cs.closed(); c {
				down = true
			}
		}
		// Send AFTER the push and require BOTH paths: a broadcast on the channel the new token now
		// authorizes (proving the channel is functionally alive, not merely un-errored) and the drain.
		bFrom := 0
		if cs != nil {
			bFrom = cs.mark()
		}
		r := sendOneSoak(fmt.Sprintf("after refresh #%d", n))
		gotB := false
		if cs != nil && r.err == "" {
			_, _, gotB = cs.waitFrom(bFrom, 15*time.Second, func(m Msg) bool {
				id, _, ok := broadcastID(m)
				return ok && id == r.messageID
			})
		}
		ok, took := false, time.Duration(0)
		if r.err == "" {
			ok, took = deliverySince(r.messageID, "post-refresh", 30*time.Second)
		}
		changed := ns.AccessToken != old
		check(fmt.Sprintf("i-refresh%d", n), rr.Status == 200 && changed && pushErr == nil && !down && r.err == "" && gotB && ok,
			fmt.Sprintf("refresh #%d: the access token really changed=%v (exp=%v), the access_token push was written (err=%v), the channel stayed up=%v, and the NEXT message arrived both as a broadcast on that same channel=%v and through the drain=%v in %s",
				n, changed, claims["exp"], pushErr, !down, gotB, ok, took.Round(time.Millisecond)))
	}

	restart := func() {
		soakLog(t0, "milestone: stack restart. Pausing the drain and sending 5 messages that must survive the restart.")
		d.setPaused(true)
		var pending []string
		for i := 0; i < 5; i++ {
			r := sendOneSoak(fmt.Sprintf("pre-restart %d", i))
			if r.err == "" {
				pending = append(pending, r.messageID)
			}
		}
		soakLog(t0, "%d messages left undrained; running `supabase stop`", len(pending))

		runCmd := func(args ...string) (string, error) {
			full := append(append([]string{}, strings.Fields(supabaseCmd)...), args...)
			c := exec.CommandContext(ctx, full[0], full[1:]...)
			c.Dir = projectDir
			soakLog(t0, "exec: %s (cwd %s)", strings.Join(full, " "), projectDir)
			out, err := c.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		outStop, errStop := runCmd("stop")
		soakLog(t0, "supabase stop -> err=%v out=%s", errStop, truncate(outStop, 500))
		time.Sleep(5 * time.Second)
		tStart := time.Now()
		// the same minimal service set the stack was started with (plan 5.9)
		outStart, errStart := runCmd("start", "-x", excluded)
		soakLog(t0, "supabase start -> err=%v (%s) out=%s", errStart, time.Since(tStart).Round(time.Second), truncate(outStart, 800))

		// wait for the REST endpoint to answer again
		up := false
		for i := 0; i < 120; i++ {
			rr := doQuiet(ctx, "GET", env.APIURL+"/auth/v1/health", map[string]string{"apikey": env.PublishableKey}, nil)
			if rr.Status == 200 {
				up = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		check("i-restart-stack", errStop == nil && errStart == nil && up,
			fmt.Sprintf("supabase stop err=%v, supabase start err=%v, API healthy again=%v", errStop, errStart, up))

		stats.mu.Lock()
		stats.reconnects++
		stats.mu.Unlock()
		rejoined := connect("after stack restart")
		check("i-restart-rejoin", rejoined, fmt.Sprintf("rejoined %s after the restart", topic))

		d.setPaused(false)
		allBack := false
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			d.kick()
			d.mu.Lock()
			n := 0
			for _, id := range pending {
				if d.seen[id] > 0 {
					n++
				}
			}
			d.mu.Unlock()
			if n == len(pending) {
				allBack = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		check("i-restart-drain", allBack && len(pending) == 5,
			fmt.Sprintf("the %d messages sent before `supabase stop` and deliberately left undrained were recovered by the drain after the rejoin: %v", len(pending), allBack))

		// Delivery must resume on BOTH paths. Requiring only the drain would let a permanently dead
		// broadcast path pass the restart milestone, since the drain's 5 s safety timer would cover it.
		connMu.Lock()
		cs := s
		connMu.Unlock()
		bFrom := 0
		if cs != nil {
			bFrom = cs.mark()
		}
		r := sendOneSoak("after restart")
		gotB := false
		if cs != nil && r.err == "" {
			_, _, gotB = cs.waitFrom(bFrom, 20*time.Second, func(m Msg) bool {
				id, _, ok := broadcastID(m)
				return ok && id == r.messageID
			})
		}
		ok, took := false, time.Duration(0)
		if r.err == "" {
			ok, took = deliverySince(r.messageID, "post-restart", 30*time.Second)
		}
		check("i-restart-delivery", r.err == "" && gotB && ok,
			fmt.Sprintf("delivery resumes after the restart on both paths: send err=%q, broadcast on the rejoined channel=%v, drained=%v in %s",
				r.err, gotB, ok, took.Round(time.Millisecond)))
	}

	ms := []milestone{
		{time.Duration(float64(dur) * 10.0 / 30.0), "refresh #1", func() { refreshAndPush(1) }},
		{time.Duration(float64(dur) * 20.0 / 30.0), "refresh #2", func() { refreshAndPush(2) }},
	}
	if soakRestart {
		ms = append(ms, milestone{time.Duration(float64(dur) * 25.0 / 30.0), "stack restart", restart})
	} else {
		soakLog(t0, "-soak-restart=false: the supabase stop/start milestone is SKIPPED")
	}
	msDone := make([]bool, len(ms))

	firstConnect := time.Now()
	var unexpectedDrops int
	for {
		if time.Since(t0) >= dur {
			break
		}
		select {
		case <-ctx.Done():
			check("i-context", false, "the soak context expired before the full duration elapsed")
			return
		case <-sendT.C:
			sendOneSoak("steady")
		case <-hbT.C:
			connMu.Lock()
			cs := s
			connMu.Unlock()
			if cs == nil {
				break
			}
			if cs.heartbeat(ctx, 10*time.Second) {
				stats.mu.Lock()
				stats.heartbeats++
				stats.mu.Unlock()
			} else {
				stats.mu.Lock()
				stats.hbMissed++
				stats.mu.Unlock()
				soakLog(t0, "heartbeat MISSED — reconnecting")
				unexpectedDrops++
				connect("missed heartbeat")
			}
		case <-progT.C:
			stats.mu.Lock()
			connMu.Lock()
			cs := s
			connMu.Unlock()
			alive := "up"
			if cs == nil {
				alive = "no socket"
			} else if c, _ := cs.closed(); c {
				alive = "CLOSED"
			}
			soakLog(t0, "progress: socket=%s sent=%d send_err=%d broadcasts=%d drained=%d heartbeats=%d(missed %d) reconnects=%d | next milestone: %s",
				alive, stats.sent, stats.sendErrs, stats.broadcasts, d.count(), stats.heartbeats, stats.hbMissed, stats.reconnects,
				func() string {
					for i, m := range ms {
						if !msDone[i] {
							return fmt.Sprintf("%s in %s", m.name, (m.at - time.Since(t0)).Round(time.Second))
						}
					}
					return fmt.Sprintf("finish in %s", (dur - time.Since(t0)).Round(time.Second))
				}())
			stats.mu.Unlock()
			// detect a socket that went down outside a milestone and rejoin
			if cs != nil {
				if c, _ := cs.closed(); c {
					unexpectedDrops++
					soakLog(t0, "socket found closed outside a milestone — reconnecting")
					connect("socket closed")
				}
			}
		case <-time.After(time.Second):
		}
		for i, m := range ms {
			if !msDone[i] && time.Since(t0) >= m.at {
				msDone[i] = true
				m.fn()
			}
		}
	}

	// ---------------- final assertions ----------------
	soakLog(t0, "soak window over; final drain")
	d.setPaused(false)
	for i := 0; i < 10; i++ {
		d.kick()
		time.Sleep(500 * time.Millisecond)
	}
	stats.mu.Lock()
	sent, sendErrs, hb, hbMissed, bc, rc := stats.sent, stats.sendErrs, stats.heartbeats, stats.hbMissed, stats.broadcasts, stats.reconnects
	sendErrList := append([]string(nil), stats.sendErrList...)
	lastB := stats.lastBroadcst
	stats.mu.Unlock()
	order, dups := d.snapshot()

	dbCount := psqlMust(fmt.Sprintf("select count(*) from brigade.messages where recipient_session_id='%s'", sidR))
	dbAccepted := psqlMust(fmt.Sprintf("select count(*) from brigade.messages where recipient_session_id='%s' and delivery_state='accepted'", sidR))

	check("i-duration", time.Since(t0) >= dur,
		fmt.Sprintf("the soak ran for %s (target %s), first connect at T+%s", time.Since(t0).Round(time.Second), dur, firstConnect.Sub(t0).Round(time.Second)))
	check("i-heartbeat", hbMissed == 0 && hb > 0,
		fmt.Sprintf("%d heartbeats answered on a 25 s cadence, %d missed (the server closes a socket silent for ~66 s)", hb, hbMissed))
	// A soak that sent nothing would satisfy "everything sent was drained" vacuously. The floor is
	// derived from the 10 s send cadence, discounted for the milestones that pause the ticker.
	wantSent := int(dur/(10*time.Second)) * 6 / 10
	check("i-workload", sent >= wantSent && sent > 0,
		fmt.Sprintf("the soak actually did work: %d messages sent over %s on a 10 s cadence (floor for this duration: %d). Without this, \"everything sent was delivered\" could be true of a soak that sent nothing",
			sent, dur, wantSent))

	check("i-delivery", sendErrs == 0 && len(dups) == 0 && len(order) == sent && dbAccepted == "0",
		fmt.Sprintf("%d messages sent (%d send errors: %v), drain yielded %d envelopes with %d duplicates; %s rows in the database for the session, %s still 'accepted'",
			sent, sendErrs, func() any {
				if len(sendErrList) == 0 {
					return "none"
				}
				return sendErrList
			}(), len(order), len(dups), dbCount, dbAccepted))

	// The drain alone would keep i-delivery green even if the broadcast path died at minute one — the
	// 5 s safety timer would quietly do all the work. These two assertions are what make the soak a
	// test of broadcast-from-the-database rather than of polling.
	bcFloor := sent * 8 / 10
	check("i-broadcasts", bc > 0 && bc >= bcFloor,
		fmt.Sprintf("the BROADCAST path carried the traffic, not just the drain's safety timer: %d message_accepted broadcasts for %d messages sent (floor %d = 80%%); %d reconnects, %d unexpected socket drops",
			bc, sent, bcFloor, rc, unexpectedDrops))
	sinceLastB := time.Duration(-1)
	if !lastB.IsZero() {
		sinceLastB = time.Since(lastB)
	}
	check("i-broadcasts-to-the-end", !lastB.IsZero() && sinceLastB < 90*time.Second,
		fmt.Sprintf("broadcasts were still arriving at the END of the soak: the last one landed %s ago (a broadcast path that died mid-soak would show a large gap here while the drain kept i-delivery green)",
			sinceLastB.Round(time.Second)))
	check("i-no-unexpected-drops", unexpectedDrops == 0,
		fmt.Sprintf("the socket survived the whole window outside the deliberate restart: %d unexpected drops, %d missed heartbeats, %d reconnects (1 expected if the stack restart ran)",
			unexpectedDrops, hbMissed, rc))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
