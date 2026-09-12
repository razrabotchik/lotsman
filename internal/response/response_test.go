package response

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func doGet(t *testing.T, srv *httptest.Server) *http.Response {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	return resp
}

func TestFromHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	result, err := FromHTTP(doGet(t, srv))
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", result.Status)
	}
	if result.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", result.ContentType)
	}
	if result.Body != `{"ok":true}` {
		t.Errorf("Body = %q", result.Body)
	}
	if result.IsError {
		t.Error("IsError = true for a 200 response")
	}
}

func TestFromHTTPClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	result, err := FromHTTP(doGet(t, srv))
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if result.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want 404", result.Status)
	}
	if !result.IsError {
		t.Error("IsError = false for a 404 response, want true (FR-39)")
	}
}

func TestFromHTTPServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	result, err := FromHTTP(doGet(t, srv))
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if !result.IsError {
		t.Error("IsError = false for a 500 response, want true")
	}
}

func TestFromHTTPTruncatesLargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("a", MaxBodyBytes+1024)))
	}))
	defer srv.Close()

	result, err := FromHTTP(doGet(t, srv))
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if len(result.Body) != MaxBodyBytes {
		t.Errorf("len(Body) = %d, want exactly %d", len(result.Body), MaxBodyBytes)
	}
}

func respond(t *testing.T, status int, header http.Header, body string) Result {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	result, err := FromHTTP(resp)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	return result
}

// Pitfall #11: a truncated JSON document must never be handed over as
// something that parses. It is text, it says it was cut, and it says how much
// arrived.
func TestTruncationIsReportedAndNeverClaimsStructure(t *testing.T) {
	whole := `{"items":[` + strings.Repeat(`"x",`, MaxBodyBytes) + `]}`
	result := respond(t, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, whole)

	if !result.Truncated {
		t.Fatal("a body over the limit was not reported as truncated")
	}
	if result.ReceivedBytes != len(result.Body) {
		t.Errorf("receivedBytes = %d, body = %d bytes", result.ReceivedBytes, len(result.Body))
	}
	if result.ReceivedBytes > MaxBodyBytes {
		t.Errorf("receivedBytes = %d, over the limit", result.ReceivedBytes)
	}
	if json.Valid([]byte(result.Body)) {
		t.Error("the truncated body happens to be valid JSON, which makes this test prove nothing")
	}
	// The body is a string in the result's own shape, so nothing downstream
	// can mistake it for structured content.
	var shaped struct {
		Body any `json:"body"`
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &shaped); err != nil {
		t.Fatal(err)
	}
	if _, isString := shaped.Body.(string); !isString {
		t.Errorf("body was published as %T, want a string", shaped.Body)
	}
}

func TestCompleteBodyIsNotMarkedTruncated(t *testing.T) {
	result := respond(t, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"ok":true}`)
	if result.Truncated {
		t.Error("a complete body was reported as truncated")
	}
	if result.ReceivedBytes != len(`{"ok":true}`) {
		t.Errorf("receivedBytes = %d", result.ReceivedBytes)
	}
	if !json.Valid([]byte(result.Body)) {
		t.Errorf("body = %q, want the document unchanged", result.Body)
	}
}

// A cut that lands inside a multi-byte character must not ship half of it.
func TestTruncationRespectsRuneBoundaries(t *testing.T) {
	// "щ" is two bytes; fill the limit so the cut lands mid-character.
	body := strings.Repeat("a", MaxBodyBytes-1) + strings.Repeat("щ", 16)
	result := respond(t, http.StatusOK, nil, body)

	if !result.Truncated {
		t.Fatal("want truncation")
	}
	if !utf8.ValidString(result.Body) {
		t.Error("the truncated body is not valid UTF-8")
	}
}

// FR-38: an allowlist, because the set of headers a vendor might use to carry
// something sensitive is open-ended and the set worth forwarding is not.
func TestHeaderAllowlist(t *testing.T) {
	result := respond(t, http.StatusOK, http.Header{
		"Content-Type":          {"application/json"},
		"X-Request-Id":          {"req-42"},
		"X-Ratelimit-Remaining": {"17"},
		"Retry-After":           {"30"},
		"Set-Cookie":            {"session=secret; HttpOnly"},
		"Authorization":         {"Bearer nope"},
		"Www-Authenticate":      {"Bearer realm=\"api\""},
		"X-Vendor-Internal":     {"whatever"},
	}, `{}`)

	for _, want := range []string{"content-type", "x-request-id", "x-ratelimit-remaining", "retry-after", "www-authenticate"} {
		if _, ok := result.Headers[want]; !ok {
			t.Errorf("header %q missing from %v", want, result.Headers)
		}
	}
	for _, unwanted := range []string{"set-cookie", "authorization", "x-vendor-internal"} {
		if value, ok := result.Headers[unwanted]; ok {
			t.Errorf("header %q was forwarded as %q", unwanted, value)
		}
	}
	if result.Headers["x-request-id"] != "req-42" {
		t.Errorf("x-request-id = %q", result.Headers["x-request-id"])
	}
}

func TestRepeatedHeadersAreJoined(t *testing.T) {
	result := respond(t, http.StatusOK, http.Header{"X-Request-Id": {"a", "b"}}, "")
	if got := result.Headers["x-request-id"]; got != "a, b" {
		t.Errorf("x-request-id = %q, want the values joined", got)
	}
}
