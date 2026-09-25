package egress

import (
	"net/url"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// The base URL and the allowed origins are the operator's grant of permission,
// and each refusal below describes a grant that would mean something other than
// what it looks like. None of them had a test: the guard was written once and
// every branch of it went unexercised, which coverage said plainly.
//
// The credential case is the one worth the most. `https://user:token@api` in a
// base URL reads as configuration and behaves as a leak: the credential ends up
// in the request line, in an access log, and in anything that reports "which
// origin did we call".
func TestAnAuthorizedOriginIsRefusedWhenItSaysMoreThanAnOrigin(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		says    string
	}{
		{
			name:    "credentials in the URL",
			baseURL: "https://operator:hunter2@api.example.com",
			says:    "credentials in URL are forbidden",
		},
		{
			name:    "a fragment",
			baseURL: "https://api.example.com/v1#section",
			says:    "fragments are forbidden",
		},
		{
			name:    "a query",
			baseURL: "https://api.example.com/v1?tenant=acme",
			says:    "queries in authorized base URLs are forbidden",
		},
		{
			name:    "a scheme that is not http",
			baseURL: "ftp://files.example.com",
			says:    "is not http or https",
		},
		{
			name:    "a file URL, which has no host at all",
			baseURL: "file:///etc/passwd",
			says:    "is not http or https",
		},
		{
			name:    "no host",
			baseURL: "https:///v1",
			says:    "host is empty",
		},
		{
			name:    "syntax nothing can parse",
			baseURL: "https://api.example.com/\x7f\x00",
			says:    "invalid base URL syntax",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PolicyFromBaseURL(tc.baseURL, DefaultBudget(), false)
			if err == nil {
				t.Fatal("the base URL was accepted")
			}
			if got := errs.ClassOf(err); got != errs.ClassUsage {
				t.Errorf("class = %s, want %s", got, errs.ClassUsage)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// The same guard applies to `execution.allowedOrigins`, where an origin is
// checked when the policy is used rather than when it is written -- so a grant
// that says too much is refused at the first call instead of authorizing it.
func TestAnAllowedOriginThatSaysTooMuchIsRefused(t *testing.T) {
	policy := Policy{
		AllowedOrigins: []string{"https://operator:hunter2@api.example.com"},
		Budget:         DefaultBudget(),
	}
	target, err := url.Parse("https://api.example.com/v1/things")
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.CheckTarget(target); err == nil {
		t.Error("an origin carrying a credential authorized a call")
	}
}

// And a target that is not a request lotsman could make is refused before any
// name is resolved: the check is about the URL, not about the network.
func TestATargetThatIsNotAnHTTPRequestIsRefused(t *testing.T) {
	policy := Policy{AllowedOrigins: []string{"https://api.example.com"}, Budget: DefaultBudget()}
	for _, raw := range []string{
		"ftp://api.example.com/things",
		"https://operator:hunter2@api.example.com/things",
		"https://api.example.com/things#fragment",
	} {
		target, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := policy.CheckTarget(target); err == nil {
			t.Errorf("%s was allowed", raw)
		}
	}
	if err := policy.CheckTarget(nil); err == nil {
		t.Error("a nil target was allowed")
	}
}
