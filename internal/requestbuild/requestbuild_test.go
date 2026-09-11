package requestbuild

import (
	"strings"
	"testing"
)

func TestBuildUsesFirstServer(t *testing.T) {
	req, err := Build(t.Context(), "GET", "/widgets", []string{"https://api.example.com", "https://backup.example.com"}, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.com/widgets" {
		t.Errorf("URL = %q, want https://api.example.com/widgets", got)
	}
	if req.Method != "GET" {
		t.Errorf("Method = %q, want GET", req.Method)
	}
}

func TestBuildBaseURLOverridesServers(t *testing.T) {
	req, err := Build(t.Context(), "GET", "/widgets", []string{"https://spec.example.com"}, Options{BaseURL: "https://override.example.com"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://override.example.com/widgets" {
		t.Errorf("URL = %q, want the override", got)
	}
}

func TestBuildJoinsTrailingSlashCleanly(t *testing.T) {
	req, err := Build(t.Context(), "GET", "/widgets", []string{"https://api.example.com/"}, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.com/widgets" {
		t.Errorf("URL = %q, want a single slash between host and path", got)
	}
}

func TestBuildRejectsUnresolvedPathParameter(t *testing.T) {
	_, err := Build(t.Context(), "GET", "/widgets/{id}", []string{"https://api.example.com"}, Options{})
	if err == nil {
		t.Fatal("want error for an unresolved path parameter")
	}
}

func TestBuildRejectsNoServerNoBaseURL(t *testing.T) {
	_, err := Build(t.Context(), "GET", "/widgets", nil, Options{})
	if err == nil {
		t.Fatal("want error when there is no server and no --base-url")
	}
}

func TestBuildRejectsNonAbsoluteBaseURL(t *testing.T) {
	_, err := Build(t.Context(), "GET", "/widgets", nil, Options{BaseURL: "/relative"})
	if err == nil {
		t.Fatal("want error for a non-absolute base URL")
	}
}

func TestBuildRejectsServerVariables(t *testing.T) {
	_, err := Build(t.Context(), "GET", "/widgets", []string{"https://{region}.example.com"}, Options{})
	if err == nil {
		t.Fatal("want error for an unresolved server variable")
	}
	if !strings.Contains(err.Error(), "variables") {
		t.Errorf("error = %v, want it to mention server variables", err)
	}
}
