package response

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
