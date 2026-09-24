package main_test

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	upstreamClientSecret = "CANARY-upstream-client-secret-3f7d-DO-NOT-LEAK"
	upstreamAccessToken  = "CANARY-upstream-access-token-91ce-DO-NOT-LEAK"
)

// authorizationServer is a real client-credentials token endpoint over HTTPS,
// which is what M4a's exit criterion asks for: the mint is a form POST whose
// client authentication, grant type and response shape are the parts that go
// wrong, and a stub returning a fixed string would test none of them.
type authorizationServer struct {
	server *httptest.Server
	caFile string
	mints  atomic.Int64
}

func newAuthorizationServer(t *testing.T) *authorizationServer {
	t.Helper()
	a := &authorizationServer{}
	a.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		// Client authentication, either style.
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		if id != "lotsman" || secret != upstreamClientSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		a.mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": upstreamAccessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        r.Form.Get("scope"),
		})
	}))
	t.Cleanup(a.server.Close)

	a.caFile = filepath.Join(t.TempDir(), "provider.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.server.Certificate().Raw})
	if err := os.WriteFile(a.caFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return a
}

// oauthSpec is a document whose security is an OAuth2 client-credentials
// flow -- the shape that was refused before M4a.
func oauthSpec(t *testing.T, tokenURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	document := `openapi: 3.0.3
info: { title: Minted, version: "1.0" }
security:
  - serviceAuth: [read]
paths:
  /things:
    get:
      operationId: listThings
      responses: { "200": { description: ok } }
components:
  securitySchemes:
    serviceAuth:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: ` + tokenURL + `
          scopes: { read: read things }
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestServiceOAuthEndToEnd is M4a's exit criterion: an operation whose
// security is a client-credentials flow is called with a token lotsman
// obtained from a real token endpoint.
func TestServiceOAuthEndToEnd(t *testing.T) {
	provider := newAuthorizationServer(t)

	var presented atomic.Value
	presented.Store("")
	apiCalls := atomic.Int64{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		presented.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"things":[]}`))
	}))
	defer api.Close()

	configPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
execution:
  allowedOrigins:
    - `+provider.server.URL+`
authProfiles:
  serviceAuth:
    scheme: oauth2-client-credentials
    tokenURL: `+provider.server.URL+`
    clientID: lotsman
    clientSecretRef: env:LOTSMAN_UPSTREAM_SECRET
    scopes: [read]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	session, stderr := stdioSessionEnv(t,
		[]string{
			"LOTSMAN_UPSTREAM_SECRET=" + upstreamClientSecret,
			"SSL_CERT_FILE=" + provider.caFile,
		},
		oauthSpec(t, provider.server.URL+"/token"), "--config", configPath,
		"--base-url", api.URL, "--log-level", "debug")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_things"})
	if err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("the call failed: %+v\nstderr:\n%s", res.Content, stderr.String())
	}

	got, _ := presented.Load().(string)
	if got != "Bearer "+upstreamAccessToken {
		t.Errorf("the API received %q, want the minted token", got)
	}
	if got := provider.mints.Load(); got != 1 {
		t.Errorf("minted %d tokens for one call, want 1", got)
	}

	// A second call reuses it: the token belongs to the credential, not to
	// the call.
	if _, second := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_things"}); second != nil {
		t.Fatalf("second tools/call: %v", second)
	}
	if got := provider.mints.Load(); got != 1 {
		t.Errorf("a second call minted again: %d tokens in total", got)
	}

	// T510: two secrets in one process, and neither in any channel.
	result, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"client secret": upstreamClientSecret,
		"access token":  upstreamAccessToken,
	} {
		if strings.Contains(string(result), value) {
			t.Errorf("the %s reached the tool result:\n%s", name, result)
		}
		if strings.Contains(stderr.String(), value) {
			t.Errorf("the %s reached the log", name)
		}
	}

	// Including the audit record, which is written for the very call that
	// carried the minted token.
	record := findAudit(stderr.String())
	if record == "" {
		t.Fatalf("no audit record was written\nstderr:\n%s", stderr.String())
	}
	if strings.Contains(record, upstreamAccessToken) || strings.Contains(record, upstreamClientSecret) {
		t.Errorf("a secret reached the audit record:\n%s", record)
	}
}

// And the report, which an operator may paste into an issue. It should say
// the profile mints and what it asks for, and neither secret.
func TestTheReportDescribesAMintingProfileWithoutItsSecret(t *testing.T) {
	provider := newAuthorizationServer(t)
	configPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  serviceAuth:
    scheme: oauth2-client-credentials
    tokenURL: `+provider.server.URL+`
    clientID: lotsman
    clientSecretRef: env:LOTSMAN_UPSTREAM_SECRET
    scopes: [read]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLIEnv(t,
		[]string{"LOTSMAN_UPSTREAM_SECRET=" + upstreamClientSecret},
		"inspect", oauthSpec(t, provider.server.URL+"/token"), "--config", configPath, "--json")
	if code != 0 {
		t.Fatalf("inspect failed (%d): %s", code, stderr)
	}

	var report struct {
		Credentials []struct {
			Name     string   `json:"name"`
			Scheme   string   `json:"scheme"`
			Mints    bool     `json:"mints"`
			TokenURL string   `json:"tokenURL"`
			Scopes   []string `json:"scopes"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if len(report.Credentials) != 1 {
		t.Fatalf("credentials = %+v, want one", report.Credentials)
	}
	described := report.Credentials[0]
	if !described.Mints {
		t.Error("the report does not say the profile obtains its token")
	}
	if len(described.Scopes) != 1 || described.Scopes[0] != "read" {
		t.Errorf("scopes = %v, want [read]", described.Scopes)
	}
	// The token endpoint is not a secret; the secret is.
	if strings.Contains(stdout, upstreamClientSecret) {
		t.Error("the report carries the client secret")
	}
}
