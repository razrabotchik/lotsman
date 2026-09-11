package openapi

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// methodOrder is the fixed method order used within a path (pipeline.md
// 3.1): deterministic catalog order, matching libopenapi's own internal
// iota ordering for PathItem fields. OAS 3.2's TRACE/QUERY methods are
// listed for future-proofing but M1 targets 3.0/3.1 (FR-1).
var methodOrder = []struct {
	name string
	get  func(*v3.PathItem) *v3.Operation
}{
	{"GET", func(pi *v3.PathItem) *v3.Operation { return pi.Get }},
	{"PUT", func(pi *v3.PathItem) *v3.Operation { return pi.Put }},
	{"POST", func(pi *v3.PathItem) *v3.Operation { return pi.Post }},
	{"DELETE", func(pi *v3.PathItem) *v3.Operation { return pi.Delete }},
	{"OPTIONS", func(pi *v3.PathItem) *v3.Operation { return pi.Options }},
	{"HEAD", func(pi *v3.PathItem) *v3.Operation { return pi.Head }},
	{"PATCH", func(pi *v3.PathItem) *v3.Operation { return pi.Patch }},
	{"TRACE", func(pi *v3.PathItem) *v3.Operation { return pi.Trace }},
}

var pathPlaceholder = regexp.MustCompile(`\{([^{}]+)\}`)

// Document is the parse result: the enumerated operations plus any
// document-level diagnostics not scoped to a single operation (e.g.
// structural parse errors from libopenapi).
type Document struct {
	Operations  []domain.Operation  `json:"operations"`
	Diagnostics []domain.Diagnostic `json:"diagnostics,omitempty"`
}

// Parse builds the IR from raw OpenAPI 3.0/3.1 bytes: pipeline stage 1
// (parse via libopenapi) and stage 3.1 (enumerate paths×methods in
// deterministic order, merge path/operation parameters by (name, in)).
//
// namespace scopes OperationKey so multiple specs served together do not
// collide; callers with a single spec may pass "".
//
// logger receives libopenapi's own error/warning logs. It must never be nil
// in a caller that also runs the stdio MCP transport: libopenapi's own
// default configuration logs to stdout, which would corrupt the JSON-RPC
// stream. A nil logger here falls back to stderr rather than to that
// upstream default, so misuse fails safe.
//
// libopenapi types never leave this package (Constitution VIII): only
// domain IR and plain errors cross the boundary.
func Parse(specBytes []byte, namespace string, logger *slog.Logger) (*Document, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	config := datamodel.NewDocumentConfiguration()
	config.Logger = logger

	doc, err := libopenapi.NewDocumentWithConfiguration(specBytes, config)
	if err != nil {
		return nil, fmt.Errorf("openapi: parse: %w", err)
	}

	model, buildErr := doc.BuildV3Model()
	if model == nil {
		return nil, fmt.Errorf("openapi: build model: %w", buildErr)
	}

	out := &Document{}
	// libopenapi collects every structural error via errors.Join instead of
	// failing fast, so a partial model can still enumerate whatever parsed
	// (pipeline.md stage 1.2: show the user the full problem list, not just
	// the first one).
	for _, e := range flattenErrors(buildErr) {
		out.Diagnostics = append(out.Diagnostics, domain.Diagnostic{
			Severity: domain.SeverityError,
			Message:  e.Error(),
		})
	}

	if model.Model.Paths == nil {
		return out, nil
	}

	for _, path := range sortedPathKeys(model.Model.Paths.PathItems) {
		item, _ := model.Model.Paths.PathItems.Get(path)
		for _, m := range methodOrder {
			op := m.get(item)
			if op == nil {
				continue
			}
			out.Operations = append(out.Operations, buildOperation(namespace, m.name, path, op, item.Parameters))
		}
	}

	return out, nil
}

func buildOperation(namespace, method, path string, op *v3.Operation, pathParams []*v3.Parameter) domain.Operation {
	result := domain.Operation{
		Key:               domain.NewOperationKey(namespace, method, path),
		SourceOperationID: op.OperationId,
		Method:            method,
		PathTemplate:      path,
	}

	merged := mergeParameters(pathParams, op.Parameters)
	pointer := "#/paths/" + jsonPointerEscape(path) + "/" + strings.ToLower(method)

	rejected := false
	for _, name := range pathPlaceholder.FindAllStringSubmatch(path, -1) {
		placeholder := name[1]
		p, ok := merged[paramKey{placeholder, "path"}]
		if ok && p.Required != nil && *p.Required {
			continue
		}
		rejected = true
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{
			Severity: domain.SeverityError,
			Code:     domain.ReasonPathParameterMismatch,
			Pointer:  pointer,
			Message:  fmt.Sprintf("path placeholder %q has no required 'in: path' parameter", placeholder),
		})
	}

	if rejected {
		result.Support = domain.SupportStatus{
			Level:   domain.SupportRejected,
			Reasons: []domain.ReasonCode{domain.ReasonPathParameterMismatch},
		}
	} else {
		result.Support = domain.SupportStatus{Level: domain.SupportSupported}
	}
	return result
}

type paramKey struct{ name, in string }

// mergeParameters merges path-level and operation-level parameters keyed by
// (name, in); operation-level entries override path-level ones for the same
// key (pipeline.md 3.1).
func mergeParameters(pathParams, opParams []*v3.Parameter) map[paramKey]*v3.Parameter {
	merged := make(map[paramKey]*v3.Parameter, len(pathParams)+len(opParams))
	for _, p := range pathParams {
		merged[paramKey{p.Name, p.In}] = p
	}
	for _, p := range opParams {
		merged[paramKey{p.Name, p.In}] = p
	}
	return merged
}

func sortedPathKeys(items *orderedmap.Map[string, *v3.PathItem]) []string {
	keys := make([]string, 0, items.Len())
	for k := range items.FromOldest() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var jsonPointerReplacer = strings.NewReplacer("~", "~0", "/", "~1")

func jsonPointerEscape(s string) string {
	return jsonPointerReplacer.Replace(s)
}

// flattenErrors unwraps an errors.Join tree (as returned by
// doc.BuildV3Model) into its leaves.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range u.Unwrap() {
			out = append(out, flattenErrors(e)...)
		}
		return out
	}
	return []error{err}
}
