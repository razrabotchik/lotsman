package inbound

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// clockSkew is the tolerance applied to a token's time claims.
//
// Small and fixed. A resource server and an authorization server that
// disagree by more than a minute have a clock problem, and widening this to
// cover it would extend the life of every revoked token by the same amount.
const clockSkew = time.Minute

// oauthVerifier validates a JWT the configured authorization server minted
// (FR-81).
//
// Every refusal returns auth.ErrInvalidToken and nothing else, because the
// SDK's middleware writes the error's message into the 401 body: a caller who
// could tell "unknown issuer" from "wrong audience" would be reading the
// configuration one guess at a time (FR-84). The reason goes to the log,
// where the operator is.
func oauthVerifier(oauth *config.InboundOAuth, keys *keySet, log *slog.Logger) auth.TokenVerifier {
	algorithms := oauth.Algorithms
	if len(algorithms) == 0 {
		algorithms = config.DefaultInboundAlgorithms
	}
	audience := oauth.Audience
	if audience == "" {
		// RFC 8707: the resource identifier is what a client asks for a token
		// for, so it is what the token is for.
		audience = oauth.Resource
	}

	// WithValidMethods is the allowlist, and it is the defence that matters:
	// without it a token can nominate its own algorithm, and `alg: none` or
	// an RSA public key presented as an HMAC secret both become signatures
	// that verify.
	parser := jwt.NewParser(
		jwt.WithValidMethods(algorithms),
		jwt.WithIssuer(oauth.Issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockSkew),
	)

	return func(ctx context.Context, presented string, _ *http.Request) (*auth.TokenInfo, error) {
		claims := jwt.MapClaims{}
		_, err := parser.ParseWithClaims(presented, claims, func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			return keys.key(ctx, kid)
		})
		if err != nil {
			log.Warn("inbound token refused", "error", err)
			return nil, auth.ErrInvalidToken
		}

		expiry, err := claims.GetExpirationTime()
		if err != nil || expiry == nil {
			// Belt and braces: WithExpirationRequired already refuses this.
			log.Warn("inbound token refused", "error", "no expiration")
			return nil, auth.ErrInvalidToken
		}
		subject, _ := claims.GetSubject()

		return &auth.TokenInfo{
			Scopes:     scopesFrom(claims),
			Expiration: expiry.Time,
			UserID:     subject,
		}, nil
	}
}

// scopesFrom reads the scopes a token carries.
//
// Two shapes, because two are in the wild: RFC 8693's space-delimited
// `scope` string, and the `scp` array several providers emit instead. An
// unrecognised shape yields no scopes, which refuses any call that required
// one — the safe direction.
func scopesFrom(claims jwt.MapClaims) []string {
	if scope, ok := claims["scope"].(string); ok {
		return strings.Fields(scope)
	}
	switch scp := claims["scp"].(type) {
	case string:
		return strings.Fields(scp)
	case []any:
		scopes := make([]string, 0, len(scp))
		for _, entry := range scp {
			if text, ok := entry.(string); ok {
				scopes = append(scopes, text)
			}
		}
		return scopes
	}
	return nil
}

// oauthGuard builds the resource-server guard.
func oauthGuard(oauth *config.InboundOAuth, log *slog.Logger, client *http.Client) (Guard, error) {
	verifier, err := oauthTokenVerifier(oauth, log, client)
	if err != nil {
		return Guard{}, err
	}
	verify := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		Scopes: oauth.RequiredScopes,
		// So that a 401 tells a client where to get a token instead of
		// leaving it to guess (RFC 9728 §5.1, FR-84).
		ResourceMetadataURL: metadataURL(oauth.Resource),
		ClockSkew:           clockSkew,
	})
	return Guard{
		Required:   true,
		Metadata:   metadataHandler(oauth),
		middleware: func(next http.Handler) http.Handler { return challenge(verify(next)) },
	}, nil
}

// oauthTokenVerifier picks how a token is checked: against the issuer's
// published keys, or by asking the issuer. The configuration has already
// refused to name both (config.validateTokenValidation), so this is a
// two-branch decision rather than a precedence rule.
func oauthTokenVerifier(oauth *config.InboundOAuth, log *slog.Logger, client *http.Client) (auth.TokenVerifier, error) {
	if oauth.Introspection.URL != "" {
		introspector, err := newIntrospection(oauth, log, client)
		if err != nil {
			return nil, err
		}
		return introspector.verifier(), nil
	}

	ttl := config.DefaultJWKSTTL
	if oauth.JWKSTTL != "" {
		parsed, err := time.ParseDuration(oauth.JWKSTTL)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "inbound: jwksTTL %q is not a duration", oauth.JWKSTTL)
		}
		ttl = parsed
	}
	return oauthVerifier(oauth, newKeySet(oauth.JWKSURI, ttl, client), log), nil
}
