package auth

import (
	"context"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// Minter obtains access tokens for the profiles that mint rather than present
// (FR-63).
//
// It is keyed by profile name and shared across every tool, because the token
// belongs to the credential and not to the operation: two hundred tools
// bound to one profile hold one token between them.
//
// Nothing is minted at startup. A process that cannot reach a token endpoint
// should still come up, still answer `inspect`, and refuse only the calls that
// actually need a token.
type Minter struct {
	// base is the egress-governed client. The token endpoint is an origin
	// lotsman calls, so the allowlist, the dial guard and the redirect
	// refusal apply to it exactly as they apply to an API: a credential
	// provider is not a hole in the egress floor.
	base *http.Client

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource
}

// NewMinter returns a minter over the given outbound client.
func NewMinter(base *http.Client) *Minter {
	if base == nil {
		base = http.DefaultClient
	}
	return &Minter{base: base, sources: map[string]oauth2.TokenSource{}}
}

// Mints reports whether a profile obtains its own token.
func Mints(profile *config.Profile) bool {
	return profile.Scheme == config.SchemeOAuth2ClientCredentials
}

// token returns a valid access token for the credential, minting one if the
// cached token has expired or there is none.
func (m *Minter) token(credential *Credential) (string, error) {
	source := m.sourceFor(credential)
	token, err := source.Token()
	if err != nil {
		// Not ClassUpstream: the API did not fail, and an operator reading
		// this should be sent to the right system. The profile and the origin
		// are named because an egress denial on a token endpoint is otherwise
		// something they have to work backwards from.
		return "", redact.Error(errs.Errorf(errs.ClassAuth,
			"auth: profile %q could not obtain a token from %s: %w",
			credential.Name, credential.Profile.TokenURL, err))
	}
	if token.AccessToken == "" {
		return "", errs.Errorf(errs.ClassAuth,
			"auth: profile %q received an empty access token from %s",
			credential.Name, credential.Profile.TokenURL)
	}
	// A minted token is a secret the moment it exists.
	redact.Add(token.AccessToken)
	return token.AccessToken, nil
}

// sourceFor returns the cached token source for a profile, building one on
// first use.
//
// The source itself does the caching and the locking: oauth2's reusing source
// serialises concurrent callers behind a single fetch, so a hundred calls
// arriving with no valid token cost one mint rather than a hundred.
func (m *Minter) sourceFor(credential *Credential) oauth2.TokenSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	if source, ok := m.sources[credential.Name]; ok {
		return source
	}
	source := m.build(&credential.Profile)
	m.sources[credential.Name] = source
	return source
}

// Invalidate drops the cached token for a profile, so the next call mints a
// new one (FR-35).
func (m *Minter) Invalidate(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sources, name)
}

// build constructs the token source for a profile.
func (m *Minter) build(profile *config.Profile) oauth2.TokenSource {
	settings := &clientcredentials.Config{
		ClientID:  profile.ClientID,
		TokenURL:  profile.TokenURL,
		Scopes:    append([]string(nil), profile.Scopes...),
		AuthStyle: authStyle(profile.AuthStyle),
		EndpointParams: func() map[string][]string {
			if profile.Audience == "" {
				return nil
			}
			// RFC 8707: the resource this token is for. Providers that key a
			// token to an audience refuse to issue one without it.
			return map[string][]string{"resource": {profile.Audience}}
		}(),
	}
	// The secret is read here rather than held from startup, on the same
	// terms as every other credential: a rotated secret takes effect on the
	// next mint. A resolution failure surfaces as a token error, which is
	// where the caller is already prepared to see one.
	settings.ClientSecret = resolveQuietly(profile.ClientSecretRef)

	// The token fetch runs on the egress-governed client. The context carries
	// it rather than a deadline: an oauth2 token source takes no per-call
	// context, so the bound on a mint is the client's own budget -- which is
	// the same budget every other outbound request is held to (FR-32).
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, m.base)
	return settings.TokenSource(ctx)
}

// resolveQuietly reads a secret reference, returning empty on failure.
//
// The error is deliberately dropped here and surfaced by the mint instead: a
// client secret that cannot be resolved produces an authorization server
// refusal naming the profile, which is the message an operator can act on.
// Returning it from `build` would mean a token source that exists in a failed
// state, which is a second way for the same thing to be wrong.
func resolveQuietly(ref config.SecretRef) string {
	value, err := resolve(ref)
	if err != nil {
		return ""
	}
	return value
}

// authStyle maps the configured style onto oauth2's.
//
// `auto` is the default and probes both, which is the right answer for an
// operator who does not know and a wasted round trip for one who does. The
// explicit values exist so that knowing can be written down.
func authStyle(style config.AuthStyle) oauth2.AuthStyle {
	switch style {
	case config.AuthStyleBasic:
		return oauth2.AuthStyleInHeader
	case config.AuthStyleBody:
		return oauth2.AuthStyleInParams
	default:
		return oauth2.AuthStyleAutoDetect
	}
}
