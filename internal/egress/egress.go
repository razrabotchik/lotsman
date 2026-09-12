package egress

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrDenied identifies an outbound request refused before RoundTrip.
var ErrDenied = errors.New("policy_egress_denied")

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
