package auth

import (
	"encoding/base64"
	"net/http"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// Client returns a client that presents the binding's credentials.
//
// The credential is applied by the *innermost* round tripper, the one that
// hands the request to the transport: every layer above -- retries, logging,
// egress checks -- has already seen and recorded the request without it. That
// ordering is the reason a token cannot appear in a trace by accident.
func Client(base *http.Client, binding Binding) *http.Client {
	if binding.Empty() {
		return base
	}
	clone := *base
	inner := clone.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	clone.Transport = &transport{inner: inner, binding: binding}
	return &clone
}

// transport applies credentials immediately before the wire.
type transport struct {
	inner   http.RoundTripper
	binding Binding
}

// RoundTrip resolves each credential and applies it to a copy of the request.
//
// Secrets are resolved here, per call, rather than held from startup: a token
// rotated in the environment or on disk takes effect on the next call, and a
// process that is not making requests is not holding credentials in a
// long-lived structure.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The RoundTripper contract forbids modifying the request it was given.
	outgoing := req.Clone(req.Context())

	for i := range t.binding.Credentials {
		if err := apply(outgoing, &t.binding.Credentials[i]); err != nil {
			// Redacted defensively: a resolution error should never carry the
			// value, and this is the last place to be sure of it.
			return nil, redact.Error(err)
		}
	}
	return t.inner.RoundTrip(outgoing)
}

func apply(req *http.Request, credential *Credential) error {
	switch credential.Profile.Scheme {
	case config.SchemeBearer:
		token, err := resolve(credential.Profile.TokenRef)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil

	case config.SchemeBasic:
		username, err := resolve(credential.Profile.UsernameRef)
		if err != nil {
			return err
		}
		password, err := resolve(credential.Profile.PasswordRef)
		if err != nil {
			return err
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		// The encoded form is a secret too: redaction has to know it, or a
		// leaked Authorization header would sail straight through.
		redact.Add(encoded)
		req.Header.Set("Authorization", "Basic "+encoded)
		return nil

	case config.SchemeAPIKey:
		key, err := resolve(credential.Profile.TokenRef)
		if err != nil {
			return err
		}
		return applyAPIKey(req, credential, key)

	default:
		return errs.Errorf(errs.ClassAuth, "auth: profile %q has no provider", credential.Name)
	}
}

// applyAPIKey places the key where the *document* says the API reads it. The
// profile may not override that: sending a credential where the API does not
// look is how a key ends up in a log on someone else's side.
func applyAPIKey(req *http.Request, credential *Credential, key string) error {
	name := credential.Requirement.Name
	if name == "" {
		name = credential.Profile.Name
	}
	if name == "" {
		return errs.Errorf(errs.ClassAuth, "auth: profile %q does not say what the API key is called", credential.Name)
	}

	switch location(credential) {
	case config.InHeader:
		req.Header.Set(name, key)
	case config.InQuery:
		query := req.URL.Query()
		query.Set(name, key)
		req.URL.RawQuery = query.Encode()
	case config.InCookie:
		req.AddCookie(&http.Cookie{Name: name, Value: key})
	default:
		return errs.Errorf(errs.ClassAuth, "auth: profile %q does not say where the API key goes", credential.Name)
	}
	return nil
}

func location(credential *Credential) config.Location {
	if in := credential.Requirement.In; in != "" {
		return config.Location(in)
	}
	return credential.Profile.In
}

// resolve reads a secret and registers it for redaction, so that a value this
// process has handled can never be printed by it.
func resolve(ref config.SecretRef) (string, error) {
	value, err := ref.Resolve()
	if err != nil {
		return "", err
	}
	redact.Add(value)
	return value, nil
}
