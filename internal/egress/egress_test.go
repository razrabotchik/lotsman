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
	empty := Policy{}
	err := empty.CheckTarget(mustURL(t, "https://api.example.com/widgets"))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}

func TestCheckTargetMatchesNormalizedOrigin(t *testing.T) {
	broad := allowing(t, "https://api.example.com:443/v1")
	if err := broad.CheckTarget(mustURL(t, "https://API.EXAMPLE.com/widgets")); err != nil {
		t.Fatalf("CheckTarget: %v", err)
	}
	narrow := allowing(t, "https://api.example.com")
	if err := narrow.CheckTarget(mustURL(t, "https://other.example.com/widgets")); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-origin error = %v, want ErrDenied", err)
	}
}

// Several origins may be allowed at once, and only those.
func TestCheckTargetAllowsEveryConfiguredOrigin(t *testing.T) {
	policy := &Policy{AllowedOrigins: []string{"https://api.example.com", "https://cdn.example.com"}}
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
	empty := Policy{}
	client := empty.Client(&http.Client{Transport: transport})
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
	empty := Policy{}
	client := empty.Client(&http.Client{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default client inherited a proxy resolver")
	}
}

// fakeResolver answers with whatever a test says a name resolves to, which is
// how "this hostname points at the metadata endpoint" can be asserted without
// owning a DNS zone.
type fakeResolver struct {
	addresses []net.IPAddr
	err       error
}

func (r fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, r.err
}

func resolving(t *testing.T, origin string, to ...string) Policy {
	t.Helper()
	addresses := make([]net.IPAddr, 0, len(to))
	for _, address := range to {
		ip := net.ParseIP(address)
		if ip == nil {
			t.Fatalf("bad test address %q", address)
		}
		addresses = append(addresses, net.IPAddr{IP: ip})
	}
	return Policy{
		AllowedOrigins: []string{origin},
		resolver:       fakeResolver{addresses: addresses},
		// These addresses are not meant to answer: a short connect budget
		// keeps "the policy allowed it" fast to observe.
		Budget: Budget{Connect: 50 * time.Millisecond},
	}
}

// dialing exercises the real dial path: the transport hands the *hostname*
// from the URL to DialContext, which is precisely why the check cannot be a
// string comparison on "the address".
func dialing(t *testing.T, policy *Policy, target string) error {
	t.Helper()
	client := policy.Client(nil)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

// The origin check runs on a name; a name can resolve to anything, including
// the cloud metadata endpoint. So the resolved address is checked before the
// connection is made -- and the connection is then made to the address that
// was checked, so no second lookup can change the answer.
func TestHostnameResolvingIntoAPrivateRangeIsRefused(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1",       // loopback
		"169.254.169.254", // cloud metadata
		"10.0.0.5",        // RFC 1918
		"192.168.1.1",     // RFC 1918
		"172.16.0.1",      // RFC 1918
		"100.64.0.1",      // carrier-grade NAT
		"::1",             // IPv6 loopback
		"fd00::1",         // IPv6 unique local
		"0.0.0.0",         // unspecified
	} {
		t.Run(address, func(t *testing.T) {
			policy := resolving(t, "https://api.example.com", address)
			err := dialing(t, &policy, "https://api.example.com/widgets")
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("error = %v, want ErrDenied", err)
			}
			if !strings.Contains(err.Error(), address) {
				t.Errorf("error = %q, want it to name the address it refused", err)
			}
		})
	}
}

// A name that resolves to a public address is not refused (the connection then
// fails for its own reasons, which is not a policy decision).
func TestHostnameResolvingToAPublicAddressIsNotRefused(t *testing.T) {
	policy := resolving(t, "http://api.example.com", "203.0.113.10")
	if err := dialing(t, &policy, "http://api.example.com/widgets"); errors.Is(err, ErrDenied) {
		t.Fatalf("a public address was refused: %v", err)
	}
}

// Several answers, one of them private: the private one is skipped rather than
// making the whole name unusable... but if *every* answer is private, the call
// is refused.
func TestEveryResolvedAddressIsChecked(t *testing.T) {
	policy := resolving(t, "https://api.example.com", "10.0.0.1", "192.168.0.1")
	if err := dialing(t, &policy, "https://api.example.com/x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}

// Naming a private address is intent, not rebinding: an operator who points
// lotsman at 127.0.0.1 meant it, and refusing would stop no attack.
func TestLiteralPrivateOriginsAreAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	policy := &Policy{AllowedOrigins: []string{srv.URL}}
	if err := dialing(t, policy, srv.URL+"/x"); err != nil {
		t.Errorf("an origin named as a loopback address was refused: %v", err)
	}

	// localhost is the same statement by name.
	host := "http://localhost:" + portOf(t, srv.URL)
	viaLocalhost := &Policy{AllowedOrigins: []string{host}}
	if err := dialing(t, viaLocalhost, host+"/x"); err != nil {
		t.Errorf("an origin named localhost was refused: %v", err)
	}
}

// But a *hostname* is never literal, however innocent it looks: that is the
// case the guard exists for.
func TestHostnameIsNeverTreatedAsLiteral(t *testing.T) {
	policy := resolving(t, "https://localtest.example", "127.0.0.1")
	if err := dialing(t, &policy, "https://localtest.example/x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}

// The opt-in exists for the case a hostname legitimately resolves inside a
// private network -- an internal API behind a corporate DNS name.
func TestPrivateNetworksCanBeOptedInto(t *testing.T) {
	policy := resolving(t, "https://internal.corp", "10.1.2.3")
	policy.AllowPrivateNetworks = true
	if err := dialing(t, &policy, "https://internal.corp/x"); errors.Is(err, ErrDenied) {
		t.Fatalf("opt-in did not take effect: %v", err)
	}
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Port()
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
	empty := Policy{}
	client := empty.Client(nil)
	if client.Timeout != DefaultBudget().Total {
		t.Errorf("timeout = %v, want the default budget", client.Timeout)
	}
}
