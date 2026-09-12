package mcpserver_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// FR-41 in the runtime, not just in the gate: a mutation a deny rule names is
// refused with the same zero-RoundTrip guarantee as one the effect policy
// blocks. The rule is what changed, not how much is trusted.
func TestDeniedByRuleNeverReachesTheNetworkInToolsMode(t *testing.T) {
	transport := &countingTransport{}
	op := createPetOperation("https://api.example.com")
	op.Tags = []string{"pets"}

	cat := catalog.Build("sha256:test", []domain.Operation{op}, catalog.Options{
		Policy: policy.Config{AllowMutations: true, Deny: []policy.Rule{{Tag: "pets"}}},
	})
	session := connect(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err == nil && !res.IsError {
		t.Fatal("an operation a deny rule names was executed")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
	// The refusal has to name the rule: an operator who cannot tell which
	// line of their configuration stopped a call will disable all of them.
	if got := errorText(t, res, err); !strings.Contains(got, "denyRules") {
		t.Errorf("refusal = %q, want it to name the rule that decided", got)
	}
}

// The same property through the search-mode front door. Two doors, one gate:
// a rule that only held in tools mode would be a second security boundary.
func TestDeniedByRuleNeverReachesTheNetworkInSearchMode(t *testing.T) {
	transport := &countingTransport{}
	cat := searchCatalogWithPolicy(t, "https://api.example.com", policy.Config{
		AllowMutations: true,
		Deny:           []policy.Rule{{OperationKey: "ns:DELETE:/pets/{petId}"}},
	})
	session := searchSession(t, &cat, &http.Client{Transport: transport}, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "call_mutating_operation",
		Arguments: map[string]any{"id": "ns:DELETE:/pets/{petId}", "arguments": map[string]any{"path": map[string]any{"petId": "7"}}},
	})
	if err == nil && !res.IsError {
		t.Fatal("call_mutating_operation ran an operation a deny rule names")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}

	// The operation is still discoverable, and describing it is still free:
	// a deny rule says "you may not call this", not "this does not exist".
	found, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "describe_operation",
		Arguments: map[string]any{"id": "ns:DELETE:/pets/{petId}"},
	})
	if err != nil || found.IsError {
		t.Errorf("a denied operation vanished from discovery: %v %+v", err, found)
	}
}

// A non-empty allow list refuses the mutation it does not list, while leaving
// reads alone: FR-41 gates mutations, and a read needs no rule to license it.
func TestAllowListRefusesTheUnlistedMutationEndToEnd(t *testing.T) {
	transport := &countingTransport{}
	cat := searchCatalogWithPolicy(t, "https://api.example.com", policy.Config{
		AllowMutations: true,
		Allow:          []policy.Rule{{Effect: domain.EffectUnknown}},
	})
	session := searchSession(t, &cat, &http.Client{Transport: transport}, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "call_mutating_operation",
		Arguments: map[string]any{"id": "ns:DELETE:/pets/{petId}", "arguments": map[string]any{"path": map[string]any{"petId": "7"}}},
	})
	if err == nil && !res.IsError {
		t.Fatal("an unlisted mutation ran under a non-empty allow list")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
	if got := errorText(t, res, err); !strings.Contains(got, "allowRules") {
		t.Errorf("refusal = %q, want it to name the allow list", got)
	}
}

// errorText is whatever the server said, whether it arrived as a protocol
// error or as an error result.
func errorText(t *testing.T, res *mcp.CallToolResult, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	var text strings.Builder
	for _, content := range res.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}
