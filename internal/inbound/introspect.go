package inbound

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// maxIntrospectionBytes bounds the answer. It comes from the network like any
// other response, and a resource server is not obliged to read an unbounded
// one because the sender calls itself an authorization server.
const maxIntrospectionBytes = 64 << 10

// introspection is a client for RFC 7662, which is how a token that carries
// no claims of its own gets validated: the authorization server that minted
// it is asked whether it is still good.
//
// The trade against a signed token is explicit. This costs a network round
// trip on the hot path and makes the provider part of the endpoint's
// availability; in exchange a revoked token stops working immediately rather
// than when it expires.
type introspection struct {
	endpoint string
	clientID string
	secret   string
	client   *http.Client
	log      *slog.Logger

	// The claims a resource server must check for itself. RFC 7662 §2.2 says
	// the response *may* carry `iss` and `aud`; "may" is not a validation,
	// so anything present is checked and anything absent is refused.
	issuer   string
	audience string
}

// answer is the subset of an introspection response this reads.
type answer struct {
	Active   bool   `json:"active"`
	Scope    string `json:"scope"`
	Subject  string `json:"sub"`
	Expires  int64  `json:"exp"`
	Audience any    `json:"aud"`
	Issuer   string `json:"iss"`
}

// verifier returns the TokenVerifier that asks the authorization server.
//
// Like the JWT path, every refusal returns the bare sentinel: the SDK writes
// the error's message into the 401 body, and a caller who could tell "the
// provider timed out" from "your token is revoked" is reading the deployment
// one guess at a time (FR-84).
func (i *introspection) verifier() auth.TokenVerifier {
	return func(ctx context.Context, presented string, _ *http.Request) (*auth.TokenInfo, error) {
		result, err := i.ask(ctx, presented)
		if err != nil {
			i.log.Warn("inbound token refused", "error", err)
			return nil, auth.ErrInvalidToken
		}
		if !result.Active {
			i.log.Warn("inbound token refused", "error", "the authorization server reports it as inactive")
			return nil, auth.ErrInvalidToken
		}
		if result.Issuer != "" && result.Issuer != i.issuer {
			i.log.Warn("inbound token refused", "error", "issued by another authorization server")
			return nil, auth.ErrInvalidToken
		}
		if !audienceContains(result.Audience, i.audience) {
			i.log.Warn("inbound token refused", "error", "minted for another resource")
			return nil, auth.ErrInvalidToken
		}
		if result.Expires <= 0 {
			// Same rule as a JWT with no `exp`: a credential with no stated
			// end is one nothing can ever say has ended.
			i.log.Warn("inbound token refused", "error", "no expiration")
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			Scopes:     strings.Fields(result.Scope),
			Expiration: time.Unix(result.Expires, 0),
			UserID:     result.Subject,
		}, nil
	}
}

// ask performs the introspection call.
func (i *introspection) ask(ctx context.Context, token string) (*answer, error) {
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: introspection: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// Client authentication, not token forwarding: this credential is
	// lotsman's own and has nothing to do with the token being asked about.
	req.SetBasicAuth(i.clientID, i.secret)

	res, err := i.client.Do(req)
	if err != nil {
		// The URL can carry nothing secret, but the error may quote a
		// redirect target or a proxy that does.
		return nil, redact.Error(errs.Errorf(errs.ClassAuth, "inbound: introspection: %w", err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: introspection: answered %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxIntrospectionBytes+1))
	if err != nil {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: introspection: read: %w", err)
	}
	if len(body) > maxIntrospectionBytes {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: introspection: answer is larger than %d bytes",
			maxIntrospectionBytes)
	}

	var result answer
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: introspection: %w", err)
	}
	return &result, nil
}

// audienceContains reports whether the token was minted for this resource.
//
// An absent `aud` is not a match. RFC 7662 lets the authorization server omit
// it, and a resource server that read the omission as "for me" would accept
// every token that server ever issued, for any of its resources.
func audienceContains(claimed any, want string) bool {
	switch value := claimed.(type) {
	case string:
		return value == want
	case []any:
		for _, entry := range value {
			if text, ok := entry.(string); ok && text == want {
				return true
			}
		}
	}
	return false
}

// newIntrospection resolves the client credential and builds the verifier's
// dependencies.
func newIntrospection(oauth *config.InboundOAuth, log *slog.Logger, client *http.Client) (*introspection, error) {
	secret, err := oauth.Introspection.ClientSecretRef.Resolve()
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, errs.Errorf(errs.ClassAuth,
			"inbound: %s resolved to an empty introspection secret", oauth.Introspection.ClientSecretRef)
	}
	redact.Add(secret)

	timeout := config.DefaultIntrospectionTimeout
	if oauth.Introspection.Timeout != "" {
		parsed, err := time.ParseDuration(oauth.Introspection.Timeout)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage,
				"inbound: introspection timeout %q is not a duration", oauth.Introspection.Timeout)
		}
		timeout = parsed
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = timeout

	audience := oauth.Audience
	if audience == "" {
		audience = oauth.Resource
	}
	return &introspection{
		endpoint: oauth.Introspection.URL,
		clientID: oauth.Introspection.ClientID,
		secret:   secret,
		client:   &bounded,
		log:      log,
		issuer:   oauth.Issuer,
		audience: audience,
	}, nil
}
