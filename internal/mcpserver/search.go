package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	// describeBudgetBytes bounds one describe_operation response. Search mode
	// exists because schemas are large; handing back an unbounded one would
	// reintroduce the problem through the other door.
	describeBudgetBytes = 24_000
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

// DescribeInput names one operation.
type DescribeInput struct {
	ID string `json:"id" jsonschema:"the operation key or tool name from search_operations"`
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
}

func addDescribeOperation(srv *mcp.Server, runners []runner) {
	tool := &mcp.Tool{
		Name:  "describe_operation",
		Title: "Describe one operation",
		Description: "The full argument schema and call metadata for one operation, by the id " +
			"search_operations returned. This is where argument schemas live; search results do " +
			"not carry them.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false),
		},
	}
	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in DescribeInput) (*mcp.CallToolResult, DescribeOutput, error) {
		prepared := find(runners, in.ID)
		if prepared == nil {
			return nil, DescribeOutput{}, unknownOperation(in.ID)
		}
		t := prepared.tool

		schema := publishedSchema(t)
		size := serializedSize(schema)
		reduced := false
		if size > describeBudgetBytes {
			// Drop prose, keep every constraint: the result still validates
			// identically, it just stops explaining itself.
			schema = withoutProse(schema)
			size = serializedSize(schema)
			reduced = true
		}

		return nil, DescribeOutput{
			Key:                 t.OperationKey,
			ToolName:            t.Name,
			Method:              t.Method,
			Path:                t.PathTemplate,
			Summary:             t.Description,
			Tags:                t.Tags,
			Effect:              t.Effect,
			Callable:            prepared.callable(),
			Refusal:             prepared.refusal,
			InputSchema:         schema,
			SchemaBytes:         size,
			DescriptionsOmitted: reduced,
		}, nil
	})
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

	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CallInput) (*mcp.CallToolResult, response.Result, error) {
		prepared := find(runners, in.ID)
		if prepared == nil {
			return nil, response.Result{}, unknownOperation(in.ID)
		}

		// The effect gate is re-applied here, on the operation the id actually
		// resolved to. A search result is discovery; identity, effect,
		// arguments, auth and policy are all checked again on the way in
		// (FR-49, FR-50).
		isRead := prepared.tool.Effect.IsRead()
		switch {
		case kind == readOnlyCall && !isRead:
			return nil, response.Result{}, errs.Errorf(errs.ClassPolicy,
				"lotsman: %s is %s, not a read; call_read_operation never calls another effect class",
				prepared.tool.OperationKey, prepared.tool.Effect.Effect)
		case kind == mutatingCall && isRead:
			return nil, response.Result{}, errs.Errorf(errs.ClassUsage,
				"lotsman: %s is a read; use call_read_operation", prepared.tool.OperationKey)
		}

		result, err := prepared.call(ctx, in.Arguments)
		if err != nil {
			return nil, response.Result{}, err
		}
		var res *mcp.CallToolResult
		if result.IsError {
			res = &mcp.CallToolResult{IsError: true}
		}
		return res, result, nil
	})
}

func unknownOperation(id string) error {
	return errs.Errorf(errs.ClassUsage,
		"lotsman: no published operation has the id %q; use search_operations to find one", id)
}

// serializedSize measures what a schema costs on the wire, which is the number
// the budget is about.
func serializedSize(schema map[string]any) int {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// withoutProse strips descriptions, titles and examples from a schema at every
// level. Everything that decides whether an argument is valid stays.
func withoutProse(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "description", "title", "examples":
			continue
		default:
			out[key] = pruneValue(value)
		}
	}
	return out
}

func pruneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return withoutProse(typed)
	case []any:
		pruned := make([]any, 0, len(typed))
		for _, item := range typed {
			pruned = append(pruned, pruneValue(item))
		}
		return pruned
	default:
		return value
	}
}
