package webhook

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// DefaultGatewayURL is where `send` and `sessions` find the running
// gateway: its default loopback listener.
const DefaultGatewayURL = "http://" + DefaultListen

// runClient is `send` and `sessions`: the local way for a program on the
// gateway's machine (Grok Bot's shell, a script) to talk to the running
// gateway. They sign with the same secret file, so the inbound API needs
// no other credential and is never exposed beyond loopback by default.
func runClient(ctx context.Context, verb string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("brigade-webhook-gateway "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	gateway := fs.String("gateway", DefaultGatewayURL, "the running gateway's inbound address")
	secretFile := fs.String("secret-file", "", "the gateway's secret file (absolute path)")
	var in InboundRequest
	if verb == "send" {
		fs.StringVar(&in.To, "to", "", "a session id or a unique tail of 5+ characters")
		fs.StringVar(&in.Member, "member", "", "a member's label or principal prefix; their best session gets it")
		fs.StringVar(&in.ReplyTo, "reply-to", "", "the Brigade message id this answers (alone, it routes to that message's sender)")
		fs.StringVar(&in.Text, "text", "", "the message text; read from stdin when absent")
		fs.StringVar(&in.Summary, "summary", "", "a one-line summary")
		fs.StringVar(&in.ID, "id", "", "a delivery id: a retried send with the same id is one message")
		fs.StringVar(&in.From, "from", "Grok Bot", "display name shown in the attribution line (unverified)")
		fs.StringVar(&in.Ref, "ref", "", "an opaque reference echoed back in thread.ref on replies")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	complain := func(msg string) int {
		_, _ = io.WriteString(stderr, "brigade-webhook-gateway "+verb+": "+msg+"\n")
		return 2
	}
	if fs.NArg() != 0 {
		return complain("unexpected arguments")
	}
	if *secretFile == "" || !filepath.IsAbs(*secretFile) {
		return complain("--secret-file must be an absolute path")
	}
	raw, err := adapterkit.ReadStrict(*secretFile)
	if err != nil {
		return complain("cannot read the secret file: " + err.Error())
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if err := checkTarget(*gateway, false); err != nil {
		return complain("invalid --gateway: " + err.Error())
	}
	method, path, body := http.MethodGet, PathSessions, []byte{}
	if verb == "send" {
		if in.Text == "" {
			data, rerr := io.ReadAll(io.LimitReader(stdin, MaxInboundBytes+1))
			if rerr != nil || len(data) > MaxInboundBytes {
				return complain("cannot read the text from stdin (or it is too long)")
			}
			in.Text = string(data)
		}
		if verr := in.Validate(); verr != nil {
			return complain(verr.Error())
		}
		if body, err = json.Marshal(&in); err != nil {
			return complain("cannot encode the request")
		}
		method, path = http.MethodPost, PathMessages
	}
	rctx, cancel := context.WithTimeout(ctx, inboundTimeout+5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, strings.TrimSuffix(*gateway, "/")+path, bytes.NewReader(body))
	if err != nil {
		return complain("cannot build the request")
	}
	ts := time.Now().Unix()
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(secret, ts, body))
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		_, _ = io.WriteString(stderr, "brigade-webhook-gateway "+verb+": the gateway is not reachable (is it running?)\n")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(stdout, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return 1
	}
	return 0
}
