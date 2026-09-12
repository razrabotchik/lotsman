package egress

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCheckTargetRequiresExplicitAuthorization(t *testing.T) {
	err := CheckTarget(mustURL(t, "https://api.example.com/widgets"), "")
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
}

func TestCheckTargetMatchesNormalizedOrigin(t *testing.T) {
	if err := CheckTarget(mustURL(t, "https://API.EXAMPLE.com/widgets"), "https://api.example.com:443/v1"); err != nil {
		t.Fatalf("CheckTarget: %v", err)
	}
	if err := CheckTarget(mustURL(t, "https://other.example.com/widgets"), "https://api.example.com"); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-origin error = %v, want ErrDenied", err)
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
	client := Client(&http.Client{Transport: transport})
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
	client := Client(&http.Client{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default client inherited a proxy resolver")
	}
}
