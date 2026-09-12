package response

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// MaxBodyBytes bounds how much of a response body is read into memory
// (FR-36). No config wiring exists yet; this is the v0 hardcoded default,
// matching the example config in docs/spec.md (execution.maxResponseBytes).
const MaxBodyBytes = 512 * 1024

// headerAllowlist is what a response may tell the model about itself
// (FR-38). It is an allowlist rather than a denylist because the set of
// headers a vendor might use to carry something sensitive is open-ended, and
// the set worth forwarding is not: what request this was, how much quota is
// left, when to come back, and what the body claims to be.
var headerAllowlist = map[string]bool{
	"content-type":          true,
	"content-length":        true,
	"date":                  true,
	"etag":                  true,
	"last-modified":         true,
	"location":              true, // a refused redirect is worth explaining
	"request-id":            true,
	"x-request-id":          true,
	"x-correlation-id":      true,
	"trace-id":              true,
	"retry-after":           true,
	"ratelimit":             true,
	"ratelimit-limit":       true,
	"ratelimit-remaining":   true,
	"ratelimit-reset":       true,
	"x-ratelimit-limit":     true,
	"x-ratelimit-remaining": true,
	"x-ratelimit-reset":     true,
	"x-ratelimit-used":      true,
	"www-authenticate":      true, // names the scheme, carries no credential
}

// Result is the MCP-facing shape of an HTTP response (FR-37).
//
// Body is always text, never a decoded object, and that is deliberate: a
// truncated JSON document handed over as structured content is the specific
// failure pitfall #11 names. Text cannot claim to be well-formed, so
// truncation degrades honestly instead of producing something that parses
// into the wrong thing.
type Result struct {
	Status      int               `json:"status"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	// Truncated reports that the upstream sent more than lotsman was willing
	// to read. ReceivedBytes is how much of it reached the caller.
	Truncated     bool `json:"truncated"`
	ReceivedBytes int  `json:"receivedBytes"`
	// IsError mirrors FR-39: 4xx/5xx is a tool error, not a transport
	// failure -- the caller maps this to the MCP tools/call isError field
	// so the model sees the upstream error text instead of a bare status.
	IsError bool `json:"-"`
}

// FromHTTP reads resp's body under MaxBodyBytes and shapes it into a Result.
// It always closes resp.Body.
func FromHTTP(resp *http.Response) (Result, error) {
	defer resp.Body.Close()

	// One byte past the limit is what tells "exactly the limit" and
	// "truncated" apart without buffering the excess.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return Result{}, errs.Errorf(errs.ClassUpstream, "response: read body: %w", err)
	}

	truncated := len(body) > MaxBodyBytes
	if truncated {
		body = body[:MaxBodyBytes]
		body = trimPartialRune(body)
	}

	return Result{
		Status: resp.StatusCode,
		// An upstream is entitled to echo a credential back -- some APIs
		// return the token they were given, and a 401 body often quotes the
		// header it rejected. Whatever the reason, a value lotsman resolved
		// must not travel on into a model's context (FR-61, pitfall #14).
		ContentType:   redact.String(resp.Header.Get("Content-Type")),
		Headers:       allowedHeaders(resp.Header),
		Body:          redact.String(string(body)),
		Truncated:     truncated,
		ReceivedBytes: len(body),
		IsError:       resp.StatusCode >= http.StatusBadRequest,
	}, nil
}

// allowedHeaders copies the headers worth forwarding, joining repeats the way
// HTTP means them. Values are redacted like everything else: a vendor is
// perfectly capable of echoing a token into a header of its own invention.
func allowedHeaders(header http.Header) map[string]string {
	var names []string
	for name := range header {
		if headerAllowlist[strings.ToLower(name)] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names) // deterministic output for a deterministic runtime

	out := make(map[string]string, len(names))
	for _, name := range names {
		out[strings.ToLower(name)] = redact.String(strings.Join(header.Values(name), ", "))
	}
	return out
}

// trimPartialRune drops a multi-byte character the cut landed inside. A lone
// continuation byte is not text, and shipping one produces a replacement
// character in someone else's terminal for no reason.
func trimPartialRune(body []byte) []byte {
	for len(body) > 0 {
		if r, size := utf8.DecodeLastRune(body); r != utf8.RuneError || size > 1 {
			break
		}
		body = body[:len(body)-1]
	}
	return body
}
