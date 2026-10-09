package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/response"
	"github.com/razrabotchik/lotsman/internal/searchindex"
)

// Search-mode limits.
const (
	// defaultSearchLimit is what a model gets when it does not say. Ten
	// candidates is enough to choose from and small enough to read.
	defaultSearchLimit = 10
	maxSearchLimit     = 50
	// describeBudgetBytes bounds one describe_operation result as it goes on
	// the wire: the structured content and the text copy of it that clients
	// without structured output read. Search mode exists because schemas are
	// large; handing back an unbounded one would reintroduce the problem
	// through the other door. The number is the old 24 000-byte bound on the
	// schema alone, counted the way it is actually paid -- twice.
	describeBudgetBytes = 48_000
)

// addSearchTools publishes the five meta-tools of FR-48 over the same runners
// tools mode would have used.
//
// The mutating call tool is published only when something is actually allowed
// to mutate: advertising a tool whose every call is refused teaches a model
// nothing except to keep trying.
func addSearchTools(srv *mcp.Server, cat *catalog.Catalog, runners []runner, mutationsAllowed bool) {
	index := searchindex.FromCatalog(cat)

	addSearchOperations(srv, index)
	addListTags(srv, index)
	addDescribeOperation(srv, runners)
	addCallOperation(srv, runners, readOnlyCall)
	if mutationsAllowed {
		addCallOperation(srv, runners, mutatingCall)
	}
}

// SearchInput is the argument schema of search_operations.
type SearchInput struct {
	Query   string   `json:"query,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Methods []string `json:"methods,omitempty"`
	Effects []string `json:"effects,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// SearchOutput is what search_operations returns: identity and one line of
// prose per hit, never a schema. Schemas are what describe_operation is for,
// one operation at a time, which is the whole reason this mode is smaller.
type SearchOutput struct {
	Results []searchindex.Result `json:"results"`
	Total   int                  `json:"total"`
	// Note explains an empty result rather than leaving a model to guess.
	Note string `json:"note,omitempty"`
}

func addSearchOperations(srv *mcp.Server, index *searchindex.Index) {
	tool := &mcp.Tool{
		Name:  "search_operations",
		Title: "Search operations",
		Description: "Find operations in this API by words from their name, path, tags or summary. " +
			"Returns identity and a one-line summary per match, not argument schemas: call " +
			"describe_operation for the one you want. Filters (tags, methods, effects) exclude " +
			"rather than rank. An empty query with filters browses.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false),
		},
	}
	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		limit := in.Limit
		switch {
		case limit <= 0:
			limit = defaultSearchLimit
		case limit > maxSearchLimit:
			limit = maxSearchLimit
		}

		effects := make([]domain.Effect, 0, len(in.Effects))
		for _, effect := range in.Effects {
			effects = append(effects, domain.Effect(effect))
		}

		results := index.Search(in.Query, searchindex.Filters{
			Tags: in.Tags, Methods: in.Methods, Effects: effects,
		}, limit)

		out := SearchOutput{Results: results, Total: len(results)}
		if len(results) == 0 {
			// No "closest" operation is invented, so the emptiness is
			// explained instead (Principle I).
			out.Note = fmt.Sprintf("no operation matches these words; %d operations are published, "+
				"try list_tags or fewer words", index.Len())
		}
		return nil, out, nil
	})
}

// TagsOutput is the tag vocabulary with counts.
type TagsOutput struct {
	Tags  []searchindex.TagCount `json:"tags"`
	Total int                    `json:"total"`
}

func addListTags(srv *mcp.Server, index *searchindex.Index) {
	tool := &mcp.Tool{
		Name:  "list_tags",
		Title: "List operation tags",
		Description: "The tag vocabulary of this API with a count per tag, most used first. " +
			"Use a tag as a filter in search_operations to narrow before searching.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false),
		},
	}
	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, TagsOutput, error) {
		tags := index.Tags()
		return nil, TagsOutput{Tags: tags, Total: len(tags)}, nil
	})
}

// DescribeInput names one operation, and optionally one page of its schema.
type DescribeInput struct {
	ID   string `json:"id" jsonschema:"the operation key or tool name from search_operations"`
	Page int    `json:"page,omitempty" jsonschema:"for a schema described in pages, which page; the first when omitted"`
}

// DescribeOutput is everything needed to call one operation.
type DescribeOutput struct {
	Key         domain.OperationKey   `json:"key"`
	ToolName    string                `json:"toolName"`
	Method      string                `json:"method"`
	Path        string                `json:"pathTemplate"`
	Summary     string                `json:"summary,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Effect      domain.EffectDecision `json:"effect"`
	Callable    bool                  `json:"callable"`
	Refusal     string                `json:"refusal,omitempty"`
	InputSchema map[string]any        `json:"inputSchema"`
	SchemaBytes int                   `json:"schemaBytes"`
	// DescriptionsOmitted says the schema was reduced to fit the response
	// budget. Validation is unaffected -- only prose was dropped -- but a model
	// deserves to know it is reading an abridged contract.
	DescriptionsOmitted bool `json:"descriptionsOmitted,omitempty"`
	// Page and Pages are set when the schema is too large for one answer.
	// InputSchema is then one page of it: the full schema is the union of
	// the `properties` and `$defs` of every page.
	Page  int    `json:"page,omitempty"`
	Pages int    `json:"pages,omitempty"`
	Note  string `json:"note,omitempty"`
	// Undescribable names the parts too large for any page on their own,
	// with their sizes. They are still validated on every call.
	Undescribable []SchemaPart `json:"undescribable,omitempty"`
}

// SchemaPart is one piece of an input schema: an argument group, or one
// shared definition the groups refer to.
type SchemaPart struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

func addDescribeOperation(srv *mcp.Server, runners []runner) {
	tool := &mcp.Tool{
		Name:  "describe_operation",
		Title: "Describe one operation",
		Description: "The full argument schema and call metadata for one operation, by the id " +
			"search_operations returned. This is where argument schemas live; search results do " +
			"not carry them. A schema too large for one answer comes in pages; ask again with " +
			"page set to each.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false),
		},
	}
	// The result is built here rather than returned as a typed output. The
	// SDK checks a typed output against its schema by decoding it into
	// float64s and encoding it again, which would publish a bound of
	// 9223372036854775807 as 2^63 in the one place a model reads it; and it
	// appends a text copy whose size the budget has to count.
	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in DescribeInput) (*mcp.CallToolResult, any, error) {
		prepared := find(runners, in.ID)
		if prepared == nil {
			return nil, nil, unknownOperation(in.ID)
		}
		encoded, err := describe(prepared, in.Page)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
			StructuredContent: json.RawMessage(encoded),
		}, nil, nil
	})
}

// describe renders one describe_operation answer that fits the budget, or
// says why there is none.
//
// The order is: the schema as published; the schema without its prose, which
// validates identically; and, when even that is too large, the schema without
// prose in pages. No branch returns more than the budget.
func describe(prepared *runner, page int) ([]byte, error) {
	t := prepared.tool
	full := publishedSchema(t)
	out := DescribeOutput{
		Key:      t.OperationKey,
		ToolName: t.Name,
		Method:   t.Method,
		Path:     t.PathTemplate,
		Summary:  t.Description,
		Tags:     t.Tags,
		Effect:   t.Effect,
		Callable: prepared.callable(),
		Refusal:  prepared.refusal,
	}

	if page <= 1 {
		for _, reduced := range []bool{false, true} {
			candidate := full
			if reduced {
				// Drop prose, keep every constraint: the result still
				// validates identically, it just stops explaining itself.
				candidate = withoutProse(full)
			}
			out.InputSchema = candidate
			out.SchemaBytes = serializedSize(candidate)
			out.DescriptionsOmitted = reduced
			if encoded, fits := withinBudget(&out); fits {
				return encoded, nil
			}
		}
	}

	bare := withoutProse(full)
	out.SchemaBytes = serializedSize(bare)
	out.DescriptionsOmitted = true
	pages := paginate(&out, bare)
	if len(pages) == 0 {
		return nil, errs.Errorf(errs.ClassUnsupported,
			"lotsman: %s has a %d-byte schema, and no part of it fits the %d-byte describe_operation "+
				"budget; it cannot be described in search mode, though it can still be called",
			t.OperationKey, out.SchemaBytes, describeBudgetBytes)
	}
	if page < 1 {
		page = 1
	}
	if page > len(pages) {
		return nil, errs.Errorf(errs.ClassUsage,
			"lotsman: %s is described in %d pages; there is no page %d", t.OperationKey, len(pages), page)
	}
	return pages[page-1], nil
}

// paginate splits a schema that has lost its prose and is still too large
// into pages that each fit the budget.
//
// A page is a fragment of the same document: the top-level shape (type,
// required groups, additionalProperties) and a subset of its argument groups
// and $defs. Parts are packed in a fixed order -- the argument groups as
// arguments are grouped, then the definitions by name -- so the same schema
// is always cut the same way, and page 2 of one call is page 2 of the next.
func paginate(out *DescribeOutput, bare map[string]any) [][]byte {
	type part struct {
		container, name string
		schema          any
	}
	var parts []part
	properties, _ := bare["properties"].(map[string]any)
	for _, group := range domain.GroupOrder {
		if schema, ok := properties[group]; ok {
			parts = append(parts, part{"properties", group, schema})
		}
	}
	defs, _ := bare["$defs"].(map[string]any)
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		parts = append(parts, part{"$defs", name, defs[name]})
	}

	fragment := func(members []part) map[string]any {
		shape := map[string]any{}
		for key, value := range bare {
			if key != "properties" && key != "$defs" {
				shape[key] = value
			}
		}
		groups, shared := map[string]any{}, map[string]any{}
		for _, member := range members {
			if member.container == "properties" {
				groups[member.name] = member.schema
			} else {
				shared[member.name] = member.schema
			}
		}
		shape["properties"] = groups
		if len(shared) > 0 {
			shape["$defs"] = shared
		}
		return shape
	}

	// Each part's cost on the wire is measured once: its member entry, and
	// that entry again as escaped text. A page costs the result around it
	// plus its parts. The page count, the note and the list of what did not
	// fit are added afterwards; they cost a few hundred bytes, and the
	// reserve keeps a cut made without them honest. Every rendered page is
	// measured again below, so an estimate can refuse but never overspend.
	const reserve = 2048
	empty := *out
	empty.InputSchema = fragment(nil)
	encodedEmpty, err := json.Marshal(&empty)
	if err != nil {
		return nil
	}
	available := describeBudgetBytes - wireSize(encodedEmpty) - reserve
	cost := func(p part) int {
		entry, err := json.Marshal(map[string]any{p.name: p.schema})
		if err != nil {
			return describeBudgetBytes
		}
		return wireSize(entry) - resultEnvelopeBytes
	}

	var cuts [][]part
	var current []part
	var undescribable []SchemaPart
	used := 0
	for _, p := range parts {
		price := cost(p)
		switch {
		case price > available:
			undescribable = append(undescribable, SchemaPart{
				Name: describedName(p.container, p.name), Bytes: jsonSize(p.schema),
			})
			continue
		case used+price > available:
			cuts = append(cuts, current)
			current, used = nil, 0
		}
		current = append(current, p)
		used += price
	}
	if len(current) > 0 {
		cuts = append(cuts, current)
	}

	pages := make([][]byte, 0, len(cuts))
	for i, members := range cuts {
		rendered := *out
		rendered.InputSchema = fragment(members)
		rendered.Page = i + 1
		rendered.Pages = len(cuts)
		rendered.Undescribable = undescribable
		rendered.Note = fmt.Sprintf("Page %d of %d. The schema is too large to describe at once; "+
			"the full schema is the union of the properties and $defs of every page, so ask "+
			"describe_operation again with page set to each.", i+1, len(cuts))
		if len(undescribable) > 0 {
			rendered.Note += " The parts listed in undescribable are too large for any page; they " +
				"are still validated when the operation is called."
		}
		encoded, err := json.Marshal(&rendered)
		if err != nil || wireSize(encoded) > describeBudgetBytes {
			// The reserve was not enough. Failing here is better than
			// handing back more than the budget promised.
			return nil
		}
		pages = append(pages, encoded)
	}
	return pages
}

func describedName(container, name string) string {
	if container == "$defs" {
		return "$defs/" + name
	}
	return name
}

// withinBudget encodes a result and reports whether it fits, counted as it is
// sent (wireSize).
func withinBudget(out *DescribeOutput) ([]byte, bool) {
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return encoded, wireSize(encoded) <= describeBudgetBytes
}

// resultEnvelopeBytes covers what the result carries besides the payload --
// the content wrapper, the server identity in _meta -- measured at under 200
// bytes and reserved generously.
const resultEnvelopeBytes = 512

// wireSize is what an encoded payload costs in a tools/call result: once as
// structured content, and once more as its text copy -- which is a JSON
// string, so every quote in the payload is escaped and the copy is the larger
// of the two. Counting it as "twice the payload" let a page out at 48 484
// bytes against a 48 000-byte budget.
func wireSize(encoded []byte) int {
	return len(encoded) + jsonSize(string(encoded)) + resultEnvelopeBytes
}

// CallInput names an operation and carries its grouped arguments.
type CallInput struct {
	ID        string         `json:"id" jsonschema:"the operation key or tool name"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema:"the grouped arguments from describe_operation"`
}

// callKind distinguishes the two call tools. They are separate tools, not one
// tool with a flag, because a model that may only read must not be holding a
// tool that can write (FR-50).
type callKind int

const (
	readOnlyCall callKind = iota
	mutatingCall
)

func addCallOperation(srv *mcp.Server, runners []runner, kind callKind) {
	tool := &mcp.Tool{
		Name:  "call_read_operation",
		Title: "Call a read operation",
		Description: "Call an operation whose effect is read, with the grouped arguments from " +
			"describe_operation. Refuses anything that is not a read, whatever the search result said.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, OpenWorldHint: ptr(true),
		},
	}
	if kind == mutatingCall {
		tool = &mcp.Tool{
			Name:  "call_mutating_operation",
			Title: "Call a mutating operation",
			Description: "Call an operation that changes state, with the grouped arguments from " +
				"describe_operation. Every call is re-checked against policy; a search result is " +
				"not permission.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: false, DestructiveHint: ptr(true), OpenWorldHint: ptr(true),
			},
		}
	}

	mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in CallInput) (*mcp.CallToolResult, response.Result, error) {
		prepared := find(runners, in.ID)
		if prepared == nil {
			return nil, response.Result{}, unknownOperation(in.ID)
		}

		// The effect gate is re-applied here, on the operation the id actually
		// resolved to. A search result is discovery; identity, effect,
		// arguments, auth and policy are all checked again on the way in
		// (FR-49, FR-50).
		isRead := prepared.tool.Effect.IsRead()
		var refused error
		switch {
		case kind == readOnlyCall && !isRead:
			refused = errs.Errorf(errs.ClassPolicy,
				"lotsman: %s is %s, not a read; call_read_operation never calls another effect class",
				prepared.tool.OperationKey, prepared.tool.Effect.Effect)
		case kind == mutatingCall && isRead:
			refused = errs.Errorf(errs.ClassUsage,
				"lotsman: %s is a read; use call_read_operation", prepared.tool.OperationKey)
		}
		if refused != nil {
			// Recorded here, with its own class, rather than left to the
			// catch-all for calls that never reached a runner.
			prepared.record(ctx, time.Now(), response.Result{}, outcome{}, refused)
			return nil, response.Result{}, refused
		}

		// in.Arguments went through the SDK's decoding, which rounds every
		// number to a float64; the call is made from what the client sent.
		args, err := callArguments(req.Params.Arguments)
		if err != nil {
			return nil, response.Result{}, err
		}
		result, err := prepared.call(ctx, req, args)
		if err != nil {
			var ask *pending
			if errors.As(err, &ask) {
				return ask.result, response.Result{}, nil
			}
			return nil, response.Result{}, err
		}
		var res *mcp.CallToolResult
		if result.IsError {
			res = &mcp.CallToolResult{IsError: true}
		}
		return res, result, nil
	})
}

// callArguments reads the grouped arguments out of a call tool's raw input,
// numbers intact (argvalidate.Decode).
func callArguments(raw json.RawMessage) (map[string]any, error) {
	var envelope struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "lotsman: the call's input is not a JSON object: %w", err)
	}
	return argvalidate.Decode(envelope.Arguments)
}

func unknownOperation(id string) error {
	return errs.Errorf(errs.ClassUsage,
		"lotsman: no published operation has the id %q; use search_operations to find one", id)
}

// serializedSize measures what a schema costs on the wire, which is the number
// the budget is about.
func serializedSize(schema map[string]any) int { return jsonSize(schema) }

func jsonSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// withoutProse strips descriptions, titles and examples from a schema at every
// depth, so that a schema too large for the response budget can be sent
// without its explanations rather than not at all.
//
// It recurses only where a value is known to be a schema. That is the whole
// difficulty, and getting it wrong is not a cosmetic bug:
//
//   - Under `properties`, the keys are the names of the API's fields. A field
//     called `description` is data, and dropping it produces a schema that
//     rejects the very argument the API expects -- a call that works in tools
//     mode failing in search mode, with the caller told `descriptionsOmitted`
//     as if only prose had gone.
//   - Under `enum`, `const` and `default`, the values are instance data.
//     Walking into them looking for keywords would edit the API's own values.
//   - An unrecognised keyword is left exactly as it was found. lotsman does
//     not know what a vendor extension means, and guessing is how a schema
//     stops describing the API.
func withoutProse(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch {
		case domain.ProseKeywords[key]:
			continue
		case domain.SchemaValuedKeywords[key]:
			out[key] = pruneSchema(value)
		case domain.SchemaListKeywords[key]:
			out[key] = pruneSchemaList(value)
		case domain.NamedSchemaKeywords[key]:
			out[key] = pruneNamedSchemas(value)
		default:
			// Data, or something this build does not recognise. Either way it
			// is copied through untouched.
			out[key] = value
		}
	}
	return out
}

// pruneSchema prunes a value that is a schema, or leaves it alone when it is
// not one (`additionalProperties: false` is a boolean, not a schema).
func pruneSchema(value any) any {
	if schema, ok := value.(map[string]any); ok {
		return withoutProse(schema)
	}
	return value
}

// pruneSchemaList prunes each schema in an array of them.
func pruneSchemaList(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	pruned := make([]any, 0, len(list))
	for _, item := range list {
		pruned = append(pruned, pruneSchema(item))
	}
	return pruned
}

// pruneNamedSchemas prunes the values of a name-to-schema map and never the
// names.
func pruneNamedSchemas(value any) any {
	named, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(named))
	for name, sub := range named {
		out[name] = pruneSchema(sub)
	}
	return out
}
