package inbound

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/redact"
)

const introspectionSecret = "CANARY-introspection-9c4e-DO-NOT-LEAK"

// authServer answers introspection calls with whatever a test hands it.
type authServer struct {
	t        *testing.T
	server   *httptest.Server
	answer   func(w http.ResponseWriter, r *http.Request)
	sawAuth  string
	sawToken string
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()
	a := &authServer{t: t}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.sawAuth = r.Header.Get("Authorization")
		if err := r.ParseForm(); err == nil {
			a.sawToken = r.Form.Get("token")
		}
		a.answer(w, r)
	}))
	t.Cleanup(a.server.Close)
	return a
}

// active is the answer for a token the authorization server likes.
func (a *authServer) active(fields map[string]any) func(http.ResponseWriter, *http.Request) {
	body := map[string]any{
		"active": true,
		"sub":    "user-42",
		"scope":  "mcp:call",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"aud":    testResource,
		"iss":    testIssuer,
	}
	for name, value := range fields {
		if value == nil {
			delete(body, name)
			continue
		}
		body[name] = value
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

// guardFor builds an introspecting guard pointed at this server.
func (a *authServer) guardFor(t *testing.T) Guard {
	t.Helper()
	t.Setenv("LOTSMAN_TEST_INTROSPECTION", introspectionSecret)
	guard, err := New(&config.InboundAuth{
		Mode: config.InboundModeOAuth,
		OAuth: config.InboundOAuth{
			Issuer:   testIssuer,
			Resource: testResource,
			Introspection: config.Introspection{
				URL:             a.server.URL,
				ClientID:        "lotsman",
				ClientSecretRef: config.SecretRef("env:LOTSMAN_TEST_INTROSPECTION"),
			},
		},
	}, discardLogger(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return guard
}

// FR-81's other path: a token that carries no claims of its own is validated
// by asking the authorization server that minted it.
func TestIntrospectionAdmitsOnlyAnActiveToken(t *testing.T) {
	a := newAuthServer(t)
	guard := a.guardFor(t)

	cases := []struct {
		name   string
		answer func(http.ResponseWriter, *http.Request)
		admit  bool
	}{
		{name: "active, for this resource", answer: a.active(nil), admit: true},
		{name: "an aud carried in a list",
			answer: a.active(map[string]any{"aud": []any{"https://other.example.com/", testResource}}), admit: true},
		{name: "inactive", answer: a.active(map[string]any{"active": false})},
		{name: "minted for another resource",
			answer: a.active(map[string]any{"aud": "https://other.example.com/"})},
		{name: "no audience at all", answer: a.active(map[string]any{"aud": nil})},
		{name: "another authorization server",
			answer: a.active(map[string]any{"iss": "https://attacker.example.com"})},
		{name: "no expiration", answer: a.active(map[string]any{"exp": nil})},
		{
			name: "the provider refuses to answer",
			answer: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no", http.StatusInternalServerError)
			},
		},
		{
			name: "the provider answers with something that is not JSON",
			answer: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html>sign in</html>"))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a.answer = tc.answer
			status, reached := call(t, guard, "opaque-token-value")

			if tc.admit {
				if status != http.StatusOK || !reached {
					t.Fatalf("an active token was refused: status %d", status)
				}
				return
			}
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
			if reached {
				t.Error("an unvalidated token reached the handler")
			}
		})
	}
}

// A provider that is slow must not hold a request open for as long as it
// likes: the timeout is bounded, and the bound is a refusal rather than a
// wait. Introspection puts the authorization server on the hot path, which is
// its cost; it must not also put it in charge of how long a request lasts.
func TestIntrospectionTimesOutIntoARefusal(t *testing.T) {
	const slower = time.Second
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(slower):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("LOTSMAN_TEST_INTROSPECTION", introspectionSecret)
	guard, err := New(&config.InboundAuth{
		Mode: config.InboundModeOAuth,
		OAuth: config.InboundOAuth{
			Issuer:   testIssuer,
			Resource: testResource,
			Introspection: config.Introspection{
				URL:             server.URL,
				ClientID:        "lotsman",
				ClientSecretRef: config.SecretRef("env:LOTSMAN_TEST_INTROSPECTION"),
				Timeout:         "100ms",
			},
		},
	}, discardLogger(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := time.Now()
	status, reached := call(t, guard, "opaque-token-value")
	elapsed := time.Since(started)

	if status != http.StatusUnauthorized || reached {
		t.Errorf("status = %d, reached = %v; want a refusal", status, reached)
	}
	if elapsed >= slower {
		t.Errorf("the request waited %s; the configured timeout was 100ms", elapsed)
	}
}

// Introspection is an authenticated call with lotsman's own credential, and
// that credential has nothing to do with the token being asked about.
func TestIntrospectionAuthenticatesItselfWithoutLeaking(t *testing.T) {
	a := newAuthServer(t)
	a.answer = a.active(nil)
	guard := a.guardFor(t)

	if status, _ := call(t, guard, "opaque-token-value"); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(a.sawAuth, "Basic ") {
		t.Errorf("the introspection call did not authenticate itself: %q", a.sawAuth)
	}
	if a.sawToken != "opaque-token-value" {
		t.Errorf("the token under inspection was not sent: %q", a.sawToken)
	}
	// The client secret travels in the Basic header and nowhere else -- in
	// particular it is not the thing being introspected.
	if strings.Contains(a.sawToken, introspectionSecret) {
		t.Error("the introspection credential was sent as the token")
	}
}

// Two ways to validate one token are two answers waiting to disagree.
func TestKeysAndIntrospectionCannotBothBeConfigured(t *testing.T) {
	_, err := config.Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: oauth
    oauth:
      issuer: https://issuer.example.com
      resource: https://mcp.example.com/
      jwksURI: https://issuer.example.com/jwks
      introspection:
        url: https://issuer.example.com/introspect
        clientID: lotsman
        clientSecretRef: env:SECRET
`))
	if err == nil {
		t.Fatal("a configuration naming both was accepted")
	}
	if !strings.Contains(err.Error(), "never both") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The introspection credential joins the redaction registry like every other
// secret, so it cannot reach the log through the path most likely to carry it
// -- a provider failing, with the request in the error message.
func TestTheIntrospectionSecretNeverReachesTheLog(t *testing.T) {
	var logged strings.Builder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized_client", http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("LOTSMAN_TEST_INTROSPECTION", introspectionSecret)
	guard, err := New(&config.InboundAuth{
		Mode: config.InboundModeOAuth,
		OAuth: config.InboundOAuth{
			Issuer:   testIssuer,
			Resource: testResource,
			Introspection: config.Introspection{
				URL:             server.URL,
				ClientID:        "lotsman",
				ClientSecretRef: config.SecretRef("env:LOTSMAN_TEST_INTROSPECTION"),
			},
		},
	}, slog.New(redact.NewHandler(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if status, _ := call(t, guard, "opaque-token-value"); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if logged.Len() == 0 {
		t.Fatal("nothing was logged, so this assertion proves nothing")
	}
	if strings.Contains(logged.String(), introspectionSecret) {
		t.Errorf("the introspection credential reached the log:\n%s", logged.String())
	}
}
