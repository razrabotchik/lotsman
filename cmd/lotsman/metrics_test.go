package main_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// scrape reads the metrics endpoint with an optional inbound token.
func scrape(t *testing.T, endpoint, token string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/metrics", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	read, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read scrape: %v", err)
	}
	return res.StatusCode, string(read)
}

// TestMetricsReflectWhatHappened is U13 over the wire: an operator watching
// the endpoint sees calls, refusals and the catalog they were served from.
func TestMetricsReflectWhatHappened(t *testing.T) {
	requests := make(chan struct{}, 4)
	api := newJSONAPI(t, requests)
	defer api.Close()

	endpoint, stderr := serveHTTP(t, miniSpecPath(t), "--lax", "--base-url", api.URL)

	client := mcp.NewClient(&mcp.Implementation{Name: "metrics", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	// One executed read...
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}},
	}); err != nil {
		t.Fatalf("get_pet: %v", err)
	}
	// ...and one refusal, since mutations are off by default.
	_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_pet",
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})

	status, body := scrape(t, endpoint, "")
	if status != http.StatusOK {
		t.Fatalf("scrape status = %d\n%s", status, body)
	}
	for _, want := range []string{
		`lotsman_tool_calls_total{decision="executed"} 1`,
		`lotsman_tool_calls_total{decision="refused"} 1`,
		`lotsman_upstream_responses_total{class="2xx"} 1`,
		"lotsman_catalog_tools ",
		"lotsman_build_info{",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not contain %q:\n%s", want, body)
		}
	}

	// And the log carries the matching records, so a metric and a log line
	// cannot disagree about what happened.
	if !strings.Contains(stderr.String(), "decision=executed") ||
		!strings.Contains(stderr.String(), "decision=refused") {
		t.Errorf("the audit log does not match the counters:\n%s", stderr.String())
	}
}

// TestMetricsAreBehindInboundAuth: which operations an agent has been calling
// is not public information, and an endpoint left open because nobody thought
// about it is the failure this transport exists to avoid.
func TestMetricsAreBehindInboundAuth(t *testing.T) {
	endpoint, _ := serveHTTPEnv(t,
		[]string{"LOTSMAN_INBOUND_CANARY=" + inboundCanary},
		"--listen", "127.0.0.1:0", miniSpecPath(t), "--lax", "--config", bearerConfig(t))

	if status, body := scrape(t, endpoint, ""); status != http.StatusUnauthorized {
		t.Errorf("an unauthenticated scrape returned %d:\n%s", status, body)
	}
	status, body := scrape(t, endpoint, inboundCanary)
	if status != http.StatusOK {
		t.Fatalf("an authenticated scrape returned %d:\n%s", status, body)
	}
	if !strings.Contains(body, "lotsman_build_info{") {
		t.Errorf("the scrape is empty:\n%s", body)
	}
}

// Metrics are the HTTP profile's (§7.4, a stdio process has nobody to scrape
// it) but the audit record is not: what was called is worth writing down
// wherever lotsman runs, and the log is the same channel in both cases.
func TestAuditRecordsAreWrittenOverStdioToo(t *testing.T) {
	requests := make(chan struct{}, 4)
	api := newJSONAPI(t, requests)
	defer api.Close()

	session, stderr := stdioSession(t, miniSpecPath(t), "--lax", "--base-url", api.URL)
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}},
	}); err != nil {
		t.Fatalf("get_pet: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "msg=audit") ||
		!strings.Contains(stderr.String(), "decision=executed") {
		t.Errorf("no audit record was written over stdio:\n%s", stderr.String())
	}
}
