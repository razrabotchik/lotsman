package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

	// The arguments are valid on purpose. An earlier version of this test
	// called both operations with none, and both were refused -- by argument
	// validation, for a missing required field. It therefore passed with the
	// effect gate removed, which made it evidence for criterion 5 only by
	// accident. A test naming one control has to be the only thing that can
	// refuse.
	for _, call := range []struct {
		id        string
		arguments map[string]any
	}{
		{id: "ns:POST:/pets", arguments: map[string]any{"body": map[string]any{"name": "Murka"}}},
		{id: "ns:DELETE:/pets/{petId}", arguments: map[string]any{"path": map[string]any{"petId": "p-1"}}},
	} {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "call_read_operation",
			Arguments: map[string]any{"id": call.id, "arguments": call.arguments},
		})
		if err == nil && !res.IsError {
			t.Fatalf("call_read_operation called %s", call.id)
		}
		// And refused for the right reason: anything else would mean the
		// effect gate is not what is holding.
		if got := refusalText(res, err); !strings.Contains(got, "never calls another effect class") {
			t.Errorf("%s was refused, but not by the effect gate: %s", call.id, got)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

// refusalText is how a refusal reaches a caller: either a transport error or
// an error result carrying text.
func refusalText(res *mcp.CallToolResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if res == nil {
		return "<no result>"
	}
	var out strings.Builder
	for _, item := range res.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
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

// The published list does not grow with the catalog. That is the whole claim of
// search mode -- not "smaller", but *constant* -- and it was the one thing about
// the mode that no test held: the sizes in docs/corpus.md were measured by hand
// and then quoted as the reason the mode exists.
//
// Byte-identical is the right assertion rather than "within a margin": the
// meta-tool definitions are written in this package and mention nothing about
// the document, so any difference at all means something leaked from the
// catalog into the list.
func TestTheSearchModeToolListDoesNotGrowWithTheCatalog(t *testing.T) {
	small := catalog.Build("sha256:small", searchOperations("https://api.example.com"),
		catalog.Options{Mode: catalog.ModeSearch, Policy: policy.Config{AllowMutations: true}})

	// Two hundred operations over the same shapes: a catalog no tools-mode
	// client would accept, which is the case the mode is for.
	many := searchOperations("https://api.example.com")
	for i := 0; i < 50; i++ {
		for _, op := range searchOperations("https://api.example.com") {
			op.Key = domain.NewOperationKey("ns", op.Method,
				op.PathTemplate+"/"+strconv.Itoa(i))
			op.PathTemplate += "/" + strconv.Itoa(i)
			op.SourceOperationID += strconv.Itoa(i)
			op.Summary += strconv.Itoa(i)
			many = append(many, op)
		}
	}
	large := catalog.Build("sha256:large", many,
		catalog.Options{Mode: catalog.ModeSearch, Policy: policy.Config{AllowMutations: true}})
	if len(large.Tools) <= len(small.Tools) {
		t.Fatalf("the large catalog is not larger: %d vs %d tools", len(large.Tools), len(small.Tools))
	}

	// Two measurements from one call: the tool definitions, which is what must
	// not move, and the whole result, which is what a model actually pays for and
	// what docs/corpus.md quotes.
	list := func(cat *catalog.Catalog) (tools []byte, payload int, digest string) {
		t.Helper()
		session := searchSession(t, cat, nil, "https://api.example.com")
		res, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		encoded, err := json.Marshal(res.Tools)
		if err != nil {
			t.Fatal(err)
		}
		if res.Meta != nil {
			digest, _ = res.Meta["lotsman/catalogDigest"].(string)
		}
		whole, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		return encoded, len(whole), digest
	}

	first, payload, smallDigest := list(&small)
	second, largePayload, largeDigest := list(&large)
	if payload != largePayload {
		t.Errorf("the published payload grew with the catalog: %d then %d bytes", payload, largePayload)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the published list changed with the catalog:\n%d bytes for %d operations\n%d bytes for %d operations",
			len(first), len(small.Tools), len(second), len(large.Tools))
	}
	// The digest does differ, and that is not a contradiction: it identifies the
	// catalog a model can reach through those tools, not the literal list. A
	// client that cached search results against it has to know the catalog moved
	// even though `tools/list` did not (FR-74).
	if smallDigest == "" || smallDigest == largeDigest {
		t.Errorf("the catalog digest did not distinguish two different catalogs: %q vs %q",
			smallDigest, largeDigest)
	}
	// The size is quoted in docs/corpus.md as the reason the mode exists, so it
	// is recorded here too: a change is allowed, and going unnoticed is not.
	// Tool descriptions are what a model reads to use the mode at all, so this
	// is a budget rather than a target.
	//
	// This is the definitions alone. What a client of the advertised protocol
	// receives is the whole result -- 5 297 bytes read-only and 6 285 with the
	// mutating tool published, measured from a sessionless POST and recorded in
	// docs/corpus.md, where `--help` quotes it and a test holds the two
	// together.
	const definitionBytes = 6017
	if len(first) != definitionBytes {
		t.Errorf("the published definitions are %d bytes, recorded as %d; if the change was "+
			"intended, update this number and the measurement in docs/corpus.md",
			len(first), definitionBytes)
	}
}
