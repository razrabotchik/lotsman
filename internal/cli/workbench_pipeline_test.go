package cli

import (
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/ui"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

const securedSpec = `openapi: 3.0.3
info: { title: T, version: "1" }
security: [ { bearerAuth: [] } ]
components:
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }
paths:
  /x:
    get: { operationId: getX, responses: { "200": { description: ok } } }
  /y:
    patch:
      operationId: patchY
      parameters:
        - { name: filter, in: query, style: deepObject, schema: { type: object } }
      requestBody:
        content:
          application/merge-patch+json: { schema: { type: object } }
          application/json-patch+json: { schema: { type: array } }
      responses: { "200": { description: ok } }
`

const securedConfig = `apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
  strict: false
  overlays: [./fix.overlay.yaml]
authProfiles:
  bearerAuth:
    scheme: bearer
    tokenRef: env:LOTSMAN_CANARY
operationOverrides:
  - match: { operationId: patchY }
    mediaType: application/merge-patch+json
    parameters:
      - { name: filter, in: query, style: form, schema: { type: string } }
`

func securedDir(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir, cfg = evaluateDir(t, securedSpec)
	if err := os.WriteFile(filepath.Join(dir, "fix.overlay.yaml"), []byte(fixOverlay), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(securedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

// Spec §7: the workbench's pipeline is serve's. The catalog it builds from a
// configuration (corrections, overlay, credentials) is the catalog serve
// loads from the same files, digest for digest.
func TestTheWorkbenchCatalogIsServesCatalog(t *testing.T) {
	t.Parallel()
	_, cfg := securedDir(t)
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap, fromWorkbench, _ := evaluate(t.Context(), data, pipelineInputs{configPath: cfg})
	if fromWorkbench == nil || snap.SpecError != "" || snap.ConfigError != "" || len(snap.Problems) != 0 {
		t.Fatalf("workbench snapshot = %+v", snap)
	}
	runtime, err := resolveConfig(cfg, flag.NewFlagSet("serve", flag.ContinueOnError), &flagValues{})
	if err != nil {
		t.Fatal(err)
	}
	fromServe, err := loadCatalog(t.Context(), runtime.Source, quiet, runtime, catalog.ModeTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromServe.Tools) != 2 || len(fromWorkbench.Tools) != 2 {
		t.Fatalf("tools: serve %d, workbench %d", len(fromServe.Tools), len(fromWorkbench.Tools))
	}
	if fromWorkbench.Digest != fromServe.Digest || fromWorkbench.SpecDigest != fromServe.SpecDigest {
		t.Fatalf("digests differ: workbench %s/%s, serve %s/%s",
			fromWorkbench.Digest, fromWorkbench.SpecDigest, fromServe.Digest, fromServe.SpecDigest)
	}
}

// Spec §7: with a real secret in the environment, no response of the
// workbench's API carries it — state, operation, diff, dry run, refusals.
func TestNoResponseCarriesTheSecret(t *testing.T) {
	const canary = "canary-7f3b9e1c-workbench-must-never-say-this"
	t.Setenv("LOTSMAN_CANARY", canary)
	_, cfg := securedDir(t)
	wb, err := workbench.New(t.Context(), cfg, "", newPipeline(pipelineInputs{configPath: cfg}))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ui.Listen(t.Context(), wb, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	address, err := url.Parse(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	host := address.Host
	origin := "http://" + host
	var cookies []*http.Cookie
	call := func(method, target, body string) (int, string) {
		t.Helper()
		var reader io.Reader = http.NoBody
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, reader)
		req.Host = host
		if method != http.MethodGet {
			req.Header.Set("Origin", origin)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		text := rec.Body.String()
		if strings.Contains(text, canary) {
			t.Fatalf("%s %s: the secret is in the response:\n%s", method, target, text)
		}
		for k, v := range rec.Header() {
			if strings.Contains(strings.Join(v, " "), canary) {
				t.Fatalf("%s %s: the secret is in header %s", method, target, k)
			}
		}
		if cs := rec.Result().Cookies(); len(cs) > 0 {
			cookies = cs
		}
		return rec.Code, text
	}
	if code, _ := call("GET", address.RequestURI(), ""); code != http.StatusOK {
		t.Fatalf("exchange: %d", code)
	}
	key := ""
	code, state := call("GET", "/api/state", "")
	if code != http.StatusOK {
		t.Fatalf("state: %d %s", code, state)
	}
	snap, _, _ := evaluate(t.Context(), []byte(securedConfig), pipelineInputs{configPath: cfg})
	for _, op := range snap.Operations {
		if op.OperationID == "getX" {
			key = op.Key
		}
	}
	steps := []struct {
		method, target, body string
		want                 int
	}{
		{"GET", "/api/operation?key=" + key, "", 200},
		{"POST", "/api/draft", `{"kind":"operation","operationKey":"` + key + `","operation":{"match":{"operationId":"getX"},"authProfile":"bearerAuth","note":"pin"}}`, 200},
		{"POST", "/api/draft", `{"kind":"operation","operation":{"match":{"operationId":"getX"},"authProfile":"nobody"}}`, 422},
		{"GET", "/api/diff", "", 200},
		{"POST", "/api/explain", `{"key":"` + key + `","arguments":{}}`, 200},
		{"POST", "/api/explain", `{"key":"default:GET:/nowhere","arguments":{}}`, 422},
		{"POST", "/api/save", `{"acknowledged":[]}`, 422},
		{"POST", "/api/save", `{"acknowledged":["` + key + `#auth-profile"]}`, 200},
		{"POST", "/api/reload", "", 200},
		{"GET", "/api/state", "", 200},
	}
	for _, s := range steps {
		if code, text := call(s.method, s.target, s.body); code != s.want {
			t.Errorf("%s %s: %d, want %d: %s", s.method, s.target, code, s.want, text)
		}
	}
	if out, _ := os.ReadFile(cfg); strings.Contains(string(out), canary) || !strings.Contains(string(out), "authProfile: bearerAuth") {
		t.Fatalf("saved:\n%s", out)
	}
}
