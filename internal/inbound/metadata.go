package inbound

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/razrabotchik/lotsman/internal/config"
)

// MetadataPath is where a client looks for Protected Resource Metadata
// (RFC 9728 §3).
//
// It is the one route that lives outside the guard, and it is the only one
// that ever will: a client needs this document *before* it has a token, so
// requiring a token to read it would be a loop with no way in. Everything
// else on the bind — the MCP endpoint, `/metrics` — stays behind
// authentication.
const MetadataPath = "/.well-known/oauth-protected-resource"

// metadataHandler serves the document describing this resource (FR-80).
//
// What it says is deliberately thin: who this resource is, which
// authorization servers can issue tokens for it, and which scopes it
// understands. It is public by design, so it must contain nothing an
// operator would mind a stranger reading — no origins, no operations, no
// policy.
func metadataHandler(oauth *config.InboundOAuth) http.Handler {
	servers := oauth.AuthorizationServers
	if len(servers) == 0 {
		servers = []string{oauth.Issuer}
	}
	scopes := oauth.ScopesSupported
	if len(scopes) == 0 {
		scopes = oauth.RequiredScopes
	}
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:             oauth.Resource,
		AuthorizationServers: servers,
		ScopesSupported:      scopes,
		// Header only. A token in a query string ends up in access logs and
		// in `Referer`; a token in a form body needs a body, and an MCP
		// request's body belongs to the protocol.
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "lotsman",
	})
}

// metadataURL is what a 401 points a client at (RFC 9728 §5.1).
//
// Derived from the resource identifier rather than configured separately:
// two fields that have to agree are two fields that will not.
func metadataURL(resource string) string {
	parsed, err := url.Parse(resource)
	if err != nil || !parsed.IsAbs() {
		return ""
	}
	// RFC 9728 §3.1: a resource with a path gets that path appended to the
	// well-known location, so two resources on one host stay distinct.
	suffix := strings.TrimSuffix(parsed.Path, "/")
	return parsed.Scheme + "://" + parsed.Host + MetadataPath + suffix
}
