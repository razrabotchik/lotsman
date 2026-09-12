package egress

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrDenied identifies an outbound request refused before RoundTrip.
var ErrDenied = errors.New("policy_egress_denied")

// CheckTarget authorizes target against the operator-provided base URL. Until
// the full allowedOrigins/CIDR policy lands in T032, a spec-authored server is
// not sufficient authority to make a network call: --base-url is the explicit
// opt-in for the tracer-bullet runtime.
func CheckTarget(target *url.URL, authorizedBase string) error {
	if authorizedBase == "" {
		return fmt.Errorf("%w: network execution requires an explicit --base-url until allowedOrigins policy is implemented", ErrDenied)
	}
	allowed, err := url.Parse(authorizedBase)
	if err != nil {
		return fmt.Errorf("%w: invalid authorized base URL syntax", ErrDenied)
	}
	if err := validateHTTPURL(target, true); err != nil {
		return fmt.Errorf("%w: target: %w", ErrDenied, err)
	}
	if err := validateHTTPURL(allowed, false); err != nil {
		return fmt.Errorf("%w: base URL: %w", ErrDenied, err)
	}
	if origin(target) != origin(allowed) {
		return fmt.Errorf("%w: target origin %q is not the authorized origin %q", ErrDenied, origin(target), origin(allowed))
	}
	return nil
}

// Client clones base and installs the fail-closed redirect default. Cloning
// preserves injected transports/test seams without mutating a caller-owned
// client that may be used concurrently elsewhere.
func Client(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	clone := *base
	if clone.Transport == nil {
		// The assertion is checked rather than assumed: a caller that has
		// replaced http.DefaultTransport must not silently get a transport
		// that still inherits proxy settings from the environment.
		def, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			clone.Transport = &http.Transport{}
		} else {
			transport := def.Clone()
			transport.Proxy = nil
			clone.Transport = transport
		}
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func validateHTTPURL(u *url.URL, allowQuery bool) error {
	if u == nil {
		return errors.New("URL is nil")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("host is empty")
	}
	if u.User != nil {
		return errors.New("credentials in URL are forbidden")
	}
	if u.Fragment != "" {
		return errors.New("fragments are forbidden")
	}
	if !allowQuery && u.RawQuery != "" {
		return errors.New("queries in authorized base URLs are forbidden")
	}
	return nil
}

func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + host + ":" + port
}
