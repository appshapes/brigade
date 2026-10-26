package webhook

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// maxExtraHeaders bounds a --header-file: it carries a receiver's own
// credential (Grok Bot routines want an Authorization header), not a
// request template.
const maxExtraHeaders = 16

// parseHeaderFile reads a --header-file: one `Name: value` per line,
// blank lines and lines starting with `#` ignored. Names must be HTTP
// tokens, values non-empty and free of control characters, and a name
// may appear once. The gateway's own headers — Content-Type, Host,
// User-Agent and every X-Brigade-* — are refused, so the file can never
// change what is signed or how. No error quotes a value or a name: the
// file is a credential, and a malformed line may be one.
func parseHeaderFile(data []byte) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		at := "--header-file line " + strconv.Itoa(i+1)
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !isToken(name) {
			return nil, errors.New(at + ": expected `Name: value` with a valid header name")
		}
		if value == "" || !isFieldValue(value) {
			return nil, errors.New(at + ": the value is empty or holds a control character")
		}
		if err := checkExtraHeaderName(name); err != nil {
			return nil, errors.New(at + ": " + err.Error())
		}
		key := http.CanonicalHeaderKey(name)
		if _, dup := h[key]; dup {
			return nil, errors.New(at + ": a header may be given only once")
		}
		if len(h) == maxExtraHeaders {
			return nil, errors.New(at + ": at most " + strconv.Itoa(maxExtraHeaders) + " headers")
		}
		h.Set(key, value)
	}
	return h, nil
}

// checkExtraHeaderName refuses the names the gateway sets itself.
// Content-Type and the X-Brigade-* headers are what the receiver
// verifies; Host and User-Agent identify the request. Comparison is
// case-insensitive, as HTTP names are.
func checkExtraHeaderName(name string) error {
	lower := strings.ToLower(name)
	switch {
	case lower == "content-type", lower == "host", lower == "user-agent", lower == "content-length":
		return errors.New("that header is set by the gateway and cannot be overridden")
	case strings.HasPrefix(lower, "x-brigade-"):
		return errors.New("X-Brigade-* headers are the gateway's own and cannot be overridden")
	}
	return nil
}

// minRedactedHeaderValue is the length a header value must reach before
// it becomes a redactor token. A token is a plain substring replaced
// everywhere it occurs in every line, so a short value is a global
// pattern over the gateway's whole log: `X-Api-Version: 1` would blank
// every `1` in every timestamp, message id and attempt count, and a
// seven-letter tag value such as `brigade` would blank it inside
// `brigade-webhook-gateway/<version>`, inside the header file's own path
// in a "cannot read" diagnostic and inside the adapter child's argv —
// taking from the operator exactly the diagnostics a failure needs, with
// nothing saying why. A credential is never that short; a tag or a
// version value often is, and a tag is not what a header file is for.
const minRedactedHeaderValue = 8

// headerSecrets is what a header file adds to the log redactor: every
// value long enough to be a credential, whole, and, for a value of the
// form `<scheme> <credential>` (Authorization: Bearer …), the credential
// alone, which is what a diagnostic quoting a token would print.
func headerSecrets(h http.Header) []string {
	var out []string
	for _, values := range h {
		for _, v := range values {
			if len(v) >= minRedactedHeaderValue {
				out = append(out, v)
			}
			if _, cred, ok := strings.Cut(v, " "); ok {
				if cred = strings.TrimSpace(cred); len(cred) >= minRedactedHeaderValue {
					out = append(out, cred)
				}
			}
		}
	}
	return out
}

// isToken reports whether s is an RFC 9110 token.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// isFieldValue reports whether s is a header value with no control
// characters other than horizontal tab.
func isFieldValue(s string) bool {
	for _, c := range []byte(s) {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
