package main_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
