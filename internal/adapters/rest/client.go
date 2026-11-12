package rest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	// The Mozilla root bundle, as the Supabase adapter links it: Go's darwin
	// verifier cannot reach Security.framework inside the Bash sandbox and
	// minimal Linux containers ship no CA bundle. It is consulted only when
	// the system roots are unavailable, and only because cmd/brigade sets
	// //go:debug x509usefallbackroots=1.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/buildinfo"
)

// The HTTP budget: 20 s per request, 10 s to connect. The harness gives a
// command 20 s in total, so a request that takes longer is lost either
// way; the per-request deadline turns a hung server into
// `unavailable`/timeout with an envelope instead of a killed child. A
// long poll of the watch carries its own, longer deadline (watch.go).
const (
	requestTimeout = 20 * time.Second
	connectTimeout = 10 * time.Second
	// maxRequestBytes is the contract's cap on a request body (section 1).
	maxRequestBytes = 1 << 20
	// maxResponseBytes bounds what the adapter will read of an answer. A
	// receive page of 200 messages at 16 KiB each is about 3.3 MiB.
	maxResponseBytes = 8 << 20
)

// The paths of the contract's section 2, under the profile's base url.
const (
	pathTeamCreate       = "/v1/team/create"
	pathTeamJoin         = "/v1/team/join"
	pathTeamLeave        = "/v1/team/leave"
	pathTeamMembers      = "/v1/team/members"
	pathSessionRegister  = "/v1/session/register"
	pathSessionHeartbeat = "/v1/session/heartbeat"
	pathSessionList      = "/v1/session/list"
	pathSessionClose     = "/v1/session/close"
	pathMessageSend      = "/v1/message/send"
	pathMessageReceive   = "/v1/message/receive"
	pathMessageAck       = "/v1/message/ack"
	pathCredentialRevoke = "/v1/credential/revoke" //nolint:gosec // G101: a path, not a credential
)

// createTokenHeader carries the operator's creation token on
// /v1/team/create when the server requires one (the contract's section 3).
const createTokenHeader = "Brigade-Create-Token" //nolint:gosec // G101: a header name, not a credential

// offlineVar is the test-only switch the Supabase adapter also honours:
// with BRIGADE_TEST_OFFLINE=1 in the environment every dial fails loudly,
// which is how C-01 and C-07 prove that `describe` never touches the
// network.
const offlineVar = "BRIGADE_TEST_OFFLINE"

// errOffline is the dial failure under BRIGADE_TEST_OFFLINE=1.
var errOffline = errors.New("network dial refused: BRIGADE_TEST_OFFLINE is set")

// A client is the adapter's one HTTP client for one server: the proxy and
// CA configuration of the environment it was built from, and no redirects.
type client struct {
	http    *http.Client
	baseURL string
	log     *slog.Logger
	offline bool
}

// newClient builds the client for a profile's server. baseURL is the base
// url as `profile init` stored it (checkBackendURL). Nothing here dials:
// construction is safe for `describe`.
//
// The client has no timeout of its own: every request carries a context
// with a deadline, because a long poll must outlive the 20 s every other
// request gets. It follows no redirect: a 3xx is an answer that is not an
// envelope, and a server that moved says so to its operator, not by
// sending the credential to another origin.
func newClient(baseURL string, environ []string, log *slog.Logger) (*client, error) {
	transport, err := newTransport(environ)
	if err != nil {
		return nil, err
	}
	return &client{
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		log:     log,
		offline: adapterkit.Getenv(environ, offlineVar) != "",
	}, nil
}

// newTransport is the one place the network is configured: the proxy from
// the environment (how a sandboxed `brigade send` reaches the server),
// SSL_CERT_FILE and SSL_CERT_DIR honoured when set, a 10 s connect budget,
// and the BRIGADE_TEST_OFFLINE dialer that fails every connection loudly.
func newTransport(environ []string) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	offline := adapterkit.Getenv(environ, offlineVar) != ""
	transport := &http.Transport{
		// The process environment IS the environment the harness built from
		// scratch (PATH, HOME, the proxy variables and little else), so the
		// standard resolver reads exactly the variables 4.1 allows. It also
		// honours NO_PROXY and never proxies loopback.
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if offline {
				return nil, errOffline
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   connectTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if pool, ok, err := rootPool(environ); err != nil {
		return nil, err
	} else if ok {
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

// rootPool builds the CA pool when SSL_CERT_FILE or SSL_CERT_DIR is set:
// the system roots (or the embedded fallback when they cannot be loaded)
// plus every PEM certificate named, so a private CA keeps working. Without
// either variable the verifier's own default applies.
func rootPool(environ []string) (*x509.CertPool, bool, error) {
	file := adapterkit.Getenv(environ, "SSL_CERT_FILE")
	dir := adapterkit.Getenv(environ, "SSL_CERT_DIR")
	if file == "" && dir == "" {
		return nil, false, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if file != "" {
		pem, rerr := os.ReadFile(file)
		if rerr != nil {
			return nil, false, errConfig("SSL_CERT_FILE could not be read", "ssl_cert_file")
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, false, errConfig("SSL_CERT_FILE holds no PEM certificate", "ssl_cert_file")
		}
	}
	if dir != "" {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			return nil, false, errConfig("SSL_CERT_DIR could not be read", "ssl_cert_dir")
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if pem, rerr := os.ReadFile(dir + string(os.PathSeparator) + entry.Name()); rerr == nil {
				pool.AppendCertsFromPEM(pem)
			}
		}
	}
	return pool, true, nil
}

// userAgent identifies the adapter build to the server.
func userAgent() string {
	return progName + "/" + buildinfo.String()
}

// A response is one HTTP answer: the status and the body, read in full up
// to maxResponseBytes.
type response struct {
	status int
	body   []byte
}

// post performs one POST of the contract's section 1: a JSON body, the
// credential as a bearer token when the adapter holds one, and the
// caller's extra headers. A failed exchange (not an HTTP error status) is
// returned as the `unavailable` of mapTransportError; the raw error is
// logged at debug through the redactor. Neither the body nor a header is
// ever logged.
func (cl *client) post(ctx context.Context, path, bearer string, headers map[string]string, body []byte) (*response, error) {
	if cl.offline {
		cl.log.Debug("dial refused", slog.String("reason", reasonOffline), slog.String("path", path))
		return nil, mapTransportError(errOffline)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, errInternal("the request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	started := time.Now()
	resp, err := cl.http.Do(req)
	if err != nil {
		cl.log.Debug("request failed", slog.String("path", path), adapterlog.Err(err))
		return nil, mapTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		cl.log.Debug("response read failed", slog.String("path", path), adapterlog.Err(err))
		return nil, mapTransportError(err)
	}
	cl.log.Debug("request answered", slog.String("path", path), slog.Int("status", resp.StatusCode),
		slog.Int("bytes", len(data)), slog.Duration("took", time.Since(started)))
	if len(data) > maxResponseBytes {
		return nil, nonEnvelope(resp.StatusCode)
	}
	return &response{status: resp.StatusCode, body: data}, nil
}
