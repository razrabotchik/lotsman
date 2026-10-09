package mcpserver_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
)

// largeOperation has a body that refers to many shared definitions, each with
// enough prose and enough constraints that the schema stays over the budget
// once the prose is gone. oversized, when set, adds one definition too large
// for any page by itself.
func largeOperation(defs int, oversized bool) domain.Operation {
	bundle := domain.SchemaDefs{}
	refs := map[string]any{}
	for i := range defs {
		name := fmt.Sprintf("Def%03d", i)
		properties := map[string]any{}
		for j := range 20 {
			properties[fmt.Sprintf("field_%02d", j)] = map[string]any{
				"type": "string", "maxLength": 64, "description": strings.Repeat("prose ", 20),
			}
		}
		bundle[name] = domain.Schema{"type": "object", "properties": properties, "additionalProperties": false}
		refs[strings.ToLower(name)] = map[string]any{"$ref": "#/$defs/" + name}
	}
	if oversized {
		enum := make([]any, 6000)
		for i := range enum {
			enum[i] = fmt.Sprintf("value-%04d", i)
		}
		bundle["Huge"] = domain.Schema{"type": "string", "enum": enum}
		refs["huge"] = map[string]any{"$ref": "#/$defs/Huge"}
	}
	return domain.Operation{
		Key: "ns:POST:/big", Method: "POST", PathTemplate: "/big", SourceOperationID: "createBig",
		Servers: []string{"https://api.example.com"},
		Effect:  domain.EffectDecision{Effect: domain.EffectWrite, Source: domain.EffectSourceHTTPMethod},
		Input: domain.InputModel{
			Body: &domain.BodySpec{MediaType: "application/json", Required: true, Schema: domain.Schema{
				"type": "object", "properties": refs, "additionalProperties": false,
			}},
			Defs: bundle,
		},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
}

func describeSession(t *testing.T, op *domain.Operation) *mcp.ClientSession {
	t.Helper()
	cat := catalog.Build("sha256:test", []domain.Operation{*op}, catalog.Options{Mode: catalog.ModeSearch})
	return connect(t, &mcpserver.Options{Catalog: &cat})
}

// describePage asks for one page and checks the answer against the budget
// as it was received: structured content and its text copy together.
func describePage(t *testing.T, session *mcp.ClientSession, page int) (map[string]any, error) {
	t.Helper()
	arguments := map[string]any{"id": "ns:POST:/big"}
	if page > 0 {
		arguments["page"] = page
	}
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "describe_operation", Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("%s", errorText(t, res, nil))
	}
	wire, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > mcpserver.DescribeBudgetBytesForTest {
		t.Errorf("page %d is %d bytes on the wire, over the %d-byte budget",
			page, len(wire), mcpserver.DescribeBudgetBytesForTest)
	}
	out, _ := res.StructuredContent.(map[string]any)
	return out, nil
}

// A schema too large to describe at once is described in pages that each fit
// the budget, and the pages together are the whole contract -- every group
// and every definition, nothing approximated.
func TestALargeSchemaIsDescribedInPagesThatFit(t *testing.T) {
	op := largeOperation(60, false)
	session := describeSession(t, &op)

	first, err := describePage(t, session, 0)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	pages, _ := first["pages"].(float64)
	if pages < 2 {
		t.Fatalf("pages = %v, want the schema split", first["pages"])
	}

	properties, defs := map[string]any{}, map[string]any{}
	for page := 1; page <= int(pages); page++ {
		out, err := describePage(t, session, page)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		schema, _ := out["inputSchema"].(map[string]any)
		groups, _ := schema["properties"].(map[string]any)
		for name, group := range groups {
			properties[name] = group
		}
		if shared, ok := schema["$defs"].(map[string]any); ok {
			for name, def := range shared {
				if _, twice := defs[name]; twice {
					t.Errorf("$defs/%s is on two pages", name)
				}
				defs[name] = def
			}
		}
	}
	if len(defs) != len(op.Input.Defs) {
		t.Errorf("the pages carry %d definitions, want %d", len(defs), len(op.Input.Defs))
	}
	if _, ok := properties[domain.GroupBody]; !ok {
		t.Error("no page carries the body group")
	}
	// Only prose was dropped: the constraints arrive as published.
	field, _ := defs["Def000"].(map[string]any)["properties"].(map[string]any)["field_00"].(map[string]any)
	if want := map[string]any{"type": "string", "maxLength": float64(64)}; !reflect.DeepEqual(field, want) {
		t.Errorf("Def000.field_00 = %v, want %v", field, want)
	}

	if _, err := describePage(t, session, int(pages)+1); err == nil {
		t.Error("a page past the last was described")
	}
}

// A part too large for any page on its own cannot be described, and the
// answer says so by name and size instead of exceeding the budget or
// dropping it without a word.
func TestAPartTooLargeForAnyPageIsNamed(t *testing.T) {
	huge := largeOperation(5, true)
	session := describeSession(t, &huge)
	out, err := describePage(t, session, 0)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	listed, _ := out["undescribable"].([]any)
	var first map[string]any
	if len(listed) == 1 {
		first, _ = listed[0].(map[string]any)
	}
	if first["name"] != "$defs/Huge" {
		t.Errorf("undescribable = %v, want $defs/Huge", out["undescribable"])
	}
}

// The schema a model reads in search mode carries the document's numbers. The
// SDK's typed output would have rounded int64's limit to 2^63 on the way out.
func TestDescribeKeepsTheSchemasNumbers(t *testing.T) {
	op := accountOperation("https://api.example.com")
	cat := catalog.Build("sha256:test", []domain.Operation{op}, catalog.Options{Mode: catalog.ModeSearch})
	session := connect(t, &mcpserver.Options{Catalog: &cat})
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "describe_operation", Arguments: map[string]any{"id": string(op.Key)},
	})
	if err != nil || res.IsError {
		t.Fatalf("describe: %v %q", err, errorText(t, res, err))
	}
	content, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want text", res.Content[0])
	}
	if text := content.Text; !strings.Contains(text, `"maximum":`+maxInt64) {
		t.Errorf("the described bound is not %s:\n%s", maxInt64, content.Text)
	}
}
