package httpserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// A public bind with nothing authenticating the endpoint does not start
// (FR-70). The refusal is at startup rather than per request: a deployment
// that comes up and denies everything looks like an outage.
func TestPublicBindWithoutAuthenticationIsRefused(t *testing.T) {
	cases := []struct {
		name          string
		listen        string
		authenticated bool
		optIn         bool
		refused       bool
	}{
		{name: "loopback address", listen: "127.0.0.1:0"},
		{name: "loopback name", listen: "localhost:0"},
		{name: "ipv6 loopback", listen: "[::1]:0"},
		{name: "every interface", listen: "0.0.0.0:8080", refused: true},
		{name: "every interface, written as an omission", listen: ":8080", refused: true},
		{name: "a routable address", listen: "10.0.0.5:8080", refused: true},
		{name: "a name that is not localhost", listen: "mcp.example.com:8080", refused: true},
		{name: "public, but authenticated", listen: "0.0.0.0:8080", authenticated: true},
		{name: "public, and explicitly permitted", listen: "0.0.0.0:8080", optIn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBind(tc.listen, tc.authenticated, tc.optIn)
			if tc.refused && err == nil {
				t.Fatalf("checkBind(%q, authenticated=%v, optIn=%v) = nil, want a refusal",
					tc.listen, tc.authenticated, tc.optIn)
			}
			if !tc.refused && err != nil {
				t.Fatalf("checkBind(%q) = %v, want nil", tc.listen, err)
			}
			if tc.refused && errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
		})
	}
}

// A name that resolves to a loopback address today is still refused: reading
// DNS at startup would make the safety of a configuration depend on an answer
// that can change afterwards without the process noticing.
func TestALoopbackNameIsNotTrustedWithoutBeingLiteral(t *testing.T) {
	if err := checkBind("mcp.example.com:8080", false, false); err == nil {
		t.Fatal("a hostname was treated as loopback")
	}
	if !strings.Contains(mustErr(t, checkBind("nope:8080", false, false)), "FR-70") {
		t.Error("the refusal does not name the requirement it enforces")
	}
}

func TestListenAddressMustBeHostPort(t *testing.T) {
	err := checkBind("127.0.0.1", false, false)
	if err == nil {
		t.Fatal("an address without a port was accepted")
	}
	if errs.ClassOf(err) != errs.ClassUsage {
		t.Errorf("class = %s, want usage", errs.ClassOf(err))
	}
}

// FR-71: a browser request from an origin the operator did not name never
// reaches the handler, and the refusal says nothing about why.
func TestForeignOriginIsRefusedBeforeTheHandler(t *testing.T) {
	var reached bool
	handler, err := guard(spy(&reached), &Options{AllowedOrigins: []string{"https://console.example.com"}})
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{name: "a trusted origin", origin: "https://console.example.com", want: http.StatusOK},
		{name: "a foreign origin", origin: "https://evil.example.com", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader("{}"))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.want == http.StatusForbidden {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", rec.Code)
				}
				if reached {
					t.Error("a refused origin reached the MCP handler")
				}
				if body := rec.Body.String(); strings.Contains(body, tc.origin) {
					t.Errorf("the refusal echoes the caller back to it: %q", body)
				}
				return
			}
			if !reached {
				t.Error("a trusted origin was refused")
			}
		})
	}
}

// A request with no Origin at all is not a browser request. Refusing it would
// refuse every MCP client there is.
func TestARequestWithoutAnOriginIsNotJudgedByOrigin(t *testing.T) {
	var reached bool
	handler, err := guard(spy(&reached), &Options{})
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !reached {
		t.Error("a request carrying no Origin was refused")
	}
}

// An explicit Host allowlist is exactly that: the set, and nothing else.
func TestHostAllowlistIsExact(t *testing.T) {
	var reached bool
	handler := allowHosts(spy(&reached), []string{"mcp.example.com:8080"})

	for _, tc := range []struct {
		host string
		want int
	}{
		{host: "mcp.example.com:8080", want: http.StatusOK},
		{host: "MCP.Example.com:8080", want: http.StatusOK}, // Host is case-insensitive
		{host: "attacker.example.com:8080", want: http.StatusForbidden},
		{host: "mcp.example.com", want: http.StatusForbidden}, // a different port is a different host
	} {
		t.Run(tc.host, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/", http.NoBody)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusForbidden && reached {
				t.Error("a refused Host reached the handler")
			}
		})
	}
}

// An origin the operator wrote down wrong is a startup error, not an origin
// quietly dropped from a list they believe is in force.
func TestAnUnparseableAllowedOriginIsRefused(t *testing.T) {
	_, err := newHandler(&Options{Server: testServer(t), AllowedOrigins: []string{"not an origin"}})
	if err == nil {
		t.Fatal("a malformed allowed origin was accepted")
	}
	if errs.ClassOf(err) != errs.ClassUsage {
		t.Errorf("class = %s, want usage", errs.ClassOf(err))
	}
}

// FR-75: cancelling the context stops the listener and returns cleanly rather
// than cutting the process off mid-answer.
func TestServeDrainsOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	opts := &Options{Server: testServer(t), DrainTimeout: 2 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), opts)
	}()

	// The socket is live before the shutdown is asked for.
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, want nil after a clean drain", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after the context was cancelled")
	}

	if _, err := net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond); err == nil {
		t.Error("the listener is still accepting connections after shutdown")
	}
}

// Serve without a server to serve is an internal error, not a listening
// socket that answers nothing.
func TestServeRefusesWithoutAServer(t *testing.T) {
	err := Serve(t.Context(), &Options{Listen: "127.0.0.1:0"})
	if err == nil {
		t.Fatal("Serve started with no MCP server")
	}
	if errs.ClassOf(err) != errs.ClassInternal {
		t.Errorf("class = %s, want internal", errs.ClassOf(err))
	}
}

// testServer is a minimal MCP server: these tests are about the guards in
// front of it, not about what it publishes.
func testServer(t *testing.T) func() *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "lotsman-test", Version: "v0"}, nil)
	return func() *mcp.Server { return server }
}

// spy stands in for the MCP handler and records whether a request reached it.
func spy(reached *bool) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *reached = true })
}

// mustErr returns the message of an error a test requires to exist.
func mustErr(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}
