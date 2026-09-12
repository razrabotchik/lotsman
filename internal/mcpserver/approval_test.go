package mcpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// elicitingClient answers every approval prompt with action, and records what
// it was shown.
func elicitingClient(action string, prompts *[]string, count *atomic.Int64) *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			if count != nil {
				count.Add(1)
			}
			if prompts != nil {
				*prompts = append(*prompts, req.Params.Message)
			}
			return &mcp.ElicitResult{Action: action}, nil
		},
	}
}

func mutationCatalog(t *testing.T, server string) catalog.Catalog {
	t.Helper()
	return catalog.Build("sha256:test", []domain.Operation{createPetOperation(server)},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
}

// Acceptance criterion 6 (FR-45, Constitution II): a required approval and a
// client that cannot be asked is a refusal before the network, not a call
// that quietly proceeds.
func TestApprovalFailsClosedWithoutClientCapability(t *testing.T) {
	transport := &countingTransport{}
	cat := mutationCatalog(t, "https://api.example.com")
	session := connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	}, nil) // a client that declared no elicitation capability

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err == nil && !res.IsError {
		t.Fatal("a mutation ran without the approval the policy requires")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
	got := errorText(t, res, err)
	if !strings.Contains(got, string(domain.ReasonApprovalUnavailable)) {
		t.Errorf("refusal = %q, want it to name %s", got, domain.ReasonApprovalUnavailable)
	}
	// The refusal has to say what would change it, or the operator's only
	// move is to turn the whole feature off.
	if !strings.Contains(got, "interactiveApproval") {
		t.Errorf("refusal = %q, want it to name the setting", got)
	}
}

func TestDeclinedApprovalRefusesBeforeTheNetwork(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			transport := &countingTransport{}
			cat := mutationCatalog(t, "https://api.example.com")
			session := connectWith(t, &mcpserver.Options{
				Catalog: &cat, BaseURL: "https://api.example.com",
				HTTPClient: &http.Client{Transport: transport},
			}, elicitingClient(action, nil, nil))

			res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      cat.Tools[0].Name,
				Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
			})
			if err == nil && !res.IsError {
				t.Fatal("a declined mutation was executed")
			}
			if transport.calls != 0 {
				t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
			}
			if got := errorText(t, res, err); !strings.Contains(got, string(domain.ReasonApprovalDeclined)) {
				t.Errorf("refusal = %q, want %s", got, domain.ReasonApprovalDeclined)
			}
		})
	}
}

func TestApprovedMutationExecutes(t *testing.T) {
	var body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		body = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	var prompts []string
	cat := mutationCatalog(t, upstream.URL)
	session := connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: upstream.URL, HTTPClient: upstream.Client(),
	}, elicitingClient("accept", &prompts, nil))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err != nil || res.IsError {
		t.Fatalf("an approved mutation failed: %v %q", err, errorText(t, res, err))
	}
	if body != `{"name":"Murka"}` {
		t.Errorf("upstream body = %q", body)
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want exactly one", len(prompts))
	}

	// FR-46: identity, effect, target and a redacted argument summary -- and
	// no argument values from the body, which is where the unbounded and the
	// sensitive live.
	prompt := prompts[0]
	for _, want := range []string{"ns:POST:/pets", "unknown", "POST", "/pets", "body: name"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q does not mention %q", prompt, want)
		}
	}
	if strings.Contains(prompt, "Murka") {
		t.Errorf("prompt %q shows a body value", prompt)
	}
}

// A read is not a decision anyone should be asked to make on every call: the
// policy that let it through is the one that classified it.
func TestReadsAreNeverSubmittedForApproval(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pets":[]}`))
	}))
	defer upstream.Close()

	op := domain.Operation{
		Key: "ns:GET:/pets", Method: "GET", PathTemplate: "/pets", Servers: []string{upstream.URL},
		Effect:  domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
	cat := catalog.Build("sha256:test", []domain.Operation{op}, catalog.Options{})

	var asked atomic.Int64
	session := connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: upstream.URL, HTTPClient: upstream.Client(),
	}, elicitingClient("decline", nil, &asked))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: cat.Tools[0].Name})
	if err != nil || res.IsError {
		t.Fatalf("a read was refused: %v %q", err, errorText(t, res, err))
	}
	if asked.Load() != 0 {
		t.Errorf("the client was asked %d times about a read", asked.Load())
	}
}

func TestApprovalModes(t *testing.T) {
	transport := &countingTransport{}
	cat := mutationCatalog(t, "https://api.example.com")

	// never: nothing is asked, and the mutation proceeds to the point where
	// only the network stops it (the transport refuses, having been called).
	session := connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport}, Approval: config.ApprovalNever,
	}, nil)
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	}); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if transport.calls != 1 {
		t.Errorf("RoundTrip calls = %d with approval=never, want the call to have been attempted", transport.calls)
	}

	// client-capability: the same client, which cannot be asked, proceeds --
	// this is the mode for an operator who decided that in writing.
	capable := &countingTransport{}
	session = connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: capable}, Approval: config.ApprovalClientCapability,
	}, nil)
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	}); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if capable.calls != 1 {
		t.Errorf("RoundTrip calls = %d with approval=client-capability and a client that cannot be asked, want 1", capable.calls)
	}
}

// Search mode is the same gate through the other front door.
func TestApprovalFailsClosedInSearchMode(t *testing.T) {
	transport := &countingTransport{}
	cat := searchCatalogWithPolicy(t, "https://api.example.com", policy.Config{AllowMutations: true})
	session := connectWith(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	}, nil)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_mutating_operation",
		Arguments: map[string]any{
			"id":        "ns:POST:/pets",
			"arguments": map[string]any{"body": map[string]any{"name": "Murka"}},
		},
	})
	if err == nil && !res.IsError {
		t.Fatal("call_mutating_operation ran without approval")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
	if got := errorText(t, res, err); !strings.Contains(got, string(domain.ReasonApprovalUnavailable)) {
		t.Errorf("refusal = %q, want %s", got, domain.ReasonApprovalUnavailable)
	}
}
