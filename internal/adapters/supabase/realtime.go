package supabase

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
)

// Phoenix channels over coder/websocket (plan 5.6, ported from
// scripts/experiments/E0-2/phoenix.go, every frame shape measured against
// Realtime v2.129.3). The default serializer is vsn=1.0.0, under which
// every frame is one JSON object {"topic","event","payload","ref"
// [,"join_ref"]}; vsn=2.0.0 (the realtime-js default: a five-element
// array, and database broadcasts as binary frames in realtime-js's own
// framing) stays behind BRIGADE_SUPABASE_REALTIME_VSN, the test-only
// switch of 5.11, in case a Realtime release drops 1.0.0.
//
// The channel is a HINT, never a transport (D21): the private topic
// realtime:brigade:session:<id> carries ids-only broadcasts — a
// `message_accepted` for every accepted message, a `membership_revoked`
// from leave_team — and every hint, the join itself and a periodic timer
// all end in the same fetch_inbox drain (5.7). runLink is the goroutine
// that owns one socket for one join; it reports to the watch loop over a
// channel and never writes to stdout.

// Phoenix protocol constants.
const (
	phxVersionV1    = "1.0.0"
	phxVersionV2    = "2.0.0"
	phxSocketTopic  = "phoenix"
	phxTopicPrefix  = "realtime:brigade:session:"
	phxEventJoin    = "phx_join"
	phxEventLeave   = "phx_leave"
	phxEventReply   = "phx_reply"
	phxEventClose   = "phx_close"
	phxEventError   = "phx_error"
	phxEventSystem  = "system"
	phxEventHB      = "heartbeat"
	phxEventToken   = "access_token" //nolint:gosec // G101: a Phoenix event name, not a credential
	phxEventBcast   = "broadcast"
	phxWriteTimeout = 5 * time.Second
	phxLeaveTimeout = time.Second
)

// The broadcast events the finished migrations write on a session's
// topic: message_accepted from notify_message_inserted on every accepted
// message, membership_revoked from leave_team on each of the leaver's open
// sessions before they close. hintFor does not tell them apart — any
// broadcast on the topic means "drain now" and the RPC decides what the
// drain finds — so these names serve the tests and the reader.
const (
	broadcastMessage = "message_accepted"
	broadcastRevoked = "membership_revoked"
)

// The fixed reason tokens a link reports and the watch puts in a
// `status` event's detail. They are this adapter's words: the server's
// own reason text goes to stderr at debug through the redactor and never
// to stdout (4.3). The classification follows plan 5.6's reason table
// (names from https://supabase.com/docs/guides/realtime/error_codes).
const (
	linkReasonUnauthorized   = "unauthorized"      // Unauthorized, RlsPolicyError, "You do not have permissions"
	linkReasonRateLimited    = "rate_limited"      // *RateLimitReached
	linkReasonBadToken       = "bad_token"         // InvalidJWTToken, JwtSignatureError, MalformedJWT, "Token has expired"
	linkReasonPrivateOnly    = "private_only"      // PrivateOnly: the join was not private (a bug)
	linkReasonDisabled       = "realtime_disabled" // RealtimeDisabledForTenant, DatabaseLackOfConnections
	linkReasonUnmatchedTopic = "unmatched_topic"   // the realtime: prefix was omitted (a bug)
	linkReasonChannelError   = "channel_error"     // any other refusal or system error
	linkReasonDisconnected   = "disconnected"      // the socket closed or the channel was closed by the server
	linkReasonHeartbeat      = "heartbeat_timeout" // a phx heartbeat went unanswered for a whole interval
	linkReasonJoinTimeout    = "join_timeout"      // no phx_reply to the join in time
	linkReasonDial           = "dial_failed"       // the websocket upgrade failed
	linkReasonCredential     = "credential"        // no access token could be obtained for the join
)

// realtimeVersion is the serializer version in force: 1.0.0 unless the
// test-only switch says otherwise.
func realtimeVersion(environ []string) string {
	if vsn := adapterkit.Getenv(environ, realtimeVersionVar); vsn != "" {
		return vsn
	}
	return phxVersionV1
}

// sessionTopic is the private channel of one session (5.6).
func sessionTopic(sessionID string) string {
	return phxTopicPrefix + sessionID
}

// A phxFrame is one Phoenix message in either serializer version, refs
// rendered as strings ("" when null).
type phxFrame struct {
	JoinRef string
	Ref     string
	Topic   string
	Event   string
	Payload jsontext.Value
}

// phxObject is the vsn 1.0.0 wire shape.
type phxObject struct {
	JoinRef any            `json:"join_ref,omitzero"`
	Ref     any            `json:"ref,omitzero"`
	Topic   string         `json:"topic"`
	Event   string         `json:"event"`
	Payload jsontext.Value `json:"payload"`
}

// phxReply is the payload of a phx_reply.
type phxReply struct {
	Status   string `json:"status"`
	Response struct {
		Reason string `json:"reason"`
	} `json:"response"`
}

// phxBroadcast is the payload of a broadcast frame: {type, event,
// payload}; the inner payload of message_accepted carries message_id and
// seq, never a body.
type phxBroadcast struct {
	Type    string         `json:"type"`
	Event   string         `json:"event"`
	Payload jsontext.Value `json:"payload"`
}

// phxSystem is the payload of a `system` event on a channel.
type phxSystem struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// refString renders a ref the server sent back: Phoenix echoes the
// client's string, a broadcast carries null.
func refString(v any) string {
	switch r := v.(type) {
	case string:
		return r
	case float64:
		return strconv.FormatFloat(r, 'f', -1, 64)
	default:
		return ""
	}
}

// decodePhxFrame parses one websocket message under the serializer
// version in force: a JSON object at 1.0.0, a five-element array at
// 2.0.0, and at 2.0.0 a binary frame is a database broadcast in
// realtime-js's own framing.
func decodePhxFrame(vsn string, typ websocket.MessageType, data []byte) (phxFrame, error) {
	if typ == websocket.MessageBinary {
		return decodeBinaryBroadcast(data)
	}
	if vsn == phxVersionV2 {
		var arr []jsontext.Value
		if err := json.Unmarshal(data, &arr); err != nil || len(arr) != 5 {
			return phxFrame{}, errors.New("realtime: frame is not a five-element array")
		}
		var joinRef, ref any
		var f phxFrame
		if err := errors.Join(
			json.Unmarshal(arr[0], &joinRef), json.Unmarshal(arr[1], &ref),
			json.Unmarshal(arr[2], &f.Topic), json.Unmarshal(arr[3], &f.Event),
		); err != nil {
			return phxFrame{}, errors.New("realtime: frame members did not decode")
		}
		f.JoinRef, f.Ref, f.Payload = refString(joinRef), refString(ref), arr[4]
		return f, nil
	}
	var obj phxObject
	if err := json.Unmarshal(data, &obj); err != nil {
		return phxFrame{}, errors.New("realtime: frame is not a JSON object")
	}
	return phxFrame{
		JoinRef: refString(obj.JoinRef), Ref: refString(obj.Ref),
		Topic: obj.Topic, Event: obj.Event, Payload: obj.Payload,
	}, nil
}

// decodeBinaryBroadcast mirrors realtime-js's Serializer._decodeUserBroadcast
// (kind 4): [kind, topicLen, eventLen, metaLen, encoding, topic, event,
// meta, payload], encoding 1 for JSON. The result is the same frame a
// text broadcast would have produced, so the caller has one path.
func decodeBinaryBroadcast(b []byte) (phxFrame, error) {
	const header = 5
	if len(b) < header || b[0] != 4 {
		return phxFrame{}, errors.New("realtime: unknown binary frame")
	}
	topicLen, eventLen, metaLen, encoding := int(b[1]), int(b[2]), int(b[3]), b[4]
	if len(b) < header+topicLen+eventLen+metaLen {
		return phxFrame{}, errors.New("realtime: truncated binary frame")
	}
	off := header
	topic := string(b[off : off+topicLen])
	off += topicLen
	event := string(b[off : off+eventLen])
	off += eventLen + metaLen
	payload := b[off:]
	if encoding != 1 || !jsontext.Value(payload).IsValid() {
		return phxFrame{}, errors.New("realtime: binary broadcast payload is not JSON")
	}
	wrapped, err := json.Marshal(phxBroadcast{Type: phxEventBcast, Event: event, Payload: jsontext.Value(payload)})
	if err != nil {
		return phxFrame{}, errors.New("realtime: binary broadcast could not be re-encoded")
	}
	return phxFrame{Topic: topic, Event: phxEventBcast, Payload: wrapped}, nil
}

// hintFor reports whether f is a broadcast on the session's own topic —
// ANY broadcast: message_accepted, membership_revoked, or an event a later
// migration adds. Every one means "drain now"; the RPC decides what the
// drain finds (a message, or the uniform unauthorized that ends the watch
// of a revoked member at once, C-08). Only the database writes on the
// topic (no client has an insert policy), so nothing here needs the
// event's name, and a broadcast whose payload is not the {type, event,
// payload} shape is still a hint: a drain is cheap and never wrong.
func hintFor(f phxFrame, topic string) bool {
	return f.Event == phxEventBcast && f.Topic == topic
}

// joinPayload mirrors RealtimeChannel.subscribe(): the channel config
// with private true and self false, plus the user JWT (5.6, E0-2).
func joinPayload(token string) map[string]any {
	return map[string]any{
		"config": map[string]any{
			"broadcast":        map[string]any{"ack": false, "self": false},
			"presence":         map[string]any{"key": "", "enabled": false},
			"postgres_changes": []any{},
			"private":          true,
		},
		"access_token": token,
	}
}

// classifyReason maps a join refusal's reason or a system error's
// message to one fixed token (plan 5.6's table). The JWT names are
// checked before "Unauthorized": an expired token is a refresh-and-rejoin,
// not a revocation.
func classifyReason(text string) string {
	has := func(needles ...string) bool {
		for _, n := range needles {
			if strings.Contains(text, n) {
				return true
			}
		}
		return false
	}
	switch {
	case has("InvalidJWTToken", "JwtSignatureError", "MalformedJWT", "Token has expired", "expired"):
		return linkReasonBadToken
	case has("Unauthorized", "RlsPolicyError", "permissions"):
		return linkReasonUnauthorized
	case has("RateLimit"):
		return linkReasonRateLimited
	case has("PrivateOnly"):
		return linkReasonPrivateOnly
	case has("RealtimeDisabledForTenant", "DatabaseLackOfConnections"):
		return linkReasonDisabled
	case has("unmatched topic", "UnmatchedTopic"):
		return linkReasonUnmatchedTopic
	default:
		return linkReasonChannelError
	}
}

// A phxClient is one Phoenix socket: refs, the serializer version and
// the writes. Reads happen on exactly one goroutine (readFrames); writes
// may come from the link goroutine at any time (coder/websocket
// serialises them).
type phxClient struct {
	conn *websocket.Conn
	vsn  string
	log  *slog.Logger
	refs atomic.Int64
}

func (p *phxClient) nextRef() string {
	return strconv.FormatInt(p.refs.Add(1), 10)
}

// send writes one frame with the given refs.
func (p *phxClient) send(ctx context.Context, ref, joinRef, topic, event string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errInternal("a realtime payload could not be encoded")
	}
	var wire []byte
	if p.vsn == phxVersionV2 {
		var jr any
		if joinRef != "" {
			jr = joinRef
		}
		wire, err = json.Marshal([]any{jr, ref, topic, event, jsontext.Value(body)})
	} else {
		obj := phxObject{Ref: ref, Topic: topic, Event: event, Payload: body}
		if joinRef != "" {
			obj.JoinRef = joinRef
		}
		wire, err = json.Marshal(obj)
	}
	if err != nil {
		return errInternal("a realtime frame could not be encoded")
	}
	wctx, cancel := context.WithTimeout(ctx, phxWriteTimeout)
	defer cancel()
	return p.conn.Write(wctx, websocket.MessageText, wire)
}

// push writes one frame with a fresh ref and returns it.
func (p *phxClient) push(ctx context.Context, joinRef, topic, event string, payload any) (string, error) {
	ref := p.nextRef()
	return ref, p.send(ctx, ref, joinRef, topic, event, payload)
}

// join pushes phx_join; realtime-js sets the join's join_ref to its own
// ref, and every later push on the channel carries it.
func (p *phxClient) join(ctx context.Context, topic, token string) (string, error) {
	ref := p.nextRef()
	return ref, p.send(ctx, ref, ref, topic, phxEventJoin, joinPayload(token))
}

// leave is the best-effort shutdown of a joined channel: phx_leave, then
// the close handshake, each bounded so the watch's own 5 s exit budget
// holds whatever the peer does.
func (p *phxClient) leave(topic, joinRef string) {
	ctx, cancel := context.WithTimeout(context.Background(), phxLeaveTimeout)
	defer cancel()
	if joinRef != "" {
		_, _ = p.push(ctx, joinRef, topic, phxEventLeave, map[string]any{})
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = p.conn.Close(websocket.StatusNormalClosure, "")
	}()
	select {
	case <-closed:
	case <-ctx.Done():
		_ = p.conn.CloseNow()
	}
}

// A phxResult is one read: a frame, or the error that ended reading.
type phxResult struct {
	frame phxFrame
	err   error
}

// readFrames is the socket's one reader. It ends on the first read error
// (which it reports) or when ctx is done.
func (p *phxClient) readFrames(ctx context.Context, out chan<- phxResult) {
	for {
		typ, data, err := p.conn.Read(ctx)
		if err != nil {
			select {
			case out <- phxResult{err: err}:
			case <-ctx.Done():
			}
			return
		}
		frame, derr := decodePhxFrame(p.vsn, typ, data)
		if derr != nil {
			p.log.Debug("realtime frame ignored", adapterlog.Err(derr))
			continue
		}
		select {
		case out <- phxResult{frame: frame}:
		case <-ctx.Done():
			return
		}
	}
}

// The events a link reports to the watch loop.
type linkKind int

const (
	linkJoined  linkKind = iota // the private channel is up
	linkHint                    // a broadcast arrived on the session's topic
	linkDown                    // the socket or the channel went away; reason says why
	linkRefused                 // phx_join was refused; reason is the classified refusal
)

// A linkEvent is one report, stamped with the link's generation so a
// report from a superseded socket is ignored by the loop.
type linkEvent struct {
	gen    int
	kind   linkKind
	reason string
}

// runLink owns one socket for one join of topic: dial, join with token,
// then the steady state — hints (every broadcast on the topic),
// heartbeats every watchTiming.heartbeat (the server closes a socket
// silent for ~66 s), access_token pushes
// handed over tokens (a refresh re-runs the topic policy at the server,
// which is how a revocation reaches an open channel, E0-2 (h)), and the
// channel's own close. It returns after reporting linkDown or
// linkRefused, or when ctx is done (leaving the channel first, best
// effort). It never touches stdout and never calls an RPC — which is what
// lets the watch start it before `ready` (dialEarly): every report waits
// in events for the loop, and a cancel before the loop starts ends it in
// silence.
func (c *command) runLink(ctx context.Context, gen int, topic, token string, tokens <-chan string, events chan<- linkEvent) {
	report := func(kind linkKind, reason string) {
		select {
		case events <- linkEvent{gen: gen, kind: kind, reason: reason}:
		case <-ctx.Done():
		}
	}
	conn, err := c.client.dialRealtime(ctx, c.environ)
	if err != nil {
		perr := asProtocolError(err)
		c.log.Debug("realtime unavailable", slog.String("code", string(perr.Code)))
		if perr.Code.Retryable() && perr.RetryAfterMS > 0 {
			report(linkDown, linkReasonRateLimited)
			return
		}
		report(linkDown, linkReasonDial)
		return
	}
	p := &phxClient{conn: conn, vsn: realtimeVersion(c.environ), log: c.log}
	// The reader gets its own context: coder/websocket closes the
	// connection when a Read's context ends, and the leave on shutdown
	// must go out first. CloseNow (deferred) is what ends the reader.
	readCtx, stopReading := context.WithCancel(context.Background())
	defer stopReading()
	defer func() { _ = conn.CloseNow() }()
	frames := make(chan phxResult, 64)
	go p.readFrames(readCtx, frames)

	joinRef, err := p.join(ctx, topic, token)
	if err != nil {
		c.log.Debug("realtime join write failed", adapterlog.Err(err))
		report(linkDown, linkReasonDisconnected)
		return
	}
	// A refusal arrives only after the server's fixed 5 s backoff, so the
	// join timeout must be well above it (5.6).
	joinTimer := time.NewTimer(watchTiming.joinTimeout)
	defer joinTimer.Stop()
	for joined := false; !joined; {
		select {
		case <-ctx.Done():
			// Cancelled with the join unanswered: a watch refused before
			// `ready` (C-37, the dial precedes the ownership check), a
			// SIGTERM during the handshake. There is no channel to leave
			// and no close handshake to attempt: a Phoenix socket process
			// waits on the channel's join and answers nothing meanwhile (a
			// refusal's 5 s backoff included), and coder/websocket's
			// CloseNow behind an unfinished Close only waits for it, so the
			// handshake ran out finish's whole bound on every refused
			// watch (C-37: 0.4 s to 2.4 s, measured). The deferred
			// CloseNow, with no Close in flight, ends the socket at once.
			return
		case <-joinTimer.C:
			report(linkDown, linkReasonJoinTimeout)
			return
		case r := <-frames:
			if r.err != nil {
				c.log.Debug("realtime socket closed during join", adapterlog.Err(r.err))
				report(linkDown, linkReasonDisconnected)
				return
			}
			f := r.frame
			switch {
			case f.Event == phxEventReply && f.Ref == joinRef:
				var reply phxReply
				if err := json.Unmarshal(f.Payload, &reply); err != nil || reply.Status != "ok" {
					c.log.Debug("realtime join refused", slog.String("reason", reply.Response.Reason))
					report(linkRefused, classifyReason(reply.Response.Reason))
					return
				}
				joined = true
			case hintFor(f, topic):
				// A broadcast that lands during the handshake (E0-2 (f)); the
				// drain on join ok covers it, and a hint costs one more.
				report(linkHint, "")
			}
		}
	}
	c.log.Debug("realtime channel joined")
	report(linkJoined, "")

	heartbeat := time.NewTicker(watchTiming.heartbeat)
	defer heartbeat.Stop()
	awaiting := ""
	for {
		select {
		case <-ctx.Done():
			p.leave(topic, joinRef)
			return
		case <-heartbeat.C:
			if awaiting != "" {
				c.log.Debug("realtime heartbeat unanswered; reconnecting")
				report(linkDown, linkReasonHeartbeat)
				return
			}
			ref, err := p.push(ctx, "", phxSocketTopic, phxEventHB, map[string]any{})
			if err != nil {
				report(linkDown, linkReasonDisconnected)
				return
			}
			awaiting = ref
		case next := <-tokens:
			if _, err := p.push(ctx, joinRef, topic, phxEventToken, map[string]string{"access_token": next}); err != nil {
				report(linkDown, linkReasonDisconnected)
				return
			}
			c.log.Debug("realtime access token pushed")
		case r := <-frames:
			if r.err != nil {
				c.log.Debug("realtime socket closed", adapterlog.Err(r.err))
				report(linkDown, linkReasonDisconnected)
				return
			}
			f := r.frame
			switch {
			case f.Event == phxEventReply && f.Topic == phxSocketTopic:
				if f.Ref == awaiting {
					awaiting = ""
				}
			case hintFor(f, topic):
				report(linkHint, "")
			case f.Event == phxEventSystem && f.Topic == topic:
				var sys phxSystem
				if err := json.Unmarshal(f.Payload, &sys); err == nil && sys.Status == "error" {
					c.log.Debug("realtime channel error", slog.String("message", sys.Message))
					report(linkDown, classifyReason(sys.Message))
					return
				}
			case (f.Event == phxEventClose || f.Event == phxEventError) && f.Topic == topic:
				c.log.Debug("realtime channel closed by the server", slog.String("event", f.Event))
				report(linkDown, linkReasonDisconnected)
				return
			}
		}
	}
}
