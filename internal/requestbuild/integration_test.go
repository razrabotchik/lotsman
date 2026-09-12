package requestbuild_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/razrabotchik/lotsman/internal/requestbuild"
	"github.com/razrabotchik/lotsman/internal/response"
)

// TestExecuteGETEndToEnd wires the runtime call order (docs/pipeline.md
// stages 6-8, minus auth/egress) against a real HTTP server: build the
// request, execute it with an ordinary http.Client, shape the response.
// This is T013's httptest integration coverage for T012.
func TestExecuteGETEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/widgets" {
			t.Errorf("path = %s, want /widgets", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"widgets":[]}`))
	}))
	defer srv.Close()

	req, err := requestbuild.Build(t.Context(), &requestbuild.Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{srv.URL}}, nil, requestbuild.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	result, err := response.FromHTTP(resp)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if result.Status != http.StatusOK || result.IsError {
		t.Errorf("result = %+v, want a clean 200", result)
	}
	if result.Body != `{"widgets":[]}` {
		t.Errorf("Body = %q", result.Body)
	}
}

// TestExecuteGETUpstream4xxIsError checks the FR-39 boundary this
// integration exercises for real: an upstream 4xx must not look like
// success anywhere in the chain.
func TestExecuteGETUpstream4xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	req, err := requestbuild.Build(t.Context(), &requestbuild.Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{srv.URL}}, nil, requestbuild.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	result, err := response.FromHTTP(resp)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if !result.IsError || result.Status != http.StatusForbidden {
		t.Errorf("result = %+v, want isError with status 403", result)
	}
}
