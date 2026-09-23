package inbound

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// FR-85: a forwarding header from anyone but a configured proxy is a claim
// the caller made about itself. It is removed rather than ignored, so that a
// later feature reading X-Forwarded-For gets the truth without having been
// told to be careful.
func TestForwardingHeadersSurviveOnlyFromATrustedProxy(t *testing.T) {
	cases := []struct {
		name    string
		trusted []string
		peer    string
		kept    bool
	}{
		{name: "no proxies configured", peer: "203.0.113.9:4444"},
		{name: "an untrusted peer", trusted: []string{"10.0.0.1"}, peer: "203.0.113.9:4444"},
		{name: "the configured address", trusted: []string{"10.0.0.1"}, peer: "10.0.0.1:4444", kept: true},
		{name: "inside the configured range", trusted: []string{"10.0.0.0/8"}, peer: "10.9.9.9:4444", kept: true},
		{name: "outside the configured range", trusted: []string{"10.0.0.0/8"}, peer: "192.168.1.1:4444"},
		{name: "an unparseable peer", trusted: []string{"10.0.0.0/8"}, peer: "not-an-address"},
		{name: "ipv6, configured", trusted: []string{"::1"}, peer: "[::1]:4444", kept: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen http.Header
			handler, err := StripUntrustedForwarding(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = r.Header.Clone()
			}), tc.trusted)
			if err != nil {
				t.Fatalf("StripUntrustedForwarding: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", "198.51.100.7")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", "mcp.example.com")
			req.Header.Set("X-Real-Ip", "198.51.100.7")
			req.Header.Set("Forwarded", "for=198.51.100.7")
			req.Header.Set("Authorization", "Bearer keep-me")
			handler.ServeHTTP(httptest.NewRecorder(), req)

			for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-Ip", "Forwarded"} {
				got := seen.Get(name)
				if tc.kept && got == "" {
					t.Errorf("%s was stripped from a trusted proxy", name)
				}
				if !tc.kept && got != "" {
					t.Errorf("%s survived from an untrusted peer: %q", name, got)
				}
			}
			// Stripping is about claims of provenance, not about the request.
			if seen.Get("Authorization") != "Bearer keep-me" {
				t.Error("an unrelated header was removed")
			}
		})
	}
}

// A trusted proxy the operator wrote down wrong is a startup error, not a
// proxy quietly dropped from a list they believe is in force.
func TestAMalformedTrustedProxyIsRefused(t *testing.T) {
	for _, entry := range []string{"proxy.example.com", "10.0.0.0/64", "999.1.1.1"} {
		if _, err := StripUntrustedForwarding(http.NotFoundHandler(), []string{entry}); err == nil {
			t.Errorf("trusted proxy %q was accepted", entry)
		}
	}
}
