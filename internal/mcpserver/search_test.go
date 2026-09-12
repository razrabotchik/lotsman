package mcpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// searchCatalog builds a search-mode catalog: a read, a mutation, and a
// destructive operation, tagged so filters have something to bite on.
func searchCatalog(t *testing.T, server string, allowMutations bool) catalog.Catalog {
	t.Helper()
	return searchCatalogWithPolicy(t, server, policy.Config{AllowMutations: allowMutations})
}

// searchCatalogWithPolicy is the same catalog under an arbitrary execution
// policy, for the tests that are about the policy rather than the mode.
func searchCatalogWithPolicy(t *testing.T, server string, gate policy.Config) catalog.Catalog {
	t.Helper()
	return catalog.Build("sha256:test", searchOperations(server), catalog.Options{Mode: catalog.ModeSearch, Policy: gate})
}

// searchOperations is the fixture document behind both.
func searchOperations(server string) []domain.Operation {
	read := func(method, path, id string, effect domain.Effect, tags ...string) domain.Operation {
		return domain.Operation{
			Key:               domain.NewOperationKey("ns", method, path),
			SourceOperationID: id,
			Method:            method,
			PathTemplate:      path,
			Summary:           id,
			Tags:              tags,
			Servers:           []string{server},
			Effect: domain.EffectDecision{
				Effect: effect, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred,
			},
			Input:   domain.InputModel{},
			Support: domain.SupportStatus{Level: domain.SupportSupported},
		}
	}
	createPet := read("POST", "/pets", "createPet", domain.EffectUnknown, "pets")
	createPet.Input = domain.InputModel{Body: &domain.BodySpec{
		MediaType: "application/json", Required: true,
		Schema: domain.Schema{
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		},
	}}

	ops := []domain.Operation{
		read("GET", "/pets", "listPets", domain.EffectRead, "pets"),
		createPet,
		read("DELETE", "/pets/{petId}", "deletePet", domain.EffectDestructive, "pets"),
		read("GET", "/orders", "listOrders", domain.EffectRead, "billing"),
	}
	// The path parameter of deletePet is declared so the operation is
	// executable rather than blocked for an unrelated reason.
	ops[2].Input = domain.InputModel{Parameters: []domain.Parameter{{
		Name: "petId", In: domain.LocationPath, Required: true,
		Style: domain.StyleSimple, Schema: domain.Schema{"type": "string"},
	}}}

	return ops
}

func searchSession(t *testing.T, cat *catalog.Catalog, client *http.Client, baseURL string) *mcp.ClientSession {
	t.Helper()
	return connect(t, &mcpserver.Options{Catalog: cat, HTTPClient: client, BaseURL: baseURL})
}

func structured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// A large catalog is published as five tools, not hundreds -- which is the
// entire point of the mode (FR-47/48).
func TestSearchModePublishesFiveMetaTools(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool
	}
	for _, want := range []string{
		"ping", "search_operations", "list_tags", "describe_operation",
		"call_read_operation", "call_mutating_operation",
	} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s missing from tools/list: %v", want, names)
		}
	}
	// No per-operation tool is published: that is the size problem this mode
	// exists to avoid.
	for name := range names {
		if strings.HasPrefix(name, "list_pets") || strings.HasPrefix(name, "create_pet") {
			t.Errorf("search mode published a per-operation tool: %s", name)
		}
	}

	// FR-51: the mutating call tool is advertised conservatively.
	mutating := names["call_mutating_operation"]
	if mutating.Annotations == nil || mutating.Annotations.ReadOnlyHint {
		t.Error("call_mutating_operation must not carry readOnlyHint")
	}
	if mutating.Annotations.DestructiveHint == nil || !*mutating.Annotations.DestructiveHint {
		t.Error("call_mutating_operation must be advertised as destructive")
	}
}

// Advertising a tool whose every call is refused teaches a model nothing except
// to keep trying.
func TestMutatingToolIsAbsentWhenMutationsAreNotAllowed(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", false)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "call_mutating_operation" {
			t.Fatal("the mutating call tool was published with mutations disabled")
		}
	}
}

func TestSearchOperationsReturnsIdentityWithoutSchemas(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_operations",
		Arguments: map[string]any{"query": "list pets"},
	})
	if err != nil || res.IsError {
		t.Fatalf("search_operations: %v %+v", err, res)
	}
	out := structured[mcpserver.SearchOutput](t, res)
	if len(out.Results) == 0 {
		t.Fatal("no results")
	}
	if got := string(out.Results[0].Key); got != "ns:GET:/pets" {
		t.Errorf("top hit = %q", got)
	}
	// The results carry no schema: that is what describe_operation is for, and
	// what keeps this mode small.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "inputSchema") || strings.Contains(string(raw), "additionalProperties") {
		t.Errorf("search results carry schemas:\n%s", raw)
	}
}

// Principle I in the search path: an empty result is explained, not filled with
// the nearest thing.
func TestSearchOperationsExplainsAnEmptyResult(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_operations",
		Arguments: map[string]any{"query": "provision a kubernetes cluster"},
	})
	if err != nil || res.IsError {
		t.Fatalf("search_operations: %v", err)
	}
	out := structured[mcpserver.SearchOutput](t, res)
	if len(out.Results) != 0 {
		t.Fatalf("results = %+v, want none", out.Results)
	}
	if out.Note == "" || !strings.Contains(out.Note, "list_tags") {
		t.Errorf("note = %q, want it to suggest a way forward", out.Note)
	}
}

func TestListTagsAndFilters(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_tags"})
	if err != nil || res.IsError {
		t.Fatalf("list_tags: %v", err)
	}
	tags := structured[mcpserver.TagsOutput](t, res)
	if len(tags.Tags) != 2 || tags.Tags[0].Tag != "pets" || tags.Tags[0].Count != 3 {
		t.Errorf("tags = %+v", tags.Tags)
	}

	// A filter excludes: a billing search cannot return a pets operation.
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_operations",
		Arguments: map[string]any{"query": "list", "tags": []any{"billing"}},
	})
	if err != nil || res.IsError {
		t.Fatalf("filtered search: %v", err)
	}
	out := structured[mcpserver.SearchOutput](t, res)
	for _, result := range out.Results {
		if string(result.Key) != "ns:GET:/orders" {
			t.Errorf("%s leaked past the tag filter", result.Key)
		}
	}
}

func TestDescribeOperationCarriesTheSchema(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "describe_operation",
		Arguments: map[string]any{"id": "ns:POST:/pets"},
	})
	if err != nil || res.IsError {
		t.Fatalf("describe_operation: %v %+v", err, res)
	}
	out := structured[mcpserver.DescribeOutput](t, res)

	if out.Key != "ns:POST:/pets" || out.Method != http.MethodPost {
		t.Errorf("described %+v", out)
	}
	if out.Effect.Effect != domain.EffectUnknown {
		t.Errorf("effect = %q", out.Effect.Effect)
	}
	if out.InputSchema == nil || out.SchemaBytes == 0 {
		t.Errorf("no schema in %+v", out)
	}
	if !out.Callable {
		t.Errorf("callable = false, refusal = %q", out.Refusal)
	}
	// A tool name also resolves, because that is the other identifier a reader
	// has in hand.
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "describe_operation",
		Arguments: map[string]any{"id": out.ToolName},
	}); err != nil {
		t.Errorf("describe by tool name: %v", err)
	}
}

func TestDescribeUnknownOperation(t *testing.T) {
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, nil, "https://api.example.com")

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "describe_operation",
		Arguments: map[string]any{"id": "ns:GET:/nope"},
	})
	if err == nil && !res.IsError {
		t.Fatal("an unknown id was described")
	}
}

// FR-50: the read tool never calls another effect class, whatever the search
// result said. This is the property that keeps a search result from being a way
// around the mutation gate.
func TestCallReadOperationRefusesNonReads(t *testing.T) {
	transport := &countingTransport{}
	cat := searchCatalog(t, "https://api.example.com", true)
	session := searchSession(t, &cat, &http.Client{Transport: transport}, "https://api.example.com")

	for _, id := range []string{"ns:POST:/pets", "ns:DELETE:/pets/{petId}"} {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "call_read_operation",
			Arguments: map[string]any{"id": id},
		})
		if err == nil && !res.IsError {
			t.Errorf("call_read_operation called %s", id)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

func TestCallReadOperationExecutes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pets":[]}`))
	}))
	defer srv.Close()

	cat := searchCatalog(t, srv.URL, true)
	session := searchSession(t, &cat, srv.Client(), srv.URL)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "call_read_operation",
		Arguments: map[string]any{"id": "ns:GET:/pets"},
	})
	if err != nil || res.IsError {
		t.Fatalf("call_read_operation: %v %+v", err, res.Content)
	}
	if gotPath != "/pets" {
		t.Errorf("upstream path = %q", gotPath)
	}
}

// The mutating tool runs the same call path, and the same validation: enabling
// mutations does not enable invalid arguments.
func TestCallMutatingOperationExecutesAndValidates(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	cat := searchCatalog(t, srv.URL, true)
	session := searchSession(t, &cat, srv.Client(), srv.URL)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_mutating_operation",
		Arguments: map[string]any{
			"id":        "ns:POST:/pets",
			"arguments": map[string]any{"body": map[string]any{"name": "Murka"}},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("call_mutating_operation: %v %+v", err, res.Content)
	}
	if gotBody != `{"name":"Murka"}` {
		t.Errorf("upstream body = %s", gotBody)
	}

	// An undeclared field is still a validation error.
	bad, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_mutating_operation",
		Arguments: map[string]any{
			"id":        "ns:POST:/pets",
			"arguments": map[string]any{"body": map[string]any{"nickname": "Murka"}},
		},
	})
	if err == nil && !bad.IsError {
		t.Error("an invalid body was accepted through the mutating tool")
	}

	// And a read cannot be called through it: the two tools are separate so a
	// model holding one cannot do the other's job.
	wrong, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "call_mutating_operation",
		Arguments: map[string]any{"id": "ns:GET:/pets"},
	})
	if err == nil && !wrong.IsError {
		t.Error("a read was called through the mutating tool")
	}
}

// A policy-blocked operation is findable and describable, and refuses when
// called: discovery is not permission, in either mode.
func TestSearchModeRespectsThePolicyGate(t *testing.T) {
	transport := &countingTransport{}
	cat := searchCatalog(t, "https://api.example.com", false) // mutations off
	session := searchSession(t, &cat, &http.Client{Transport: transport}, "https://api.example.com")

	found, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_operations",
		Arguments: map[string]any{"query": "create pet"},
	})
	if err != nil || found.IsError {
		t.Fatalf("search: %v", err)
	}
	if len(structured[mcpserver.SearchOutput](t, found).Results) == 0 {
		t.Fatal("a policy-blocked operation is not findable; a model cannot learn why it may not call it")
	}

	described, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "describe_operation",
		Arguments: map[string]any{"id": "ns:POST:/pets"},
	})
	if err != nil || described.IsError {
		t.Fatalf("describe: %v", err)
	}
	out := structured[mcpserver.DescribeOutput](t, described)
	if out.Callable {
		t.Error("a policy-blocked operation reports itself callable")
	}
	if !strings.Contains(out.Refusal, "policy") {
		t.Errorf("refusal = %q, want it to name the policy", out.Refusal)
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}
