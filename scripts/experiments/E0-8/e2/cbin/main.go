// E0-8 (c): a real `brigade` CLI, small but honest -- it actually talks to the
// local Supabase stack over loopback, so a sandbox verdict is the server's or
// the kernel's, never a mock's.
//
// Commands
//
//	bootstrap        (run UNSANDBOXED) anonymous sign-up, create a team, register
//	                 two sessions, write profile.json + session.json
//	sessions --json  RPC brigade.list_sessions for the profile's team
//	send <to> <body> RPC brigade.send_message from this profile's session
//	whoami           report the token's expiry state and the environment
//
// The credential path is the one the plan specifies: refresh when fewer than 90 s
// remain or after a PGRST303/401, then try to persist. When the profile directory
// cannot be written -- exactly the read-only-home case the Bash sandbox creates --
// the refresh stays IN MEMORY, the command still completes with the new access
// token, and `persisted:false` is reported rather than hidden.
//
// Every run prints one JSON object with the whole diagnosis: the proxy variables
// it was handed, whether it refreshed, whether it persisted, the HTTP statuses,
// and the transport error verbatim if there was one. That object is the datum.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Profile struct {
	APIURL string `json:"api_url"`
	Key    string `json:"publishable_key"`
	TeamID string `json:"team_id"`
	SelfID string `json:"self_session_id"`
	PeerID string `json:"peer_session_id"`
}

type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int    `json:"expires_in"`
	User         struct {
		ID string `json:"id"`
	} `json:"user"`
}

type Diag struct {
	Command      string            `json:"command"`
	OK           bool              `json:"ok"`
	Steps        []Step            `json:"steps"`
	Refreshed    bool              `json:"refreshed"`
	Persisted    *bool             `json:"persisted"`
	PersistError string            `json:"persist_error,omitempty"`
	Env          map[string]string `json:"env"`
	HomeWritable bool              `json:"home_writable"`
	ProfileDir   string            `json:"profile_dir"`
	Result       json.RawMessage   `json:"result,omitempty"`
	Error        string            `json:"error,omitempty"`
	TokenExpiry  string            `json:"token_expiry"`
	ProbeWriteProfile string       `json:"probe_write_profile_dir,omitempty"`
	ProbeWriteCwd     string       `json:"probe_write_cwd,omitempty"`
	SecondsLeft  int64             `json:"token_seconds_left"`
}

type Step struct {
	Name     string  `json:"name"`
	Method   string  `json:"method"`
	URL      string  `json:"url"`
	Status   int     `json:"status"`
	Ms       float64 `json:"ms"`
	Err      string  `json:"err,omitempty"`
	BodyCut  string  `json:"body,omitempty"`
	ViaProxy string  `json:"via_proxy,omitempty"`
}

var forceProxy = false
var clientTimeout = 20 * time.Second

func client() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if forceProxy {
		// Ignore NO_PROXY: the Bash sandbox sets NO_PROXY=localhost,127.0.0.1,::1,
		// which makes Go dial loopback DIRECTLY. Forcing the proxy separates
		// "the sandbox refuses the direct connect" from "the proxy refuses the host".
		p := firstNonEmpty(os.Getenv("HTTPS_PROXY"), os.Getenv("https_proxy"),
			os.Getenv("HTTP_PROXY"), os.Getenv("http_proxy"))
		if p != "" {
			if u, err := url.Parse(p); err == nil {
				tr.Proxy = func(*http.Request) (*url.URL, error) { return u, nil }
			}
		}
	}
	return &http.Client{Transport: tr, Timeout: clientTimeout}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func do(d *Diag, name, method, u string, h map[string]string, body any) (int, []byte) {
	var bb []byte
	if body != nil {
		bb, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(bb))
	if err != nil {
		d.Steps = append(d.Steps, Step{Name: name, Method: method, URL: u, Err: err.Error()})
		return -1, nil
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	c := client()
	via := ""
	if tr, ok := c.Transport.(*http.Transport); ok && tr.Proxy != nil {
		if pu, _ := tr.Proxy(req); pu != nil {
			via = pu.String()
		}
	}
	t0 := time.Now()
	resp, err := c.Do(req)
	ms := float64(time.Since(t0).Microseconds()) / 1000
	if err != nil {
		d.Steps = append(d.Steps, Step{Name: name, Method: method, URL: u, Ms: ms,
			Err: err.Error(), ViaProxy: via})
		return -1, nil
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	cut := string(rb)
	if len(cut) > 400 {
		cut = cut[:400] + "…"
	}
	d.Steps = append(d.Steps, Step{Name: name, Method: method, URL: u, Status: resp.StatusCode,
		Ms: ms, BodyCut: cut, ViaProxy: via})
	return resp.StatusCode, rb
}

func profileDir() string {
	if v := os.Getenv("BRIGADE_CONFIG_DIR"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "brigade", "profiles", "default")
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeAtomic(dir, name string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

func jwtExp(tok string) int64 {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 0
	}
	if f, ok := m["exp"].(float64); ok {
		return int64(f)
	}
	return 0
}

func mintExpired(secret string, tok string) string {
	parts := strings.Split(tok, ".")
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var m map[string]any
	_ = json.Unmarshal(pb, &m)
	now := time.Now().Unix()
	m["iat"] = now - 3600
	m["exp"] = now - 600 // ten minutes stale: unambiguously expired
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	nb, _ := json.Marshal(m)
	pl := base64.RawURLEncoding.EncodeToString(nb)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(hdr + "." + pl))
	return hdr + "." + pl + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func envSnapshot() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
		"NO_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "TMPDIR", "HOME",
		"CLAUDE_PID", "BRIGADE_CONFIG_DIR"} {
		out[k] = os.Getenv(k)
	}
	return out
}

func homeWritable() bool {
	d := profileDir()
	f, err := os.CreateTemp(d, ".wtest.*")
	if err != nil {
		return false
	}
	n := f.Name()
	f.Close()
	os.Remove(n)
	return true
}

func rpc(d *Diag, p Profile, jwt, fn string, args any) (int, []byte) {
	return do(d, "rpc:"+fn, "POST", p.APIURL+"/rest/v1/rpc/"+fn, map[string]string{
		"apikey":          p.Key,
		"Authorization":   "Bearer " + jwt,
		"Content-Type":    "application/json",
		"Accept-Profile":  "brigade",
		"Content-Profile": "brigade",
		"X-Client-Info":   "brigade-e08c/0",
	}, args)
}

func refresh(d *Diag, p Profile, s Session) (Session, bool) {
	st, body := do(d, "gotrue:refresh", "POST",
		p.APIURL+"/auth/v1/token?grant_type=refresh_token",
		map[string]string{"apikey": p.Key, "Content-Type": "application/json;charset=UTF-8",
			"X-Supabase-Api-Version": "2024-01-01"},
		map[string]string{"refresh_token": s.RefreshToken})
	if st != 200 {
		return s, false
	}
	var ns Session
	if json.Unmarshal(body, &ns) != nil || ns.AccessToken == "" {
		return s, false
	}
	if ns.ExpiresAt == 0 {
		ns.ExpiresAt = jwtExp(ns.AccessToken)
	}
	return ns, true
}

func emit(d *Diag, code int) {
	b, _ := json.MarshalIndent(d, "", "  ")
	fmt.Println(string(b))
	os.Exit(code)
}

func main() {
	args := os.Args[1:]
	var rest []string
	for _, a := range args {
		switch a {
		case "--json":
		case "--force-proxy":
			forceProxy = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Println(`{"error":"usage: brigade <bootstrap|sessions|send|whoami>"}`)
		os.Exit(2)
	}
	cmd := rest[0]
	dir := profileDir()
	d := &Diag{Command: cmd, Env: envSnapshot(), ProfileDir: dir}

	if cmd == "bootstrap" {
		bootstrap(d)
		return
	}

	var p Profile
	if err := readJSON(filepath.Join(dir, "profile.json"), &p); err != nil {
		d.Error = "read profile: " + err.Error()
		emit(d, 3)
	}
	var s Session
	if err := readJSON(filepath.Join(dir, "session.json"), &s); err != nil {
		d.Error = "read session: " + err.Error()
		emit(d, 3)
	}
	d.HomeWritable = homeWritable()
	exp := s.ExpiresAt
	if exp == 0 {
		exp = jwtExp(s.AccessToken)
	}
	left := exp - time.Now().Unix()
	d.SecondsLeft = left
	d.TokenExpiry = time.Unix(exp, 0).Format(time.RFC3339)

	jwt := s.AccessToken
	tryRefresh := func(why string) bool {
		d.Steps = append(d.Steps, Step{Name: "refresh_trigger:" + why})
		ns, ok := refresh(d, p, s)
		if !ok {
			return false
		}
		s = ns
		jwt = ns.AccessToken
		d.Refreshed = true
		// Persist is BEST EFFORT. Under the Bash sandbox with a read-only home it
		// fails, and that is the interesting case: the command must still work.
		err := writeAtomic(dir, "session.json", ns)
		ok2 := err == nil
		d.Persisted = &ok2
		if err != nil {
			d.PersistError = err.Error()
			if errors.Is(err, os.ErrPermission) {
				d.PersistError += " (permission denied -- in-memory refresh only)"
			}
		}
		return true
	}
	if left < 90 {
		tryRefresh(fmt.Sprintf("expires_in_%ds", left))
	}

	// ---- WITHIN-RUN SANDBOX ENFORCEMENT CONTROL (adversarial pass) ----------
	// A cross-run allowlist comparison shows the allowlist matters, but it never
	// proves the sandbox was ENFORCING inside the very run that SUCCEEDED. This
	// does, in one process, in one Bash call:
	//   pos:  the allowlisted host must answer 200
	//   neg:  a NON-allowlisted host must be refused, same client, same proxy
	//   fs:   a write under the profile dir must fail while a write in the
	//         project cwd succeeds -- so "denied" is a scoped policy, not a
	//         broken binary.
	if cmd == "probe" {
		clientTimeout = 6 * time.Second
		// example.com is REACHABLE when nothing is enforcing, so a refusal here
		// is the sandbox and not a DNS or routing artefact.
		do(d, "neg:non-allowlisted-host", "GET", "http://example.com/", nil, nil)
		do(d, "neg:non-allowlisted-ip", "GET", "http://127.0.0.2:54321/auth/v1/health", nil, nil)
		clientTimeout = 20 * time.Second
		st, _ := do(d, "pos:allowlisted", "GET", p.APIURL+"/auth/v1/health",
			map[string]string{"apikey": p.Key}, nil)
		pw := filepath.Join(dir, ".e08-probe-write")
		if err := os.WriteFile(pw, []byte("x"), 0o600); err != nil {
			d.ProbeWriteProfile = "DENIED: " + err.Error()
		} else {
			d.ProbeWriteProfile = "WROTE"
			os.Remove(pw)
		}
		cw := ".e08-probe-write"
		if err := os.WriteFile(cw, []byte("x"), 0o600); err != nil {
			d.ProbeWriteCwd = "DENIED: " + err.Error()
		} else {
			d.ProbeWriteCwd = "WROTE"
			os.Remove(cw)
		}
		d.OK = st == 200
		emit(d, 0)
	}

	call := func() (int, []byte) {
		switch cmd {
		case "sessions":
			return rpc(d, p, jwt, "list_sessions", map[string]any{"p_team_id": p.TeamID})
		case "whoami":
			return rpc(d, p, jwt, "list_members", map[string]any{"p_team_id": p.TeamID})
		case "send":
			if len(rest) < 3 {
				return 0, nil
			}
			to := rest[1]
			if to == "peer" {
				to = p.PeerID
			}
			body := strings.Join(rest[2:], " ")
			if body == "-" {
				b, _ := io.ReadAll(os.Stdin)
				body = string(b)
			}
			return rpc(d, p, jwt, "send_message", map[string]any{
				"p_sender_session_id":    p.SelfID,
				"p_recipient_session_id": to,
				"p_body":                 body,
				"p_idempotency_key":      fmt.Sprintf("e08c-%d", time.Now().UnixNano()),
			})
		}
		return 0, nil
	}

	st, body := call()
	if st == 401 && !d.Refreshed && tryRefresh("http_401") {
		st, body = call()
	}
	if st == 200 {
		d.OK = true
		d.Result = json.RawMessage(body)
		emit(d, 0)
	}
	d.Error = fmt.Sprintf("final status %d", st)
	if len(body) > 0 {
		d.Result = json.RawMessage(body)
	}
	emit(d, 4)
}

// bootstrap runs UNSANDBOXED, from the harness, before any measurement.
func bootstrap(d *Diag) {
	apiURL := os.Getenv("BRIGADE_API_URL")
	key := os.Getenv("BRIGADE_KEY")
	dir := profileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.Error = err.Error()
		emit(d, 3)
	}
	p := Profile{APIURL: apiURL, Key: key}

	st, body := do(d, "gotrue:signup", "POST", apiURL+"/auth/v1/signup",
		map[string]string{"apikey": key, "Content-Type": "application/json;charset=UTF-8",
			"X-Supabase-Api-Version": "2024-01-01"},
		map[string]any{"data": map[string]any{}, "gotrue_meta_security": map[string]any{}})
	if st != 200 {
		d.Error = fmt.Sprintf("signup status %d", st)
		emit(d, 4)
	}
	var s Session
	_ = json.Unmarshal(body, &s)
	if s.ExpiresAt == 0 {
		s.ExpiresAt = jwtExp(s.AccessToken)
	}

	st, body = rpc(d, p, s.AccessToken, "create_team",
		map[string]any{"p_name": "e08c", "p_human_label": "probe"})
	if st != 200 {
		d.Error = fmt.Sprintf("create_team status %d", st)
		emit(d, 4)
	}
	var team struct {
		TeamID string `json:"team_id"`
	}
	_ = json.Unmarshal(body, &team)
	p.TeamID = team.TeamID

	reg := func(name string) string {
		st, body := rpc(d, p, s.AccessToken, "register_session",
			map[string]any{"p_team_id": p.TeamID, "p_name": name, "p_lease_seconds": 600})
		if st != 200 {
			d.Error = fmt.Sprintf("register_session(%s) status %d", name, st)
			emit(d, 4)
		}
		var r struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(body, &r)
		return r.SessionID
	}
	p.SelfID = reg("sandboxed-self")
	p.PeerID = reg("peer")

	if err := writeAtomic(dir, "profile.json", p); err != nil {
		d.Error = err.Error()
		emit(d, 3)
	}
	if err := writeAtomic(dir, "session.json", s); err != nil {
		d.Error = err.Error()
		emit(d, 3)
	}
	// A second copy of the session with a DELIBERATELY EXPIRED access token and
	// the same live refresh token, for the expired-token arm.
	if secret := os.Getenv("BRIGADE_JWT_SECRET"); secret != "" {
		es := s
		es.AccessToken = mintExpired(secret, s.AccessToken)
		es.ExpiresAt = time.Now().Unix() - 600
		if err := writeAtomic(dir, "session.expired.json", es); err != nil {
			d.Error = err.Error()
			emit(d, 3)
		}
	}
	d.OK = true
	b, _ := json.Marshal(p)
	d.Result = b
	emit(d, 0)
}
