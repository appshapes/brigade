package supabase

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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	// The Mozilla root bundle (5.1): Go's darwin verifier cannot reach
	// Security.framework inside the Bash sandbox and minimal Linux
	// containers ship no CA bundle. It is consulted only when the system
	// roots are unavailable, and only because cmd/brigade sets
	// //go:debug x509usefallbackroots=1.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// The HTTP budget of 5.1: 20 s per request, 10 s to connect. The harness
// gives a command 20 s in total, so a request that takes longer is lost
// either way; the per-request cap turns a hung backend into
// `unavailable`/timeout with an envelope instead of a killed child.
const (
	requestTimeout = 20 * time.Second
	connectTimeout = 10 * time.Second
	// maxResponseBytes bounds what the adapter will read of an answer.
	// A fetch_inbox page of 200 messages at 16 KiB each is about 3.3 MiB.
	maxResponseBytes = 8 << 20
)

// offlineVar is the test-only switch of 5.11: with BRIGADE_TEST_OFFLINE=1
// in the environment every dial fails loudly, which is how C-01 and C-07
// prove that `describe` never touches the network.
const offlineVar = "BRIGADE_TEST_OFFLINE"

// errOffline is the dial failure under BRIGADE_TEST_OFFLINE=1.
var errOffline = errors.New("network dial refused: BRIGADE_TEST_OFFLINE is set")

// A client is the adapter's one HTTP client for one backend: the
// publishable key as `apikey` on every request, X-Client-Info, the proxy
// and CA configuration of the environment it was built from, and the
// websocket dial the watch uses through the same transport.
type client struct {
	http    *http.Client
	baseURL string
	apikey  string
	log     *slog.Logger
	offline bool
}

// newClient builds the client for a profile's backend. baseURL is the
// project URL as `profile init` stored it (https, or http for a loopback
// host, checkBackendURL); apikey is the publishable key. Nothing here
// dials: construction is safe for `describe`.
func newClient(baseURL, apikey string, environ []string, log *slog.Logger) (*client, error) {
	transport, err := newTransport(environ)
	if err != nil {
		return nil, err
	}
	return &client{
		http:    &http.Client{Transport: transport, Timeout: requestTimeout},
		baseURL: strings.TrimRight(baseURL, "/"),
		apikey:  apikey,
		log:     log,
		offline: adapterkit.Getenv(environ, offlineVar) != "",
	}, nil
}

// newTransport is the one place the network is configured: the proxy
// from the environment (how a sandboxed `brigade send` reaches the
// backend), SSL_CERT_FILE and SSL_CERT_DIR honoured when set, a 10 s
// connect budget, and the BRIGADE_TEST_OFFLINE dialer that fails every
// connection loudly.
func newTransport(environ []string) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	offline := adapterkit.Getenv(environ, offlineVar) != ""
	transport := &http.Transport{
		// The process environment IS the environment the harness built
		// from scratch (3.2: PATH, HOME, the proxy variables and little
		// else), so the standard resolver reads exactly the variables
		// 4.1 allows. It also honours NO_PROXY and never proxies loopback.
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if offline {
				return nil, errOffline
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: requestTimeout,
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
// plus every PEM certificate named, so a corporate CA keeps working
// (5.1). Without either variable the verifier's own default applies.
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

// clientInfo is the X-Client-Info header of 5.1.
func clientInfo() string {
	return "brigade-adapter-supabase/" + buildinfo.String()
}

// A response is one HTTP answer: the status, the headers and the body,
// read in full up to maxResponseBytes.
type response struct {
	status int
	header http.Header
	body   []byte
}

// post performs one POST against the backend (every GoTrue and PostgREST
// call this adapter makes is a POST) with the headers every
// request carries — apikey, X-Client-Info, Content-Type for a body — plus
// the caller's. A failed exchange (not an HTTP error status) is returned
// as the mapped *protocol.Error of mapTransportError; the raw error is
// logged at debug through the redactor.
func (cl *client) post(ctx context.Context, path string, headers map[string]string, body []byte) (*response, error) {
	if cl.offline {
		cl.log.Debug("dial refused", slog.String("reason", reasonOffline), slog.String("path", path))
		return nil, mapTransportError(errOffline)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.baseURL+path, reader)
	if err != nil {
		return nil, errInternal("the request could not be built")
	}
	req.Header.Set("apikey", cl.apikey)
	req.Header.Set("X-Client-Info", clientInfo())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
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
	if len(data) > maxResponseBytes {
		return nil, errUnexpectedResponse("the backend answered with an oversize response")
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

// realtimeVersionVar is the test-only switch of 5.11 that selects the
// Phoenix serializer version; the default 1.0.0 delivers every frame as
// one JSON object, and 2.0.0 (binary frames for database broadcasts) is
// kept behind the switch in case a Realtime release drops 1.0.0.
const realtimeVersionVar = "BRIGADE_SUPABASE_REALTIME_VSN"

// realtimeURL is the websocket endpoint of 5.6:
// wss://<host>/realtime/v1/websocket?apikey=<publishable>&vsn=<vsn>, with
// ws:// for an http backend (loopback only, checkBackendURL).
func (cl *client) realtimeURL(environ []string) (string, error) {
	u, err := url.Parse(cl.baseURL)
	if err != nil {
		return "", errConfig("the backend url does not parse", "backend_url")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errConfig("the backend url scheme is not http or https", "backend_url")
	}
	vsn := adapterkit.Getenv(environ, realtimeVersionVar)
	if vsn == "" {
		vsn = "1.0.0"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime/v1/websocket"
	q := url.Values{}
	q.Set("apikey", cl.apikey)
	q.Set("vsn", vsn)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// dialRealtime opens the Phoenix websocket of 5.6 through this client's
// transport, so the proxy, the CA configuration and BRIGADE_TEST_OFFLINE
// apply to the watch exactly as to every HTTP request. The read limit of
// 1 MiB is set before the connection is returned. The HTTP response of a
// refused upgrade (a bad key is HTTP 401 after about 2 s) is mapped to
// the 4.6 taxonomy; the caller owns the connection.
func (cl *client) dialRealtime(ctx context.Context, environ []string) (*websocket.Conn, error) {
	if cl.offline {
		return nil, mapTransportError(errOffline)
	}
	target, err := cl.realtimeURL(environ)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("X-Client-Info", clientInfo())
	conn, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: cl.http,
		HTTPHeader: header,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		cl.log.Debug("realtime dial failed", adapterlog.Err(err))
		if resp != nil {
			switch {
			case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
				return nil, errConfig("the realtime service rejected the profile's API key", reasonAPIKeyRejected)
			case resp.StatusCode == http.StatusTooManyRequests:
				return nil, errRateLimited(reasonRealtime, 60_000)
			case resp.StatusCode >= 500:
				return nil, errUnavailable("the realtime service is unavailable", reasonBackendError)
			}
		}
		return nil, mapTransportError(err)
	}
	conn.SetReadLimit(protocol.MaxLineBytes)
	return conn, nil
}
