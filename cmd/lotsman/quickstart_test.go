package main_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	yaml "go.yaml.in/yaml/v4"
)

const quickstartSpec = "../../examples/quickstart/openapi.yaml"

// The quickstart document is a promise that a first run works: every
// operation translates, so `inspect` says so and `serve` needs no --lax.
func TestTheQuickstartDocumentIsServedStrictly(t *testing.T) {
	stdout, stderr, code := runCLI(t, "inspect", quickstartSpec)
	if code != 0 {
		t.Fatalf("inspect exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0 rejected") {
		t.Errorf("the quickstart document has rejected operations:\n%s", stdout)
	}
	if !strings.Contains(stdout, "next: lotsman serve "+quickstartSpec+" --base-url ORIGIN") {
		t.Errorf("inspect does not end with the command to run next:\n%s", stdout)
	}
}

// It is a subset of the development API's own document, operation for
// operation: a quickstart that drifted from the API it points at would fail
// on the first call, which is the one call a newcomer makes.
func TestTheQuickstartIsASubsetOfTheDevelopmentAPI(t *testing.T) {
	type document struct {
		Servers []map[string]any          `yaml:"servers"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	read := func(path string) document {
		var doc document
		if err := yaml.Unmarshal([]byte(readRepoFile(t, path)), &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return doc
	}
	quick, full := read("examples/quickstart/openapi.yaml"), read("cmd/api-gateway/openapi.yaml")
	if !reflect.DeepEqual(quick.Servers, full.Servers) {
		t.Errorf("servers %v, the API's are %v", quick.Servers, full.Servers)
	}
	for path, item := range quick.Paths {
		for key, value := range item {
			want := full.Paths[path][key]
			if key == "parameters" {
				if !reflect.DeepEqual(value, want) {
					t.Errorf("%s: parameters differ from the API's", path)
				}
				continue
			}
			got, _ := value.(map[string]any)
			api, _ := want.(map[string]any)
			for _, field := range []string{"operationId", "parameters", "requestBody"} {
				if !reflect.DeepEqual(got[field], api[field]) {
					t.Errorf("%s %s: %s differs from the API's", key, path, field)
				}
			}
		}
	}
}

// The README's first run, end to end: the development API, the quickstart
// document, and a read through a real MCP session.
func TestTheQuickstartRunsAgainstTheDevelopmentAPI(t *testing.T) {
	origin := startDevelopmentAPI(t)
	session, stderr := stdioSession(t, quickstartSpec, "--base-url", origin)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "list_pets", Arguments: map[string]any{"query": map[string]any{"limit": 2}},
	})
	if err != nil || res.IsError {
		t.Fatalf("list_pets: %v %+v\nstderr:\n%s", err, res, stderr.String())
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if result.Status != http.StatusOK || !strings.Contains(result.Body, `"items"`) {
		t.Errorf("list_pets answered %d: %s", result.Status, result.Body)
	}
}

// startDevelopmentAPI builds and runs cmd/api-gateway on a free loopback
// port, plain HTTP only, and returns its origin once it answers.
func startDevelopmentAPI(t *testing.T) string {
	t.Helper()
	return startDevelopmentAPIWith(t, false).origin
}

// developmentAPI is a running fixture: its plain origin and, when asked for,
// the TLS one a token endpoint has to live on, with the certificate to trust.
type developmentAPI struct {
	origin, tlsOrigin, certificate string
}

func startDevelopmentAPIWith(t *testing.T, withTLS bool) developmentAPI {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skipping the subprocess e2e test")
	}
	bin := filepath.Join(t.TempDir(), "api-gateway")
	if out, err := exec.Command("go", "build", "-o", bin, "../api-gateway").CombinedOutput(); err != nil {
		t.Fatalf("go build api-gateway: %v\n%s", err, out)
	}
	freeAddr := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}

	certDir := t.TempDir()
	api := developmentAPI{origin: "http://" + freeAddr()}
	tlsAddr := ""
	if withTLS {
		tlsAddr = freeAddr()
		api.tlsOrigin = "https://" + tlsAddr
		api.certificate = filepath.Join(certDir, "api-gateway-ca.pem")
	}
	cmd := exec.Command(bin, "--addr", strings.TrimPrefix(api.origin, "http://"),
		"--tls-addr", tlsAddr, "--cert-dir", certDir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start api-gateway: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := http.Get(api.origin + "/health"); err == nil {
			_ = res.Body.Close()
			if !withTLS || fileExists(api.certificate) {
				return api
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("api-gateway did not answer at %s", api.origin)
	return developmentAPI{}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
