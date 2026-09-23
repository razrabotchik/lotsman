package inbound

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/razrabotchik/lotsman/internal/config"
)

// provider is an authorization server for tests: a key pair, a JWKS
// endpoint, and a token factory. Refusals are asserted against the real
// middleware rather than against a helper, because the thing under test is
// what a request actually meets.
type provider struct {
	t       *testing.T
	key     *rsa.PrivateKey
	other   *rsa.PrivateKey
	kid     string
	server  *httptest.Server
	fetches atomic.Int64
	// fail makes the key set endpoint unavailable.
	fail atomic.Bool
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{t: t, key: key, other: other, kid: "key-1"}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.fetches.Add(1)
		if p.fail.Load() {
			http.Error(w, "the provider is having a bad day", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(p.jwks())
	}))
	t.Cleanup(p.server.Close)
	return p
}

// jwks renders the current key set.
func (p *provider) jwks() []byte {
	document := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": p.kid,
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()),
	}}}
	raw, err := json.Marshal(document)
	if err != nil {
		p.t.Fatal(err)
	}
	return raw
}

// claims is a token worth accepting; each test spoils one thing.
type claims struct {
	issuer    string
	audience  any
	expires   time.Time
	notBefore time.Time
	scope     string
	subject   string
	kid       string
	signWith  *rsa.PrivateKey
	method    jwt.SigningMethod
}

const (
	testIssuer   = "https://issuer.example.com"
	testResource = "https://mcp.example.com/"
)

func (p *provider) defaults() *claims {
	return &claims{
		issuer:   testIssuer,
		audience: testResource,
		expires:  time.Now().Add(time.Hour),
		subject:  "user-42",
		kid:      p.kid,
		signWith: p.key,
		method:   jwt.SigningMethodRS256,
	}
}

// mint signs a token with the given claims.
func (p *provider) mint(c *claims) string {
	p.t.Helper()
	body := jwt.MapClaims{"iss": c.issuer, "sub": c.subject}
	if c.audience != nil {
		body["aud"] = c.audience
	}
	if !c.expires.IsZero() {
		body["exp"] = c.expires.Unix()
	}
	if !c.notBefore.IsZero() {
		body["nbf"] = c.notBefore.Unix()
	}
	if c.scope != "" {
		body["scope"] = c.scope
	}
	token := jwt.NewWithClaims(c.method, body)
	token.Header["kid"] = c.kid

	if c.method == jwt.SigningMethodNone {
		signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			p.t.Fatal(err)
		}
		return signed
	}
	signed, err := token.SignedString(c.signWith)
	if err != nil {
		p.t.Fatal(err)
	}
	return signed
}

// oauthConfig points at this provider.
func (p *provider) oauthConfig() *config.InboundAuth {
	return &config.InboundAuth{
		Mode: config.InboundModeOAuth,
		OAuth: config.InboundOAuth{
			Issuer:   testIssuer,
			Resource: testResource,
			JWKSURI:  p.server.URL,
		},
	}
}

// call runs one request through the guard and reports the status.
func call(t *testing.T, guard Guard, token string) (status int, reached bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	guard.Middleware(spy(&reached)).ServeHTTP(rec, req)
	return rec.Code, reached
}

// TestOAuthAdmitsOnlyAValidToken is FR-81, one row per way to be wrong.
func TestOAuthAdmitsOnlyAValidToken(t *testing.T) {
	p := newProvider(t)
	guard, err := New(p.oauthConfig(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !guard.Required {
		t.Fatal("an oauth guard does not report itself as required")
	}

	cases := []struct {
		name   string
		claims func(c *claims)
		admit  bool
	}{
		{name: "a token this resource server should accept", claims: func(*claims) {}, admit: true},
		{name: "an aud carried in a list, as many providers emit it",
			claims: func(c *claims) { c.audience = []string{"https://other.example.com/", testResource} }, admit: true},
		{name: "a different issuer", claims: func(c *claims) { c.issuer = "https://attacker.example.com" }},
		{name: "an audience for another resource of the same issuer",
			claims: func(c *claims) { c.audience = "https://other.example.com/" }},
		{name: "no audience at all", claims: func(c *claims) { c.audience = nil }},
		{name: "expired", claims: func(c *claims) { c.expires = time.Now().Add(-2 * time.Hour) }},
		{name: "not valid yet", claims: func(c *claims) { c.notBefore = time.Now().Add(time.Hour) }},
		{name: "no expiry", claims: func(c *claims) { c.expires = time.Time{} }},
		{name: "signed by a key the issuer does not publish",
			claims: func(c *claims) { c.signWith = p.other }},
		{name: "a kid nobody issued", claims: func(c *claims) { c.kid = "key-does-not-exist" }},
		{name: "no signature at all", claims: func(c *claims) { c.method = jwt.SigningMethodNone }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := p.defaults()
			tc.claims(c)
			status, reached := call(t, guard, p.mint(c))

			if tc.admit {
				if status != http.StatusOK || !reached {
					t.Fatalf("a valid token was refused: status %d", status)
				}
				return
			}
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
			if reached {
				t.Error("an unverified token reached the handler")
			}
		})
	}
}

// A token that is not a token, and no token at all.
func TestOAuthRefusesMalformedCredentials(t *testing.T) {
	p := newProvider(t)
	guard, err := New(p.oauthConfig(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, token := range []string{"", "not.a.token", "a.b", strings.Repeat("x", 4096)} {
		if status, reached := call(t, guard, token); status != http.StatusUnauthorized || reached {
			t.Errorf("token %q: status %d, reached %v", token, status, reached)
		}
	}
}

// The refusal never says which check failed: a caller who could tell
// "unknown issuer" from "wrong audience" would be reading the configuration
// one guess at a time (FR-84).
func TestOAuthRefusalsAreIndistinguishable(t *testing.T) {
	p := newProvider(t)
	guard, err := New(p.oauthConfig(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bodies := map[string]bool{}
	for _, spoil := range []func(c *claims){
		func(c *claims) { c.issuer = "https://attacker.example.com" },
		func(c *claims) { c.audience = "https://other.example.com/" },
		func(c *claims) { c.expires = time.Now().Add(-time.Hour) },
		func(c *claims) { c.signWith = p.other },
	} {
		cl := p.defaults()
		spoil(cl)
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+p.mint(cl))
		rec := httptest.NewRecorder()
		guard.Middleware(spy(new(bool))).ServeHTTP(rec, req)
		bodies[strings.TrimSpace(rec.Body.String())] = true
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	}
	if len(bodies) != 1 {
		t.Errorf("four different failures produced %d distinguishable refusals: %v", len(bodies), keysOf(bodies))
	}
}

// An algorithm outside the operator's allowlist is refused even when the
// signature is perfectly good: the allowlist is configuration, never
// something the token gets a say in.
func TestOAuthEnforcesTheAlgorithmAllowlist(t *testing.T) {
	p := newProvider(t)
	inbound := p.oauthConfig()
	inbound.OAuth.Algorithms = []string{"ES256"} // the provider signs RS256
	guard, err := New(inbound, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if status, reached := call(t, guard, p.mint(p.defaults())); status != http.StatusUnauthorized || reached {
		t.Errorf("an algorithm outside the allowlist was accepted: status %d", status)
	}
}

// FR-84: a token missing a required scope is refused with 403, and the
// challenge names the scope so a client can ask for the right thing.
func TestOAuthRequiresScopes(t *testing.T) {
	p := newProvider(t)
	inbound := p.oauthConfig()
	inbound.OAuth.RequiredScopes = []string{"mcp:call"}
	guard, err := New(inbound, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	withScope := p.defaults()
	withScope.scope = "openid mcp:call"
	if status, reached := call(t, guard, p.mint(withScope)); status != http.StatusOK || !reached {
		t.Fatalf("a token carrying the required scope was refused: status %d", status)
	}

	without := p.defaults()
	without.scope = "openid"
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+p.mint(without))
	rec := httptest.NewRecorder()
	var reached bool
	guard.Middleware(spy(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if reached {
		t.Error("a token without the required scope reached the handler")
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "mcp:call") {
		t.Errorf("the challenge does not name the scope: %q", got)
	}
}

// `scp`, which several providers emit instead of `scope`.
func TestOAuthReadsScpAsWellAsScope(t *testing.T) {
	p := newProvider(t)
	keys := newKeySet(p.server.URL, time.Minute, nil)
	verify := oauthVerifier(&p.oauthConfig().OAuth, keys, discardLogger())

	cl := p.defaults()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": cl.issuer, "sub": cl.subject, "aud": cl.audience,
		"exp": cl.expires.Unix(), "scp": []any{"mcp:read", "mcp:call"},
	})
	token.Header["kid"] = p.kid
	signed, err := token.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}

	info, err := verify(t.Context(), signed, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if fmt.Sprint(info.Scopes) != "[mcp:read mcp:call]" {
		t.Errorf("scopes = %v", info.Scopes)
	}
	if info.UserID != "user-42" {
		t.Errorf("subject = %q", info.UserID)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
