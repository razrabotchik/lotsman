package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The committed stand-in for the browser smoke test (spec §7): the project
// has no Node toolchain, so the flow a person clicks through is driven over
// the same HTTP API the page uses.

const competingKey = "default:PATCH:/v1/pets/{petId}/competing"

// uiDemo copies the demo document next to a configuration that serves what it
// can (strict: false) and corrects nothing yet.
func uiDemo(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	spec, err := os.ReadFile(standSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(cfg, []byte(`# the demo configuration
apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
  strict: false # the demo API refuses some operations on purpose
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

var uiAddress = regexp.MustCompile(`open (http://127\.0\.0\.1:\d+/\?token=[0-9a-f]{64})`)

// startUI runs `lotsman ui` on a free port and returns the address it printed.
func startUI(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(buildBinary(t), append([]string{"ui", "--port", "0", "--no-open"}, args...)...)
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stopGracefully(t, cmd) })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if m := uiAddress.FindStringSubmatch(stdout.String()); m != nil {
			return m[1]
		}
		if strings.Contains(stderr.String(), "lotsman:") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lotsman ui never printed its address\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	return ""
}

// uiClient is a browser in miniature: a cookie jar, no automatic redirects,
// and the page's own Origin on every write.
type uiClient struct {
	t      *testing.T
	http   *http.Client
	origin string
}

func newUIClient(t *testing.T, address string) *uiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return &uiClient{t: t, origin: u.Scheme + "://" + u.Host, http: &http.Client{
		Jar: jar, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *uiClient) do(method, path, body string) (status int, text string) {
	c.t.Helper()
	target := path
	if strings.HasPrefix(path, "/") {
		target = c.origin + path
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, target, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, string(out)
}

type uiState struct {
	Draft struct {
		Operations []struct {
			Key         string   `json:"key"`
			OperationID string   `json:"operationId"`
			Status      string   `json:"status"`
			Support     string   `json:"support"`
			MediaTypes  []string `json:"mediaTypes"`
		} `json:"operations"`
	} `json:"draft"`
	Comparison struct {
		Summary struct {
			Fixed, Broken, Altered int
		} `json:"summary"`
		Widening []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"widening"`
	} `json:"comparison"`
	Entries []struct {
		ID string `json:"id"`
	} `json:"entries"`
}

func decodeState(t *testing.T, body string) uiState {
	t.Helper()
	var st uiState
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("decode state: %v\n%s", err, body)
	}
	return st
}

func TestUIFixAnOperationAndSave(t *testing.T) {
	t.Parallel()
	_, cfg := uiDemo(t)
	address := startUI(t, "--config", cfg)
	c := newUIClient(t, address)

	// Without the printed address there is nothing to see.
	if code, _ := c.do("GET", "/api/state", ""); code != http.StatusForbidden {
		t.Fatalf("state without the token: %d", code)
	}

	// 1. The printed address trades the token for a cookie.
	if code, _ := c.do("GET", address, ""); code != http.StatusSeeOther {
		t.Fatalf("GET printed address: %d", code)
	}
	if code, page := c.do("GET", "/", ""); code != http.StatusOK || !strings.Contains(page, "<html") {
		t.Fatalf("page: %d", code)
	}

	// 2. The amber operation is there, waiting for a decision.
	code, body := c.do("GET", "/api/state", "")
	if code != http.StatusOK {
		t.Fatalf("state: %d %s", code, body)
	}
	st := decodeState(t, body)
	found := false
	for _, op := range st.Draft.Operations {
		if op.OperationID == "patchPetCompeting" {
			found = true
			if op.Key != competingKey || op.Status != "decision" || len(op.MediaTypes) != 4 {
				t.Fatalf("patchPetCompeting = %+v", op)
			}
		}
	}
	if !found {
		t.Fatal("patchPetCompeting not in the state")
	}

	// A write without the page's Origin is refused.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, c.origin+"/api/reload", http.NoBody)
	if resp, err := c.http.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write without Origin: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	// 3. Pick the media type.
	code, body = c.do("POST", "/api/draft", `{"kind":"operation","operationKey":"`+competingKey+
		`","operation":{"match":{"operationId":"patchPetCompeting"},"mediaType":"application/merge-patch+json"}}`)
	if code != http.StatusOK {
		t.Fatalf("draft: %d %s", code, body)
	}
	st = decodeState(t, body)
	if st.Comparison.Summary.Fixed != 1 || st.Comparison.Summary.Broken != 0 || len(st.Comparison.Widening) != 0 || len(st.Entries) != 1 {
		t.Fatalf("comparison = %+v, entries = %+v", st.Comparison, st.Entries)
	}
	if code, body = c.do("GET", "/api/diff", ""); code != http.StatusOK || !strings.Contains(body, "mediaType: application/merge-patch+json") {
		t.Fatalf("diff: %d %s", code, body)
	}

	// 4. Save.
	if code, body = c.do("POST", "/api/save", `{"acknowledged":[]}`); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	saved, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mediaType: application/merge-patch+json", "# the demo configuration", "# the demo API refuses some operations on purpose"} {
		if !strings.Contains(string(saved), want) {
			t.Fatalf("saved file lacks %q:\n%s", want, saved)
		}
	}

	// 5. lotsman inspect agrees.
	stdout, stderr, exit := runCLI(t, "inspect", "--config", cfg, "--json")
	if exit != 0 {
		t.Fatalf("inspect: %d\n%s", exit, stderr)
	}
	report := decodeReport(t, stdout)
	found = false
	for _, op := range report.Operations {
		if op.Key == competingKey {
			found = true
			if op.Support != "supported" {
				t.Fatalf("after save, %s is %s", competingKey, op.Support)
			}
		}
	}
	if !found {
		t.Fatalf("inspect does not report %s", competingKey)
	}
}

// A widening change is refused at save until it is acknowledged, whatever
// the page does.
func TestUIWideningIsEnforcedByTheServer(t *testing.T) {
	t.Parallel()
	_, cfg := uiDemo(t)
	c := loggedIn(t, startUI(t, "--config", cfg))
	code, body := c.do("POST", "/api/draft", `{"kind":"operation","operationKey":"default:POST:/v1/charges",`+
		`"operation":{"match":{"operationId":"createCharge"},"effect":"read"}}`)
	if code != http.StatusOK {
		t.Fatalf("draft: %d %s", code, body)
	}
	st := decodeState(t, body)
	if len(st.Comparison.Widening) != 1 || st.Comparison.Widening[0].Kind != "effect-read" {
		t.Fatalf("widening = %+v", st.Comparison.Widening)
	}
	if code, body = c.do("POST", "/api/save", `{"acknowledged":[]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("unacknowledged save: %d %s", code, body)
	}
	if saved, _ := os.ReadFile(cfg); strings.Contains(string(saved), "effect: read") {
		t.Fatal("an unacknowledged widening reached the file")
	}
	if code, body = c.do("POST", "/api/save", `{"acknowledged":["`+st.Comparison.Widening[0].ID+`"]}`); code != http.StatusOK {
		t.Fatalf("acknowledged save: %d %s", code, body)
	}
	if saved, _ := os.ReadFile(cfg); !strings.Contains(string(saved), "effect: read") {
		t.Fatalf("saved:\n%s", saved)
	}
}

// loggedIn is a client that has opened the address the server printed.
func loggedIn(t *testing.T, address string) *uiClient {
	t.Helper()
	c := newUIClient(t, address)
	if code, _ := c.do("GET", address, ""); code != http.StatusSeeOther {
		t.Fatalf("GET printed address: %d", code)
	}
	return c
}

func TestUINeedsAnExistingConfigurationOrASpec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A regression that starts the server instead must fail, not hang.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildBinary(t), "ui", "--no-open", "--port", "0", "--config", filepath.Join(dir, "missing.yaml"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatal("lotsman ui started without a configuration or a spec")
	}
	if err == nil || !strings.Contains(stderr.String(), "does not exist") {
		t.Fatalf("err %v, stderr %q", err, stderr.String())
	}
}

func TestInspectDiffAgainst(t *testing.T) {
	t.Parallel()
	dir, old := uiDemo(t)
	newer := filepath.Join(dir, "new.yaml")
	if err := os.WriteFile(newer, []byte(`apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
  strict: false
operationOverrides:
  - match: { operationId: patchPetCompeting }
    mediaType: application/merge-patch+json
  - match: { operationId: listPets }
    parameters:
      - { name: limit, in: query, schema: { type: string } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "inspect", "--config", newer, "--diff-against", old)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{"changes   1 fixed, 0 broken, 1 altered", "fixed     " + competingKey,
		"altered   default:GET:/v1/pets", "parameter query/limit", "integer → string"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report lacks %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, code = runCLI(t, "inspect", "--config", newer, "--diff-against", old, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	var cmp struct {
		Summary struct{ Fixed, Broken, Altered int } `json:"summary"`
		Changes []struct {
			Key  string `json:"key"`
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout), &cmp); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if cmp.Summary.Fixed != 1 || cmp.Summary.Broken != 0 || cmp.Summary.Altered != 1 || len(cmp.Changes) != 2 {
		t.Fatalf("comparison = %+v", cmp)
	}

	if _, stderr, code = runCLI(t, "inspect", "--diff-against", old); code == 0 {
		t.Fatalf("--diff-against without --config succeeded: %s", stderr)
	}
	// Flags that would change one side of the comparison are refused, not
	// ignored.
	for _, extra := range [][]string{{"--allow-mutations"}, {"--mode", "tools"}, {"--fail-on-rejected"}} {
		args := append([]string{"inspect", "--config", newer, "--diff-against", old}, extra...)
		if _, stderr, code = runCLI(t, args...); code != 2 || !strings.Contains(stderr, "cannot be combined with "+extra[0]) {
			t.Errorf("%v: exit %d, stderr %q", extra, code, stderr)
		}
	}
}

// The workbench re-reads the document on every change, which standard input
// cannot give it.
func TestUIRefusesStandardInput(t *testing.T) {
	t.Parallel()
	_, stderr, code := runCLI(t, "ui", "--no-open", "-")
	if code != 2 || !strings.Contains(stderr, "standard input") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}
