package main_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// identityProvider is a real authorization server for the end-to-end test:
// HTTPS, a key set, and tokens signed with the key it publishes.
//
// HTTPS because the configuration refuses anything else, and it refuses
// anything else because a development shortcut in an authentication path is a
// production configuration eventually. The subprocess is pointed at this
// server's certificate through SSL_CERT_FILE, which is how an operator with a
// private certificate authority would do it too.
type identityProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	caFile string
}

func newIdentityProvider(t *testing.T) *identityProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &identityProvider{key: key}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": "key-1",
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(p.server.Close)

	p.caFile = filepath.Join(t.TempDir(), "provider.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.server.Certificate().Raw})
	if err := os.WriteFile(p.caFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// token signs one for the given subject.
func (p *identityProvider) token(t *testing.T, subject, scope string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": p.server.URL,
		"sub": subject,
		"aud": "https://mcp.example.com/",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	if scope != "" {
		claims["scope"] = scope
	}
	signed := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed.Header["kid"] = "key-1"
	out, err := signed.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// config writes a resource-server configuration pointing at this provider.
func (p *identityProvider) config(t *testing.T, scopes string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	document := `apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: oauth
    oauth:
      issuer: ` + p.server.URL + `
      resource: https://mcp.example.com/
      jwksURI: ` + p.server.URL + `/jwks
`
	if scopes != "" {
		document += "      requiredScopes: [" + scopes + "]\n"
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOAuthEndToEnd is US-1: a token a real identity provider issued opens
// the door, and nothing else does.
func TestOAuthEndToEnd(t *testing.T) {
	p := newIdentityProvider(t)
	endpoint, stderr := serveHTTPEnv(t,
		[]string{"SSL_CERT_FILE=" + p.caFile},
		"--listen", "127.0.0.1:0", miniSpecPath(t), "--lax",
		"--config", p.config(t, "mcp:call"), "--log-level", "debug")

	t.Run("a valid token reaches the catalog", func(t *testing.T) {
		client := mcp.NewClient(&mcp.Implementation{Name: "oauth-e2e", Version: "v0"}, approving())
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: tokenTransport{token: p.token(t, "user-42", "mcp:call")}},
		}, nil)
		if err != nil {
			t.Fatalf("connect with a valid token: %v\nstderr:\n%s", err, stderr.String())
		}
		defer func() { _ = session.Close() }()
		if _, err := session.ListTools(t.Context(), nil); err != nil {
			t.Fatalf("tools/list: %v", err)
		}
	})

	t.Run("a token without the required scope does not", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.token(t, "user-42", "openid"))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", res.StatusCode)
		}
		if got := res.Header.Get("WWW-Authenticate"); !strings.Contains(got, "mcp:call") {
			t.Errorf("the challenge does not name the scope: %q", got)
		}
	})

	t.Run("a token this provider did not sign does not", func(t *testing.T) {
		other := newIdentityProvider(t)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+other.token(t, "attacker", "mcp:call"))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", res.StatusCode)
		}
	})
}

// TestTheAuditRecordNamesTheCaller is T414 end to end: the record says who
// asked, and never what they presented.
func TestTheAuditRecordNamesTheCaller(t *testing.T) {
	requests := make(chan struct{}, 4)
	api := newJSONAPI(t, requests)
	defer api.Close()

	p := newIdentityProvider(t)
	token := p.token(t, "user-42", "")
	endpoint, stderr := serveHTTPEnv(t,
		[]string{"SSL_CERT_FILE=" + p.caFile},
		"--listen", "127.0.0.1:0", miniSpecPath(t), "--lax",
		"--config", p.config(t, ""), "--base-url", api.URL, "--log-level", "debug")

	client := mcp.NewClient(&mcp.Implementation{Name: "oauth-audit", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: tokenTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}},
	}); err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}

	record := findAudit(stderr.String())
	if record == "" {
		t.Fatalf("no audit record was written\nstderr:\n%s", stderr.String())
	}
	if !strings.Contains(record, "subject=user-42") {
		t.Errorf("the record does not name the caller:\n%s", record)
	}
	// The subject, never the token.
	if strings.Contains(stderr.String(), token) {
		t.Error("the caller's token reached the log")
	}
}
