package inbound

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// Guard is the inbound authorization decision, resolved once at startup.
//
// The zero value authenticates nothing and says so: `Required` is false, which
// is what the bind rule (FR-70) reads to decide whether a public interface is
// allowed. There is deliberately no way to build a Guard that claims to
// authenticate without a verifier behind it.
type Guard struct {
	// Required reports whether a caller must prove anything.
	Required bool
	// middleware wraps a handler when Required; nil otherwise.
	middleware func(http.Handler) http.Handler
}

// Middleware wraps next in whatever the mode requires. With no inbound
// authorization it returns next unchanged rather than an identity wrapper
// that looks like a check from the outside.
func (g Guard) Middleware(next http.Handler) http.Handler {
	if g.middleware == nil {
		return next
	}
	return g.middleware(next)
}

// New builds the guard the configuration describes.
//
// Resolving the secret is part of startup, not of the first request: an
// endpoint that will refuse every caller because a `tokenRef` points at an
// unset variable should say so while someone is still watching.
func New(inbound config.InboundAuth) (Guard, error) {
	switch inbound.Mode {
	case "", config.InboundNone:
		return Guard{}, nil
	case config.InboundStaticBearer:
		token, err := inbound.TokenRef.Resolve()
		if err != nil {
			return Guard{}, err
		}
		if token == "" {
			return Guard{}, errs.Errorf(errs.ClassAuth,
				"inbound: %s resolved to an empty token; an endpoint that accepts an empty "+
					"bearer would accept everyone", inbound.TokenRef)
		}
		// It is a credential like any other, even though it points the other
		// way: whatever any package logs, the bytes leaving the process pass
		// through this registry (FR-61).
		redact.Add(token)
		verify := auth.RequireBearerToken(staticBearer(token), bearerOptions())
		return Guard{
			Required: true,
			middleware: func(next http.Handler) http.Handler {
				return challenge(verify(next))
			},
		}, nil
	case config.InboundOAuth:
		return Guard{}, errs.Errorf(errs.ClassUnsupported,
			"inbound: oauth mode is not implemented in this build (feature 005)")
	default:
		return Guard{}, errs.Errorf(errs.ClassUsage,
			"inbound: mode %q is not none, static-bearer or oauth", inbound.Mode)
	}
}

// bearerOptions are the middleware's settings for a shared secret.
//
// AllowMissingExpiration is on because a static bearer has no expiry to carry:
// the SDK's default rejects a token that does not state one, which is right
// for an issued token and impossible for a configured one. The relaxation is
// about the shape of the credential, not about how long it is trusted -- a
// shared secret is valid until the operator changes it, and saying otherwise
// in a struct field would not make it less true.
func bearerOptions() *auth.RequireBearerTokenOptions {
	return &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
}

// staticBearer compares a presented token against the configured one.
//
// Both sides are hashed before the comparison. Comparing the raw values in
// constant time still leaks their length, and a length is a genuinely useful
// thing to learn about a secret you are trying to guess; comparing digests
// costs one hash and leaks nothing.
func staticBearer(expected string) auth.TokenVerifier {
	want := sha256.Sum256([]byte(expected))
	return func(_ context.Context, presented string, _ *http.Request) (*auth.TokenInfo, error) {
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			// The SDK turns this into a 401 whose body is this sentinel's
			// message. It says nothing about the configuration, which is the
			// point: a refusal that explains itself is a policy oracle for
			// anyone who can reach the port (FR-84).
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{}, nil
	}
}
