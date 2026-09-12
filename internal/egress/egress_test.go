package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// allowing builds the policy an operator gets from --base-url.
func allowing(t *testing.T, baseURL string) Policy {
	t.Helper()
	policy, err := PolicyFromBaseURL(baseURL, Budget{}, false)
	if err != nil {
		t.Fatalf("PolicyFromBaseURL(%q): %v", baseURL, err)
	}
	return policy
}

// An empty policy authorizes nothing: a server URL authored by an untrusted
// document is not permission to call it.
func TestCheckTargetRequiresExplicitAuthorization(t *testing.T) {
	err := Policy{}.CheckTarget(mustURL(t, "https://api.example.com/widgets"))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}

func TestCheckTargetMatchesNormalizedOrigin(t *testing.T) {
	if err := allowing(t, "https://api.example.com:443/v1").CheckTarget(mustURL(t, "https://API.EXAMPLE.com/widgets")); err != nil {
		t.Fatalf("CheckTarget: %v", err)
	}
	if err := allowing(t, "https://api.example.com").CheckTarget(mustURL(t, "https://other.example.com/widgets")); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-origin error = %v, want ErrDenied", err)
	}
}

// Several origins may be allowed at once, and only those.
func TestCheckTargetAllowsEveryConfiguredOrigin(t *testing.T) {
	policy := Policy{AllowedOrigins: []string{"https://api.example.com", "https://cdn.example.com"}}
	for _, target := range []string{"https://api.example.com/x", "https://cdn.example.com/y"} {
		if err := policy.CheckTarget(mustURL(t, target)); err != nil {
			t.Errorf("CheckTarget(%q): %v", target, err)
		}
	}
	if err := policy.CheckTarget(mustURL(t, "https://evil.example.com/x")); !errors.Is(err, ErrDenied) {
		t.Errorf("error = %v, want ErrDenied", err)
	}
}

type redirectTransport struct {
	calls int
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{"https://other.example/private"}},
		Body:       io.NopCloser(strings.NewReader("redirect")),
		Request:    req,
	}, nil
}

func TestClientDeniesRedirects(t *testing.T) {
	transport := &redirectTransport{}
	client := Policy{}.Client(&http.Client{Transport: transport})
	resp, err := client.Get("https://api.example.com/widgets")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if transport.calls != 1 {
		t.Fatalf("RoundTrip calls = %d, want 1", transport.calls)
	}
}

func TestClientDoesNotInheritProxyEnvironmentByDefault(t *testing.T) {
	client := Policy{}.Client(&http.Client{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default client inherited a proxy resolver")
	}
}

// The origin check runs on a name; a name can resolve to anything, including
// the cloud metadata endpoint. So the address is checked again at connect
// time, on every connection (FR-32's neighbour: DNS rebinding).
func TestPrivateAddressesAreRefusedForHostnameOrigins(t *testing.T) {
	policy := Policy{AllowedOrigins: []string{"https://api.example.com"}}

	for _, address := range []string{
		"127.0.0.1:443",      // loopback
		"169.254.169.254:80", // cloud metadata
		"10.0.0.5:443",       // RFC 1918
		"192.168.1.1:443",    // RFC 1918
		"172.16.0.1:443",     // RFC 1918
		"100.64.0.1:443",     // carrier-grade NAT
		"[::1]:443",          // IPv6 loopback
		"[fd00::1]:443",      // IPv6 unique local
		"0.0.0.0:443",        // unspecified
	} {
		if err := policy.checkAddress(address); !errors.Is(err, ErrDenied) {
			t.Errorf("checkAddress(%q) = %v, want ErrDenied", address, err)
		}
	}

	if err := policy.checkAddress("93.184.216.34:443"); err != nil {
		t.Errorf("a public address was refused: %v", err)
	}
}

// Naming a private address is intent, not rebinding: an operator who points
// lotsman at 127.0.0.1 meant it, and refusing would stop no attack.
func TestLiteralPrivateOriginsAreAllowed(t *testing.T) {
	for _, tt := range []struct{ origin, address string }{
		{"http://127.0.0.1:8080", "127.0.0.1:8080"},
		{"http://localhost:8080", "127.0.0.1:8080"},
		{"http://[::1]:8080", "[::1]:8080"},
	} {
		policy := Policy{AllowedOrigins: []string{tt.origin}}
		if err := policy.checkAddress(tt.address); err != nil {
			t.Errorf("origin %q: %v", tt.origin, err)
		}
	}

	// Naming one private address does not allow another: an origin of
	// [::1] is not permission to reach 127.0.0.1.
	policy := Policy{AllowedOrigins: []string{"http://[::1]:8080"}}
	if err := policy.checkAddress("127.0.0.1:8080"); !errors.Is(err, ErrDenied) {
		t.Errorf("error = %v, want ErrDenied: a different private address was allowed", err)
	}
}

// The opt-in exists for the case a hostname legitimately resolves inside a
// private network -- an internal API behind a corporate DNS name.
func TestPrivateNetworksCanBeOptedInto(t *testing.T) {
	policy := Policy{AllowedOrigins: []string{"https://internal.corp"}, AllowPrivateNetworks: true}
	if err := policy.checkAddress("10.1.2.3:443"); err != nil {
		t.Errorf("opt-in did not take effect: %v", err)
	}
}

// FR-32: one overall timeout is not enough. A server that accepts the
// connection and then dribbles bytes stays inside any total budget.
func TestClientAppliesEveryPhaseOfTheBudget(t *testing.T) {
	policy := Policy{
		AllowedOrigins: []string{"https://api.example.com"},
		Budget: Budget{
			Total: time.Second, Connect: 2 * time.Second,
			TLSHandshake: 3 * time.Second, ResponseHeader: 4 * time.Second,
			IdleConnection: 5 * time.Second,
		},
	}
	client := policy.Client(nil)
	if client.Timeout != time.Second {
		t.Errorf("client timeout = %v", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second ||
		transport.ResponseHeaderTimeout != 4*time.Second ||
		transport.IdleConnTimeout != 5*time.Second {
		t.Errorf("phase budgets not applied: %+v", transport)
	}
	if transport.Proxy != nil {
		t.Error("the transport inherited a proxy resolver")
	}
}

func TestDefaultBudgetIsAppliedWhenUnset(t *testing.T) {
	client := Policy{}.Client(nil)
	if client.Timeout != DefaultBudget().Total {
		t.Errorf("timeout = %v, want the default budget", client.Timeout)
	}
}

// The guard is in the dial path, so it stops a connection that the origin
// check let through.
func TestDialGuardRefusesRebindingEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request reached a private address")
	}))
	defer srv.Close()

	// An origin named by hostname, resolved (by the test's own dialer) to the
	// loopback address the server listens on.
	policy := Policy{AllowedOrigins: []string{"http://api.example.com"}}
	client := policy.Client(nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if err := policy.checkAddress(srv.Listener.Addr().String()); err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.example.com/x", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}
