package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Msg is one Phoenix message in either serializer version.
type Msg struct {
	JoinRef *string         `json:"join_ref"`
	Ref     *string         `json:"ref"`
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Binary  bool            `json:"-"` // arrived as a binary frame (vsn 2.0.0 user broadcast)
	At      time.Time       `json:"-"`
}

func (m Msg) String() string {
	jr, r := "null", "null"
	if m.JoinRef != nil {
		jr = *m.JoinRef
	}
	if m.Ref != nil {
		r = *m.Ref
	}
	kind := "text"
	if m.Binary {
		kind = "BINARY"
	}
	return fmt.Sprintf("[%s] join_ref=%s ref=%s topic=%s event=%s payload=%s", kind, jr, r, m.Topic, m.Event, redact(string(m.Payload)))
}

// Phx is a minimal Phoenix-channels client over coder/websocket.
type Phx struct {
	c      *websocket.Conn
	vsn    string
	refN   int
	mu     sync.Mutex
	in     chan Msg
	closed chan error
	name   string
	start  time.Time
}

func dial(ctx context.Context, env Env, apikey, vsn, name string) (*Phx, error) {
	u := strings.Replace(env.APIURL, "http://", "ws://", 1) + "/realtime/v1/websocket?apikey=" + apikey
	if vsn != "" {
		u += "&vsn=" + vsn
	}
	fmt.Printf("\n--- [%s] dial %s\n", name, redact(u))
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{})
	if resp != nil {
		fmt.Printf("< upgrade HTTP %d", resp.StatusCode)
		for _, h := range []string{"Sec-Websocket-Accept", "Sec-Websocket-Protocol", "Server", "Content-Type"} {
			if v := resp.Header.Get(h); v != "" {
				fmt.Printf(" %s=%q", h, v)
			}
		}
		fmt.Println()
	}
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, fmt.Errorf("dial: %w (status %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("dial: %w", err)
	}
	c.SetReadLimit(1 << 20)
	p := &Phx{c: c, vsn: vsn, in: make(chan Msg, 4096), closed: make(chan error, 1), name: name, start: time.Now()}
	go p.readLoop()
	return p, nil
}

func (p *Phx) ts() string { return fmt.Sprintf("%7.3fs", time.Since(p.start).Seconds()) }

func (p *Phx) readLoop() {
	for {
		typ, data, err := p.c.Read(context.Background())
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				fmt.Printf("%s [%s] << SOCKET CLOSED by peer: code=%d reason=%q\n", p.ts(), p.name, ce.Code, ce.Reason)
			} else {
				fmt.Printf("%s [%s] << read error: %v\n", p.ts(), p.name, err)
			}
			p.closed <- err
			close(p.in)
			return
		}
		m, perr := p.decode(typ, data)
		if perr != nil {
			fmt.Printf("%s [%s] << undecodable %v frame (%d bytes): %s err=%v\n", p.ts(), p.name, typ, len(data), redact(string(data)), perr)
			continue
		}
		m.At = time.Now()
		fmt.Printf("%s [%s] << %s\n", p.ts(), p.name, m.String())
		p.in <- m
	}
}

func (p *Phx) decode(typ websocket.MessageType, data []byte) (Msg, error) {
	if typ == websocket.MessageBinary {
		return decodeBinaryBroadcast(data)
	}
	if p.vsn == "2.0.0" {
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil {
			return Msg{}, fmt.Errorf("v2 array: %w (raw: %s)", err, string(data))
		}
		if len(arr) != 5 {
			return Msg{}, fmt.Errorf("v2 array has %d elements", len(arr))
		}
		var m Msg
		_ = json.Unmarshal(arr[0], &m.JoinRef)
		_ = json.Unmarshal(arr[1], &m.Ref)
		_ = json.Unmarshal(arr[2], &m.Topic)
		_ = json.Unmarshal(arr[3], &m.Event)
		m.Payload = arr[4]
		return m, nil
	}
	var m Msg
	if err := json.Unmarshal(data, &m); err != nil {
		return Msg{}, fmt.Errorf("v1 object: %w (raw: %s)", err, string(data))
	}
	return m, nil
}

// decodeBinaryBroadcast mirrors realtime-js Serializer._decodeUserBroadcast (kind 4).
func decodeBinaryBroadcast(b []byte) (Msg, error) {
	if len(b) < 5 {
		return Msg{}, errors.New("short binary frame")
	}
	kind := b[0]
	if kind != 4 {
		return Msg{}, fmt.Errorf("unknown binary kind %d", kind)
	}
	topicLen, evLen, metaLen, enc := int(b[1]), int(b[2]), int(b[3]), b[4]
	off := 5
	if len(b) < off+topicLen+evLen+metaLen {
		return Msg{}, errors.New("truncated binary frame")
	}
	topic := string(b[off : off+topicLen])
	off += topicLen
	ev := string(b[off : off+evLen])
	off += evLen
	meta := b[off : off+metaLen]
	off += metaLen
	payload := b[off:]
	out := map[string]any{"type": "broadcast", "event": ev}
	if enc == 1 {
		var pj any
		if err := json.Unmarshal(payload, &pj); err != nil {
			return Msg{}, fmt.Errorf("binary payload json: %w", err)
		}
		out["payload"] = pj
	} else {
		out["payload"] = fmt.Sprintf("<%d raw bytes>", len(payload))
	}
	if metaLen > 0 {
		var mj any
		_ = json.Unmarshal(meta, &mj)
		out["meta"] = mj
	}
	pb, _ := json.Marshal(out)
	return Msg{Topic: topic, Event: "broadcast", Payload: pb, Binary: true}, nil
}

func (p *Phx) nextRef() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refN++
	return strconv.Itoa(p.refN)
}

// push sends one message; joinRef nil for socket-level messages (heartbeat) and for the join itself
// realtime-js/phoenix sets join_ref = ref of the join push.
func (p *Phx) push(ctx context.Context, joinRef *string, topic, event string, payload any) (string, error) {
	ref := p.nextRef()
	pb, _ := json.Marshal(payload)
	var wire []byte
	if p.vsn == "2.0.0" {
		arr := []any{joinRef, ref, topic, event, json.RawMessage(pb)}
		wire, _ = json.Marshal(arr)
	} else {
		obj := map[string]any{"topic": topic, "event": event, "payload": json.RawMessage(pb), "ref": ref}
		if joinRef != nil {
			obj["join_ref"] = *joinRef
		}
		wire, _ = json.Marshal(obj)
	}
	fmt.Printf("%s [%s] >> %s\n", p.ts(), p.name, redact(string(wire)))
	return ref, p.c.Write(ctx, websocket.MessageText, wire)
}

func (p *Phx) pushRaw(ctx context.Context, typ websocket.MessageType, data []byte) error {
	fmt.Printf("%s [%s] >> RAW(%v) %s\n", p.ts(), p.name, typ, redact(string(data)))
	return p.c.Write(ctx, typ, data)
}

// waitFor drains incoming messages until pred matches or the timeout expires.
func (p *Phx) waitFor(timeout time.Duration, pred func(Msg) bool) (Msg, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-p.in:
			if !ok {
				return Msg{}, false
			}
			if pred(m) {
				return m, true
			}
		case <-deadline:
			return Msg{}, false
		}
	}
}

func (p *Phx) waitReply(ref string, timeout time.Duration) (Msg, bool) {
	return p.waitFor(timeout, func(m Msg) bool { return m.Event == "phx_reply" && m.Ref != nil && *m.Ref == ref })
}

func (p *Phx) heartbeat(ctx context.Context) (Msg, bool) {
	ref, err := p.push(ctx, nil, "phoenix", "heartbeat", map[string]any{})
	if err != nil {
		fmt.Printf("heartbeat write error: %v\n", err)
		return Msg{}, false
	}
	return p.waitReply(ref, 5*time.Second)
}

type JoinConfig struct {
	Private     bool
	Ack         bool
	Self        bool
	AccessToken string
}

// joinPayload mirrors RealtimeChannel.subscribe(): {config:{broadcast,presence,postgres_changes,private}, access_token}.
func joinPayload(jc JoinConfig) map[string]any {
	p := map[string]any{
		"config": map[string]any{
			"broadcast":        map[string]any{"ack": jc.Ack, "self": jc.Self},
			"presence":         map[string]any{"key": "", "enabled": false},
			"postgres_changes": []any{},
			"private":          jc.Private,
		},
	}
	if jc.AccessToken != "" {
		p["access_token"] = jc.AccessToken
	}
	return p
}

// join pushes phx_join and returns (joinRef, reply, ok).
func (p *Phx) join(ctx context.Context, topic string, jc JoinConfig, timeout time.Duration) (string, Msg, bool) {
	// realtime-js: the join push's join_ref equals its own ref.
	ref := p.nextRef()
	pb, _ := json.Marshal(joinPayload(jc))
	var wire []byte
	if p.vsn == "2.0.0" {
		wire, _ = json.Marshal([]any{ref, ref, topic, "phx_join", json.RawMessage(pb)})
	} else {
		wire, _ = json.Marshal(map[string]any{"topic": topic, "event": "phx_join", "payload": json.RawMessage(pb), "ref": ref, "join_ref": ref})
	}
	fmt.Printf("%s [%s] >> %s\n", p.ts(), p.name, redact(string(wire)))
	if err := p.c.Write(ctx, websocket.MessageText, wire); err != nil {
		fmt.Printf("join write error: %v\n", err)
		return ref, Msg{}, false
	}
	m, ok := p.waitReply(ref, timeout)
	return ref, m, ok
}

func (p *Phx) close() {
	_ = p.c.Close(websocket.StatusNormalClosure, "done")
}
