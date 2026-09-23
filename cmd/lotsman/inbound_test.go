package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// inboundCanary is the token that lets a caller talk to lotsman. It must never
// appear anywhere else -- least of all in a request lotsman makes.
const inboundCanary = "CANARY-inbound-5d02b71e-DO-NOT-LEAK"

// bearerConfig writes a configuration that puts a shared secret on the door.
func bearerConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: static-bearer
    tokenRef: env:LOTSMAN_INBOUND_CANARY
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tokenTransport adds the inbound credential the way a deployed client would.
type tokenTransport struct {
	token string
	next  http.RoundTripper
}

func (t tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(clone)
}

// TestInboundBearerAdmitsOnlyTheConfiguredCaller is FR-79 over the wire: the
// endpoint is reachable, and reaching it is not the same as being allowed to
// use it.
func TestInboundBearerAdmitsOnlyTheConfiguredCaller(t *testing.T) {
	endpoint, stderr := serveHTTPEnv(t,
		[]string{"LOTSMAN_INBOUND_CANARY=" + inboundCanary},
		"--listen", "127.0.0.1:0", miniSpecPath(t), "--lax", "--config", bearerConfig(t))

	t.Run("without a token", func(t *testing.T) {
		res, err := http.Post(endpoint, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", res.StatusCode)
		}
		// FR-84: the refusal names the scheme that would satisfy it.
		if got := res.Header.Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("WWW-Authenticate = %q, want Bearer", got)
		}
	})

	t.Run("with the token", func(t *testing.T) {
		client := mcp.NewClient(&mcp.Implementation{Name: "inbound", Version: "v0"}, approving())
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: tokenTransport{token: inboundCanary}},
		}, nil)
		if err != nil {
			t.Fatalf("connect with the configured token: %v\nstderr:\n%s", err, stderr.String())
		}
		defer func() { _ = session.Close() }()
		if _, err := session.ListTools(t.Context(), nil); err != nil {
			t.Fatalf("tools/list: %v", err)
		}
	})
}

// TestAuthenticatedEndpointMayBindPublicly is the other half of FR-70: the
// bind rule refuses an endpoint nothing authenticates, not a public interface
// as such.
func TestAuthenticatedEndpointMayBindPublicly(t *testing.T) {
	endpoint, _ := serveHTTPEnv(t,
		[]string{"LOTSMAN_INBOUND_CANARY=" + inboundCanary},
		"--listen", "0.0.0.0:0", miniSpecPath(t), "--lax", "--config", bearerConfig(t))
	if endpoint == "" {
		t.Fatal("an authenticated public bind did not start")
	}
}

// TestInboundTokenNeverReachesTheUpstream is FR-83: an inbound identity is
// permission to talk to lotsman, never an upstream credential.
//
// The two live in one process and both travel in an Authorization header, one
// inbound and one outbound. That is exactly why this is a test and not a
// comment.
func TestInboundTokenNeverReachesTheUpstream(t *testing.T) {
	seen := make(chan string, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request strings.Builder
		request.WriteString(r.Method + " " + r.URL.RequestURI() + "\n")
		for name, values := range r.Header {
			request.WriteString(name + ": " + strings.Join(values, ",") + "\n")
		}
		seen <- request.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"p-1","name":"Murka"}`))
	}))
	defer api.Close()

	endpoint, stderr := serveHTTPEnv(t,
		[]string{"LOTSMAN_INBOUND_CANARY=" + inboundCanary},
		"--listen", "127.0.0.1:0", miniSpecPath(t), "--lax",
		"--config", bearerConfig(t), "--base-url", api.URL, "--log-level", "debug")

	client := mcp.NewClient(&mcp.Implementation{Name: "inbound-canary", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: tokenTransport{token: inboundCanary}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("the authenticated call failed: %+v", res.Content)
	}

	select {
	case request := <-seen:
		if strings.Contains(request, inboundCanary) {
			t.Errorf("the inbound token was forwarded upstream:\n%s", request)
		}
	default:
		t.Fatal("the upstream received no request, so this test proves nothing")
	}

	result, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), inboundCanary) {
		t.Errorf("the inbound token reached the tool result: %s", result)
	}
	if strings.Contains(stderr.String(), inboundCanary) {
		t.Errorf("the inbound token reached the log:\n%s", stderr.String())
	}
}

// TestOAuthModeRefusesToStart: a configuration written against the frozen
// specification must fail loudly on a build that cannot carry it out. The
// alternative is an operator who believes their endpoint is behind an
// authorization server.
func TestOAuthModeRefusesToStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: oauth
`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(buildBinary(t), "serve", miniSpecPath(t), "--lax",
		"--transport", "http", "--listen", "127.0.0.1:0", "--config", path).CombinedOutput()
	if err == nil {
		t.Fatalf("a build without oauth served an oauth configuration\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 4 {
		t.Errorf("exit code = %d, want 4 (unsupported)", code)
	}
	if !strings.Contains(string(out), "005") {
		t.Errorf("the refusal does not say where the capability is coming from:\n%s", out)
	}
}
