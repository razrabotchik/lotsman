package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/egress"
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
	// base is the egress-governed client: the dial guard and the redirect
	// refusal apply to the token endpoint exactly as they apply to an API.
	base *http.Client
	// egress is the policy the token endpoint's origin is checked against
	// before every mint. The client alone does not do this -- it guards
	// addresses, not origins -- and a credential provider is not a hole in
	// the floor (ADR-0017).
	egress *egress.Policy

	mu      sync.Mutex
	sources map[string]*source
}

// source is one profile's cached token. Its lock serialises the callers that
// find no valid token behind a single mint, so a hundred calls arriving at
// once cost one fetch rather than a hundred.
type source struct {
	mu       sync.Mutex
	token    *oauth2.Token
	settings *clientcredentials.Config
}

// NewMinter returns a minter over the given outbound client and the policy
// its token endpoints must satisfy. A nil policy allows no token endpoint.
func NewMinter(base *http.Client, outbound *egress.Policy) *Minter {
	if base == nil {
		base = http.DefaultClient
	}
	if outbound == nil {
		outbound = &egress.Policy{}
	}
	return &Minter{base: base, egress: outbound, sources: map[string]*source{}}
}

// Mints reports whether a profile obtains its own token.
func Mints(profile *config.Profile) bool {
	return profile.Scheme == config.SchemeOAuth2ClientCredentials
}

// token returns a valid access token for the credential, minting one if the
// cached token has expired or there is none.
//
// ctx is the call's: a caller that gives up stops waiting for a mint it
// started. The client's own budget bounds the mint either way (FR-32).
func (m *Minter) token(ctx context.Context, credential *Credential) (string, error) {
	cached := m.sourceFor(credential)
	cached.mu.Lock()
	defer cached.mu.Unlock()
	if cached.token.Valid() {
		return cached.token.AccessToken, nil
	}

	token, err := m.mint(ctx, cached.settings, &credential.Profile)
	if err != nil {
		// Not ClassUpstream: the API did not fail, and an operator reading
		// this should be sent to the right system. The profile and the origin
		// are named because an egress denial on a token endpoint is otherwise
		// something they have to work backwards from.
		return "", redact.Error(errs.Wrap(classOfMint(err), fmt.Errorf(
			"auth: profile %q could not obtain a token from %s: %w",
			credential.Name, credential.Profile.TokenURL, err)))
	}
	if token.AccessToken == "" {
		return "", errs.Errorf(errs.ClassAuth,
			"auth: profile %q received an empty access token from %s",
			credential.Name, credential.Profile.TokenURL)
	}
	// A minted token is a secret the moment it exists.
	redact.Add(token.AccessToken)
	cached.token = token
	return token.AccessToken, nil
}

// classOfMint keeps a policy refusal a policy refusal: an operator told
// "auth" would go looking at the secret, when the fix is the allowlist.
func classOfMint(err error) errs.Class {
	if errors.Is(err, egress.ErrDenied) {
		return errs.ClassPolicy
	}
	return errs.ClassAuth
}

// mint fetches one token. Everything that can refuse it locally does so
// before the network is touched: the origin, then the secret.
func (m *Minter) mint(ctx context.Context, settings *clientcredentials.Config, profile *config.Profile) (*oauth2.Token, error) {
	target, err := url.Parse(profile.TokenURL)
	if err != nil {
		return nil, errors.New("the token URL is not a URL")
	}
	if denied := m.egress.CheckTarget(target); denied != nil {
		return nil, denied
	}

	// The secret is read at every mint rather than held from the first one,
	// on the same terms as every other credential: a rotated secret takes
	// effect on the next mint. A secret that cannot be read, or reads empty,
	// stops here -- sending an empty client_secret would cost a round trip
	// and earn an authorization server's refusal in place of the real reason.
	secret, err := resolve(profile.ClientSecretRef)
	if err != nil {
		return nil, fmt.Errorf("client secret %s: %w", profile.ClientSecretRef, err)
	}
	// The settings are the source's, not built per mint, so that auto-detected
	// client authentication is probed once rather than at every expiry. They
	// are only ever touched under the source's lock.
	settings.ClientSecret = secret
	return settings.Token(context.WithValue(ctx, oauth2.HTTPClient, m.base))
}

// sourceFor returns the cached source for a profile, creating one on first
// use.
func (m *Minter) sourceFor(credential *Credential) *source {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.sources[credential.Name]; ok {
		return cached
	}
	cached := &source{settings: settingsFor(&credential.Profile)}
	m.sources[credential.Name] = cached
	return cached
}

// Invalidate drops the cached token for a profile, so the next call mints a
// new one (FR-35).
func (m *Minter) Invalidate(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sources, name)
}

// settingsFor is everything about a mint except the secret, which is read
// fresh each time.
func settingsFor(profile *config.Profile) *clientcredentials.Config {
	settings := &clientcredentials.Config{
		ClientID:  profile.ClientID,
		TokenURL:  profile.TokenURL,
		Scopes:    append([]string(nil), profile.Scopes...),
		AuthStyle: authStyle(profile.AuthStyle),
	}
	if profile.Audience != "" {
		// RFC 8707: the resource this token is for. Providers that key a
		// token to an audience refuse to issue one without it.
		settings.EndpointParams = url.Values{"resource": {profile.Audience}}
	}
	return settings
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
