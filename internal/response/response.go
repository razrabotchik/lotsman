package response

import (
	"fmt"
	"io"
	"net/http"

	"github.com/razrabotchik/lotsman/internal/redact"
)

// MaxBodyBytes bounds how much of a response body is read into memory
// (FR-36). No config wiring exists yet; this is the v0 hardcoded default,
// matching the example config in docs/spec.md (execution.maxResponseBytes).
const MaxBodyBytes = 512 * 1024

// Result is the MCP-facing shape of an HTTP response: FR-37's v0 slice
// (status, contentType, body). Headers, truncated/receivedBytes fields and
// the allowlist land with T033.
type Result struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType,omitempty"`
	Body        string `json:"body,omitempty"`
	// IsError mirrors FR-39: 4xx/5xx is a tool error, not a transport
	// failure -- the caller maps this to the MCP tools/call isError field
	// so the model sees the upstream error text instead of a bare status.
	IsError bool `json:"-"`
}

// FromHTTP reads resp's body under MaxBodyBytes and shapes it into a
// Result. It always closes resp.Body.
//
// A body larger than MaxBodyBytes is silently cut short here: Result.Body
// is plain text, never claimed to be valid JSON, so a truncated body cannot
// misrepresent itself as well-formed structured content (FR-36's specific
// pitfall). Marking truncation explicitly is T033.
func FromHTTP(resp *http.Response) (Result, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		return Result{}, fmt.Errorf("response: read body: %w", err)
	}
	return Result{
		Status: resp.StatusCode,
		// An upstream is entitled to echo a credential back -- some APIs
		// return the token they were given, and a 401 body often quotes the
		// header it rejected. Whatever the reason, a value lotsman resolved
		// must not travel on into a model's context (FR-61, pitfall #14).
		ContentType: redact.String(resp.Header.Get("Content-Type")),
		Body:        redact.String(string(body)),
		IsError:     resp.StatusCode >= http.StatusBadRequest,
	}, nil
}
