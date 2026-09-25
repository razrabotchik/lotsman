package egress

import (
	"net/url"
	"testing"
)

// NFR-6 names URL policy as a surface to fuzz, and it had no target. This is the
// decision that stands between a model's chosen operation and the network: an
// origin the operator authorized, and a target assembled from a document nobody
// trusts.
//
// The invariant is one sentence, and it is the whole policy: a target is allowed
// only if its origin is one of the authorized ones. Everything else -- a
// credential in the URL, a fragment, a scheme that is not http -- is refused, so
// no input may produce "allowed" for something whose origin was never granted.
func FuzzCheckTargetAllowsOnlyAuthorizedOrigins(f *testing.F) {
	f.Add("https://api.example.com", "https://api.example.com/things")
	f.Add("https://api.example.com", "https://api.example.com.evil.test/things")
	f.Add("https://api.example.com", "https://user:pass@api.example.com/things")
	f.Add("https://api.example.com", "https://api.example.com:443/things")
	f.Add("http://api.example.com:80", "http://api.example.com/things")
	f.Add("https://api.example.com", "HTTPS://API.EXAMPLE.COM/things")
	f.Add("https://api.example.com", "https://api.example.com/things#f")
	f.Add("https://api.example.com", "//api.example.com/things")
	f.Add("", "https://api.example.com/things")

	f.Fuzz(func(t *testing.T, allowed, target string) {
		parsed, err := url.Parse(target)
		if err != nil {
			return // not a URL lotsman would ever be handed
		}
		policy := Policy{AllowedOrigins: []string{allowed}, Budget: DefaultBudget()}

		if err := policy.CheckTarget(parsed); err != nil {
			return // a refusal needs no justification beyond being a refusal
		}

		// Allowed. Then the target must be an http(s) URL carrying nothing but
		// an origin and a path, and that origin must be the authorized one --
		// compared through the same normalization the policy used, since
		// "https://host:443" and "https://host" are the same origin and a
		// string comparison here would only be testing string comparison.
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			t.Fatalf("scheme %q was allowed", parsed.Scheme)
		}
		if parsed.User != nil {
			t.Fatalf("a URL carrying credentials was allowed: %q", target)
		}
		if parsed.Fragment != "" {
			t.Fatalf("a URL carrying a fragment was allowed: %q", target)
		}
		allowedParsed, parseErr := url.Parse(allowed)
		if parseErr != nil {
			t.Fatalf("an unparseable origin authorized %q", target)
		}
		if got, want := origin(parsed), origin(allowedParsed); got != want {
			t.Fatalf("origin %q was allowed by %q", got, want)
		}
	})
}
