package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

const canary = "CANARY-TOKEN-DO-NOT-LEAK"

// FR-59: a secret must be a reference. The refusal must not echo what was
// given, because what was given may be the secret.
func TestLiteralSecretsAreRefusedWithoutEchoing(t *testing.T) {
	_, err := ParseSecretRef(canary)
	if err == nil {
		t.Fatal("a literal secret was accepted as a reference")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("the refusal echoed the secret: %v", err)
	}
	if got := errs.ClassOf(err); got != errs.ClassUsage {
		t.Errorf("class = %q, want %q", got, errs.ClassUsage)
	}
	if !strings.Contains(err.Error(), "env:NAME") {
		t.Errorf("error = %q, want it to name the accepted forms", err)
	}
}

func TestSecretRefForms(t *testing.T) {
	for _, tt := range []struct {
		in      string
		wantErr bool
		mention string
	}{
		{in: "env:TOKEN"},
		{in: "file:/etc/lotsman/token"},
		{in: "  env:TOKEN  "}, // trimmed
		{in: "env:", wantErr: true},
		{in: "file:", wantErr: true},
		{in: "", wantErr: true},
		{in: "keyring:svc/acct", wantErr: true, mention: "not implemented yet"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			ref, err := ParseSecretRef(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSecretRef(%q) accepted it", tt.in)
				}
				if tt.mention != "" && !strings.Contains(err.Error(), tt.mention) {
					t.Errorf("error = %q, want it to mention %q", err, tt.mention)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSecretRef(%q): %v", tt.in, err)
			}
			if strings.TrimSpace(tt.in) != ref.String() {
				t.Errorf("ref = %q, want the trimmed input", ref)
			}
		})
	}
}

func TestSecretResolution(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_TOKEN", canary)

	value, err := SecretRef("env:LOTSMAN_TEST_TOKEN").Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value != canary {
		t.Errorf("value = %q", value)
	}

	// A trailing newline is what every editor adds; sending it as part of a
	// token is a support ticket waiting to happen.
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileValue, fileErr := SecretRef("file:" + path).Resolve()
	if fileErr != nil {
		t.Fatalf("Resolve: %v", fileErr)
	}
	value = fileValue
	if value != canary {
		t.Errorf("file value = %q, want the trimmed token", value)
	}
}

func TestSecretResolutionFailuresAreAuthErrors(t *testing.T) {
	for _, ref := range []SecretRef{
		"env:LOTSMAN_DEFINITELY_NOT_SET",
		"file:/nonexistent/lotsman/token",
	} {
		_, err := ref.Resolve()
		if err == nil {
			t.Fatalf("%s resolved", ref)
		}
		if got := errs.ClassOf(err); got != errs.ClassAuth {
			t.Errorf("%s: class = %q, want %q", ref, got, errs.ClassAuth)
		}
	}
}

func TestParseValidatesProfiles(t *testing.T) {
	for _, tt := range []struct {
		name    string
		doc     string
		mention string
	}{
		{
			name:    "missing apiVersion",
			doc:     "authProfiles: {}\n",
			mention: "apiVersion is required",
		},
		{
			name:    "unknown apiVersion",
			doc:     "apiVersion: lotsman.dev/v2\n",
			mention: "not understood",
		},
		{
			name:    "unknown field",
			doc:     "apiVersion: " + APIVersion + "\nexecution:\n  allowMutation: true\n",
			mention: "allowMutation",
		},
		{
			name: "literal secret in a profile",
			doc: "apiVersion: " + APIVersion + "\nauthProfiles:\n  api:\n    scheme: bearer\n    tokenRef: " +
				canary + "\n",
			mention: "must be a reference",
		},
		{
			name:    "apikey without a location",
			doc:     "apiVersion: " + APIVersion + "\nauthProfiles:\n  api:\n    scheme: apikey\n    name: X-Key\n    tokenRef: env:K\n",
			mention: "header, query or cookie",
		},
		{
			name:    "basic without a password",
			doc:     "apiVersion: " + APIVersion + "\nauthProfiles:\n  api:\n    scheme: basic\n    usernameRef: env:U\n",
			mention: "usernameRef and a passwordRef",
		},
		{
			name:    "unknown scheme",
			doc:     "apiVersion: " + APIVersion + "\nauthProfiles:\n  api:\n    scheme: oauth2\n",
			mention: "not one of apikey, basic, bearer",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatal("the document was accepted")
			}
			if !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("error = %q, want it to mention %q", err, tt.mention)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("the error echoed a literal secret: %v", err)
			}
		})
	}
}

func TestParseAcceptsAWellFormedDocument(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: ` + APIVersion + `
kind: Runtime
execution:
  allowMutations: true
  baseURL: https://api.example.com
authProfiles:
  api-key:
    scheme: apikey
    in: header
    name: X-API-Key
    tokenRef: env:EXAMPLE_KEY
    satisfies: [apiKeyAuth]
  token:
    scheme: bearer
    tokenRef: file:/etc/lotsman/token
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !file.Execution.AllowMutations || file.Execution.BaseURL != "https://api.example.com" {
		t.Errorf("execution = %+v", file.Execution)
	}
	if len(file.AuthProfiles) != 2 {
		t.Fatalf("profiles = %+v", file.AuthProfiles)
	}
	if got := file.AuthProfiles["api-key"]; got.In != InHeader || got.Name != "X-API-Key" {
		t.Errorf("api-key profile = %+v", got)
	}
}

// FR-62: defaults < file < environment < flags, and a flag that was not given
// must not override the file with a zero value.
func TestPrecedence(t *testing.T) {
	file := &File{
		APIVersion: APIVersion,
		Execution:  Execution{AllowMutations: true, BaseURL: "https://file.example.com"},
	}

	if got := Resolve(nil, Environment{}, Overrides{}); got.AllowMutations || got.BaseURL != "" {
		t.Errorf("defaults = %+v, want read-only with no base URL", got)
	}
	if got := Resolve(file, Environment{}, Overrides{}); !got.AllowMutations || got.BaseURL != "https://file.example.com" {
		t.Errorf("file layer = %+v", got)
	}
	if got := Resolve(file, Environment{BaseURL: "https://env.example.com"}, Overrides{}); got.BaseURL != "https://env.example.com" {
		t.Errorf("env layer = %+v", got)
	}

	flagURL := "https://flag.example.com"
	off := false
	got := Resolve(file, Environment{BaseURL: "https://env.example.com"}, Overrides{AllowMutations: &off, BaseURL: &flagURL})
	if got.AllowMutations {
		t.Error("an explicit --read-only must win over a file that enabled mutations")
	}
	if got.BaseURL != flagURL {
		t.Errorf("baseURL = %q, want the flag to win", got.BaseURL)
	}
}
