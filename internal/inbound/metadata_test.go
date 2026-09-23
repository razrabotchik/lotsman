package inbound

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
)

// FR-80: the document says who this resource is and which authorization
// servers can issue tokens for it — and nothing an operator would mind a
// stranger reading.
func TestMetadataDocument(t *testing.T) {
	p := newProvider(t)
	inbound := p.oauthConfig()
	inbound.OAuth.RequiredScopes = []string{"mcp:call"}
	guard, err := New(inbound, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if guard.Metadata == nil {
		t.Fatal("an oauth guard serves no metadata document")
	}

	rec := httptest.NewRecorder()
	guard.Metadata.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetadataPath, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var document map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if document["resource"] != testResource {
		t.Errorf("resource = %v, want %s", document["resource"], testResource)
	}
	servers, _ := document["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != testIssuer {
		t.Errorf("authorization_servers = %v, want [%s]", servers, testIssuer)
	}
	scopes, _ := document["scopes_supported"].([]any)
	if len(scopes) != 1 || scopes[0] != "mcp:call" {
		t.Errorf("scopes_supported = %v", scopes)
	}
	// It is public, so it must carry nothing about the deployment beyond the
	// two facts a client needs.
	for _, forbidden := range []string{p.server.URL, "jwks"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("the public document mentions %q:\n%s", forbidden, rec.Body.String())
		}
	}
}

// RFC 9728 §5.1: a 401 points at the document, so a client that arrived with
// nothing learns where to go rather than being told out of band.
func TestTheChallengeNamesTheMetadata(t *testing.T) {
	p := newProvider(t)
	guard, err := New(p.oauthConfig(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
	rec := httptest.NewRecorder()
	guard.Middleware(spy(new(bool))).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	challenge := rec.Header().Get("WWW-Authenticate")
	want := `resource_metadata="https://mcp.example.com` + MetadataPath + `"`
	if !strings.Contains(challenge, want) {
		t.Errorf("WWW-Authenticate = %q, want it to contain %s", challenge, want)
	}
}

// The well-known location is derived from the resource identifier, so two
// resources on one host stay distinct (RFC 9728 §3.1).
func TestMetadataURLIsDerivedFromTheResource(t *testing.T) {
	cases := map[string]string{
		"https://mcp.example.com/":       "https://mcp.example.com" + MetadataPath,
		"https://mcp.example.com":        "https://mcp.example.com" + MetadataPath,
		"https://example.com/tenants/a":  "https://example.com" + MetadataPath + "/tenants/a",
		"https://example.com/tenants/a/": "https://example.com" + MetadataPath + "/tenants/a",
		"not a uri":                      "",
	}
	for resource, want := range cases {
		if got := metadataURL(resource); got != want {
			t.Errorf("metadataURL(%q) = %q, want %q", resource, got, want)
		}
	}
}

// Static-bearer has no metadata to point at, so it keeps the bare challenge
// 004 shipped: the scheme is the same, what it advertises is not.
func TestStaticBearerServesNoMetadata(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_INBOUND", canary)
	guard, err := New(&config.InboundAuth{
		Mode:     config.InboundStaticBearer,
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_INBOUND"),
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if guard.Metadata != nil {
		t.Error("static-bearer published a metadata document it cannot back")
	}
}
