package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// specWith returns a document publishing the named operations.
func specWith(operations ...string) string {
	var b strings.Builder
	b.WriteString("openapi: 3.0.3\ninfo: { title: Reload, version: \"1.0\" }\npaths:\n")
	for _, name := range operations {
		b.WriteString("  /" + name + ":\n    get:\n      operationId: " + name +
			"\n      responses: { \"200\": { description: ok } }\n")
	}
	return b.String()
}

// replaceSpec swaps the document atomically, the way a deployment would.
func replaceSpec(t *testing.T, path, content string) {
	t.Helper()
	temporary := path + ".next"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

// toolNames lists what the endpoint publishes right now. Each call is its own
// session, which is what the stateless profile means.
func toolNames(t *testing.T, endpoint string) []string {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "reload", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// waitForTool polls until the endpoint publishes name, or gives up.
func waitForTool(t *testing.T, endpoint, name string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, published := range toolNames(t, endpoint) {
			if published == name {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestWatchRepublishesAChangedDocument is FR-72's happy path: the document is
// replaced under a running server and clients see the new catalog without
// anyone restarting anything.
func TestWatchRepublishesAChangedDocument(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	replaceSpec(t, specPath, specWith("before"))

	endpoint, stderr := serveHTTPEnv(t, nil, "--listen", "127.0.0.1:0", specPath, "--watch", "--log-level", "debug")
	if got := toolNames(t, endpoint); len(got) == 0 {
		t.Fatalf("nothing was published at startup\nstderr:\n%s", stderr.String())
	}

	replaceSpec(t, specPath, specWith("before", "after"))
	if !waitForTool(t, endpoint, "after", 30*time.Second) {
		t.Fatalf("the new operation was never published\nstderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "catalog reloaded") {
		t.Errorf("the reload was not reported:\n%s", stderr.String())
	}
}

// TestABrokenDocumentDoesNotDisturbAServingCatalog is acceptance criterion 9,
// end to end: an operator breaks the document and the clients never find out.
func TestABrokenDocumentDoesNotDisturbAServingCatalog(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	replaceSpec(t, specPath, specWith("working"))

	endpoint, stderr := serveHTTPEnv(t, nil, "--listen", "127.0.0.1:0", specPath, "--watch", "--log-level", "debug")
	before := toolNames(t, endpoint)
	if len(before) == 0 {
		t.Fatalf("nothing was published at startup\nstderr:\n%s", stderr.String())
	}

	replaceSpec(t, specPath, "this is not an OpenAPI document: [unbalanced\n")

	// Give the watcher long enough to have seen it, settled and failed.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "reload refused") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "reload refused") {
		t.Fatalf("the broken document was never rejected, so this test proves nothing\nstderr:\n%s", stderr.String())
	}

	after := toolNames(t, endpoint)
	if len(after) != len(before) {
		t.Errorf("the working catalog changed: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("the working catalog changed: %v -> %v", before, after)
			break
		}
	}
}

// TestSIGHUPReloads: a signal costs nothing, has no surface, and is what a
// sidecar already sends.
func TestSIGHUPReloads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP is not delivered on Windows")
	}
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	replaceSpec(t, specPath, specWith("before"))

	endpoint, stderr, process := serveHTTPProcess(t, nil, "--listen", "127.0.0.1:0", specPath, "--log-level", "debug")
	if got := toolNames(t, endpoint); len(got) == 0 {
		t.Fatalf("nothing was published at startup\nstderr:\n%s", stderr.String())
	}

	// No --watch: without the signal nothing would ever reload.
	replaceSpec(t, specPath, specWith("before", "after"))
	if err := process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !waitForTool(t, endpoint, "after", 30*time.Second) {
		t.Fatalf("SIGHUP did not republish the catalog\nstderr:\n%s", stderr.String())
	}
}

// TestWatchIsRefusedOverStdio: reload replaces the server a transport looks
// up per request, and a stdio session resolves one for its whole life. A flag
// that would quietly do nothing is worse than one that refuses.
func TestWatchIsRefusedOverStdio(t *testing.T) {
	out, err := runBinary(t, "serve", miniSpecPath(t), "--lax", "--watch")
	if err == nil {
		t.Fatalf("--watch was accepted over stdio\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", code)
	}
}

// TestToolsListCarriesCacheHints is FR-74: a hint saying when to ask again,
// and a digest saying whether the answer changed.
func TestToolsListCarriesCacheHints(t *testing.T) {
	endpoint, _ := serveHTTP(t, miniSpecPath(t), "--lax")

	client := mcp.NewClient(&mcp.Implementation{Name: "cache", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if list.TTLMs <= 0 {
		t.Errorf("ttlMs = %d, want a positive hint", list.TTLMs)
	}
	if list.CacheScope != "public" {
		t.Errorf("cacheScope = %q, want public", list.CacheScope)
	}
	digest, _ := list.GetMeta()["lotsman/catalogDigest"].(string)
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("catalog digest in _meta = %q, want a sha256 digest", digest)
	}

	// The same catalog answers with the same digest: the identity is of what
	// is published, not of when it was asked for.
	again, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list again: %v", err)
	}
	if second, _ := again.GetMeta()["lotsman/catalogDigest"].(string); second != digest {
		t.Errorf("the digest changed without the catalog: %q then %q", digest, second)
	}
}

// An exploded specification is the shape large vendors publish: a small index
// over hundreds of documents. Every edit an operator makes is to one of those
// documents, and `--watch` used to poll the index alone -- so the feature
// appeared to work and noticed nothing they actually did.
//
// The reload itself was always correct: it re-reads the whole closure. What was
// missing was the trigger.
func TestWatchNoticesAChangeInAReferencedDocument(t *testing.T) {
	dir := t.TempDir()
	referenced := filepath.Join(dir, "pet.yaml")
	if err := os.WriteFile(referenced, []byte(`type: object
properties:
  name: { type: string }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(specPath, []byte(`openapi: 3.0.3
info: { title: Exploded, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: './pet.yaml' }
      responses: { "201": { description: created } }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	endpoint, stderr := serveHTTPEnv(t, nil, "--listen", "127.0.0.1:0", specPath,
		"--watch", "--log-level", "debug")
	if got := toolNames(t, endpoint); len(got) == 0 {
		t.Fatalf("nothing was published at startup\nstderr:\n%s", stderr.String())
	}

	// The index is untouched. Only the document it points at changes, and the
	// change is one a client can see: a second property in the tool's schema.
	if err := os.WriteFile(referenced, []byte(`type: object
properties:
  name: { type: string }
  colour: { type: string }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "catalog reloaded") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("a change to a referenced document never republished the catalog\nstderr:\n%s",
		stderr.String())
}

// The other half of NFR-8: calls in flight while the catalog is replaced. The
// unit test in internal/reload is where the race detector applies -- this
// subprocess is a separate, uninstrumented build -- so what this checks is the
// behaviour an operator sees: reloading under load drops nothing.
//
// A dropped call here would not be a hypothetical. The stateless profile's whole
// claim is that requests are independent of each other and of the server's
// bookkeeping, and a reload is the one moment when that bookkeeping changes.
func TestReloadingUnderConcurrentCallsDropsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP is not delivered on Windows")
	}
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	replaceSpec(t, specPath, specWith("before"))

	endpoint, stderr, process := serveHTTPProcess(t, nil, "--listen", "127.0.0.1:0", specPath,
		"--log-level", "debug")
	if got := toolNames(t, endpoint); len(got) == 0 {
		t.Fatalf("nothing was published at startup\nstderr:\n%s", stderr.String())
	}

	// Callers keep listing tools while the document is replaced and reloaded
	// under them. `tools/list` is the request a reload actually changes the
	// answer to, which makes it the one worth hammering.
	ctx, cancel := context.WithCancel(t.Context())
	var callers sync.WaitGroup
	var calls, failures atomic.Int64
	for range 6 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for ctx.Err() == nil {
				frame := sessionlessOrNil(t, endpoint)
				calls.Add(1)
				result, ok := frame["result"].(map[string]any)
				if !ok {
					failures.Add(1)
					continue
				}
				if tools, _ := result["tools"].([]any); len(tools) == 0 {
					failures.Add(1)
				}
			}
		}()
	}

	for generation := range 5 {
		replaceSpec(t, specPath, specWith("before", fmt.Sprintf("after%d", generation)))
		if err := process.Signal(syscall.SIGHUP); err != nil {
			t.Fatalf("signal: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	callers.Wait()

	if calls.Load() < 10 {
		t.Fatalf("only %d calls were made, so this test proves little", calls.Load())
	}
	if failures.Load() != 0 {
		t.Errorf("%d of %d calls failed while the catalog was being replaced\nstderr:\n%s",
			failures.Load(), calls.Load(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "catalog reloaded") {
		t.Errorf("nothing was actually reloaded, so the concurrency was against a still server:\n%s",
			stderr.String())
	}
}

// sessionlessOrNil is a tools/list request that reports failure by returning a
// frame without a result, rather than by failing the test from a goroutine.
//
// The response may arrive as an event stream, which is the shape the Streamable
// HTTP profile is allowed to answer in; a decoder that only understood plain
// JSON would report every call as a failure, which is exactly what the first
// version of this did.
func sessionlessOrNil(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":` + sessionlessMeta + `}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint,
		strings.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", "tools/list")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil
	}
	payload := string(raw)
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		payload = ""
		for _, line := range strings.Split(string(raw), "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				payload = strings.TrimSpace(after)
				break
			}
		}
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil
	}
	return frame
}
