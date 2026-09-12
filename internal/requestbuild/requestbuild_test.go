package requestbuild

import (
	"net/http"
	"strings"
	"testing"
)

func TestBuildUsesFirstServer(t *testing.T) {
	req, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{"https://api.example.com", "https://backup.example.com"}}, nil, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.com/widgets" {
		t.Errorf("URL = %q, want https://api.example.com/widgets", got)
	}
	if req.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", req.Method)
	}
}

func TestBuildBaseURLOverridesServers(t *testing.T) {
	req, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{"https://spec.example.com"}}, nil, Options{BaseURL: "https://override.example.com"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://override.example.com/widgets" {
		t.Errorf("URL = %q, want the override", got)
	}
}

func TestBuildJoinsTrailingSlashCleanly(t *testing.T) {
	req, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{"https://api.example.com/"}}, nil, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.com/widgets" {
		t.Errorf("URL = %q, want a single slash between host and path", got)
	}
}

func TestBuildRejectsUnresolvedPathParameter(t *testing.T) {
	_, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets/{id}", Servers: []string{"https://api.example.com"}}, nil, Options{})
	if err == nil {
		t.Fatal("want error for an unresolved path parameter")
	}
}

func TestBuildRejectsNoServerNoBaseURL(t *testing.T) {
	_, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets"}, nil, Options{})
	if err == nil {
		t.Fatal("want error when there is no server and no --base-url")
	}
}

func TestBuildRejectsNonAbsoluteBaseURL(t *testing.T) {
	_, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets"}, nil, Options{BaseURL: "/relative"})
	if err == nil {
		t.Fatal("want error for a non-absolute base URL")
	}
}

func TestBuildRejectsServerVariables(t *testing.T) {
	_, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{"https://{region}.example.com"}}, nil, Options{})
	if err == nil {
		t.Fatal("want error for an unresolved server variable")
	}
	if !strings.Contains(err.Error(), "variables") {
		t.Errorf("error = %v, want it to mention server variables", err)
	}
}

func TestBuildRejectsUnsafeBaseURLs(t *testing.T) {
	for _, baseURL := range []string{
		"ftp://api.example.com",
		"https://user:secret@api.example.com",
		"https://api.example.com/root#fragment",
		"https://api.example.com/root?token=secret",
	} {
		t.Run(baseURL, func(t *testing.T) {
			if _, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets"}, nil, Options{BaseURL: baseURL}); err == nil {
				t.Fatalf("Build accepted unsafe base URL %q", baseURL)
			}
		})
	}
}

func TestBuildURLValidationDoesNotEchoCredentials(t *testing.T) {
	const canary = "CANARY-SECRET-DO-NOT-LOG"
	_, err := Build(t.Context(), &Operation{Method: "GET", PathTemplate: "/widgets"}, nil, Options{BaseURL: "https://user:" + canary + "@api.example.com"})
	if err == nil {
		t.Fatal("Build accepted credentials in URL")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("validation error leaked credential: %v", err)
	}
}

// Pitfall #12: a relative server URL is legal and common, and means "wherever
// this document was served from". A document read off disk was served from
// nowhere, so lotsman refuses rather than inventing an origin -- and says so
// in terms an operator can act on.
func TestRelativeServerNeedsABaseURL(t *testing.T) {
	op := &Operation{Method: "GET", PathTemplate: "/widgets", Servers: []string{"/api/v2"}}

	_, err := Build(t.Context(), op, nil, Options{})
	if err == nil {
		t.Fatal("a relative server URL was accepted")
	}
	for _, want := range []string{"relative", "--base-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}

	// With an origin supplied, the same operation builds.
	req, err := Build(t.Context(), op, nil, Options{BaseURL: "https://api.example.com"})
	if err != nil {
		t.Fatalf("Build with --base-url: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.com/widgets" {
		t.Errorf("URL = %q", got)
	}
}
