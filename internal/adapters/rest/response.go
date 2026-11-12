package rest

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// What the adapter does with a response (the contract's section 1): a body
// that is a valid result envelope is the answer, whatever the status;
// anything else is `unavailable` when the status is 5xx and `internal`
// otherwise. A request that failed in transport never reaches this file:
// client.post has already answered `unavailable`.

// serverCodes are the codes a server may answer. `usage`, `config` and
// `protocol_mismatch` are the adapter's own (section 1), so an envelope
// that carries one of them, or a code outside the taxonomy, is not a
// valid answer of this contract.
var serverCodes = map[protocol.Code]bool{
	protocol.CodeInternal:        true,
	protocol.CodeInvalidInput:    true,
	protocol.CodeUnauthenticated: true,
	protocol.CodeUnauthorized:    true,
	protocol.CodeNotFound:        true,
	protocol.CodeConflict:        true,
	protocol.CodeRateLimited:     true,
	protocol.CodeUnavailable:     true,
	protocol.CodeLoopDetected:    true,
}

// nonEnvelope is the answer for a response that is not a valid envelope.
func nonEnvelope(status int) *protocol.Error {
	if status >= 500 {
		return errUnavailable("the backend failed; try again later", reasonBackendError)
	}
	return errUnexpectedResponse("the backend answered with something that is not a result envelope")
}

// readEnvelope turns one response into the command's raw result, or into
// the error the command fails with. A failing envelope is the server's own
// answer and is passed on: its code, its message, its retry_after_ms and
// its details. Its text goes through the redactor first, so a server that
// breaks the contract's "never a secret, never a credential" rule cannot
// make this adapter print one.
func readEnvelope(resp *response, redact *adapterlog.Redactor) (jsontext.Value, error) {
	var env protocol.Envelope
	if protocol.Unmarshal(resp.body, &env) != nil || env.Validate() != nil || env.ProtocolVersion != protocol.ProtocolVersion {
		return nil, nonEnvelope(resp.status)
	}
	if env.OK {
		if env.Result.Kind() != '{' {
			return nil, nonEnvelope(resp.status)
		}
		return env.Result, nil
	}
	e := env.Error
	if !serverCodes[e.Code] || (e.Code == protocol.CodeRateLimited && e.RetryAfterMS < 1) {
		return nil, nonEnvelope(resp.status)
	}
	out := &protocol.Error{Code: e.Code, Message: redact.Redact(e.Message), RetryAfterMS: e.RetryAfterMS}
	if len(e.Details) > 0 {
		out.Details = make(map[string]string, len(e.Details))
		for k, v := range e.Details {
			out.Details[redact.Redact(k)] = redact.Redact(v)
		}
	}
	return nil, out
}

// A request is one call of the contract's section 2.
type request struct {
	path string
	// in is marshalled as the request body; nil sends `{}`.
	in any
	// out receives the result, decoded and validated; nil discards it.
	out protocol.Validator
	// headers are extra request headers (the creation token).
	headers map[string]string
}

// call performs one request with the command's own 20 s deadline.
func (c *command) call(path string, in any, out protocol.Validator) error {
	ctx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	return c.do(ctx, &request{path: path, in: in, out: out})
}

// do is the one place a request is made and its answer read: the body is
// encoded and held to the contract's 1 MiB, the credential rides as a
// bearer token, the response is read as section 1 says, and the result is
// decoded into the command's typed shape and validated before anything is
// printed, so a server that answers a malformed result is `internal` and
// an unknown member never reaches stdout.
//
// An `unauthenticated` answer earns one retry when the credential file
// holds another credential than the one that was refused: a `team join`
// in another process may have been handed a new credential that replaced
// this one (the contract's section 3), and a running watch should not end
// for that.
func (c *command) do(ctx context.Context, r *request) error {
	body := []byte("{}")
	if r.in != nil {
		encoded, err := json.Marshal(r.in)
		if err != nil {
			return errInternal("the request could not be encoded")
		}
		body = encoded
	}
	if len(body) > maxRequestBytes {
		return errInvalidField("stdin", "too_long", "the request exceeds the 1 MiB a backend accepts")
	}
	for attempt := 0; ; attempt++ {
		bearer := c.bearer()
		resp, err := c.client.post(ctx, r.path, bearer, r.headers, body)
		if err != nil {
			return err
		}
		result, err := readEnvelope(resp, c.redactor)
		if err != nil {
			perr := asProtocolError(err)
			c.log.Debug("request refused", slog.String("path", r.path), slog.Int("status", resp.status),
				slog.String("code", string(perr.Code)))
			if perr.Code == protocol.CodeUnauthenticated && attempt == 0 && bearer != "" && c.adoptStoredCredential(bearer) {
				continue
			}
			return err
		}
		if r.out == nil {
			return nil
		}
		if protocol.Decode(result, r.out) != nil {
			c.log.Debug("result did not validate", slog.String("path", r.path))
			return errUnexpectedResponse("the backend answered with a result this adapter could not read")
		}
		return nil
	}
}
