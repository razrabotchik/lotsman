package openapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/policy"
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
	// Version is the document's own `openapi` value, reported as authored so
	// a reader can tell a 3.0 document (which needs translation) from a 3.1
	// one (which mostly does not).
	Version     string              `json:"version,omitempty"`
	Operations  []domain.Operation  `json:"operations"`
	Diagnostics []domain.Diagnostic `json:"diagnostics,omitempty"`
}

// HasErrors reports whether parsing or model construction produced a
// document-level error. Inspect-style callers may still use the partial model
// for diagnostics; serve callers must fail closed.
func (d *Document) HasErrors() bool {
	for _, diagnostic := range d.Diagnostics {
		if diagnostic.Severity == domain.SeverityError {
			return true
		}
	}
	return false
}

// Options configures Parse.
type Options struct {
	// Namespace scopes OperationKey so multiple specs served together do not
	// collide. The empty value means the single-spec default.
	Namespace string

	// Logger receives lotsman's own progress logging. It must never write to
	// stdout: on stdio transport stdout carries protocol frames only.
	Logger *slog.Logger

	// ParserLog reinstates libopenapi's own log, which is discarded by
	// default because everything in it reaches lotsman as a diagnostic with
	// better provenance (see parserLogger). For debugging the adapter itself.
	ParserLog *slog.Logger

	// RootPath is the directory the document was loaded from
	// (specsource.Source.RootPath), carried here as the confinement root for
	// $ref resolution. It is empty for stdin.
	RootPath string

	// Limits bounds parse time, operation count and the $ref closure.
	Limits Limits
}

// Parse builds the IR from raw OpenAPI 3.0/3.1 bytes: pipeline stage 1
// (parse via libopenapi), stage 2 ($ref budget) and stage 3.1 (enumerate
// paths x methods in deterministic order, merge path/operation parameters by
// (name, in)).
//
// Every budget is checked before the work it bounds: the $ref audit runs on
// the raw node tree before libopenapi resolves anything, and the operation
// count is checked before the IR is built.
//
// libopenapi types never leave this package (Constitution VIII): only domain
// IR and plain errors cross the boundary.
//
// value, copied once per document; a pointer would buy nothing and would make
// the safe zero value optional.
//
//nolint:gocritic // hugeParam: Options is the entry point's configuration
func Parse(ctx context.Context, specBytes []byte, opts Options) (*Document, error) {
	limits := opts.Limits

	scan, err := scanRefs(specBytes, opts.RootPath, limits)
	if err != nil {
		return nil, err
	}

	if versionErr := checkVersion(scan); versionErr != nil {
		return nil, versionErr
	}

	parsed, err := buildModel(ctx, specBytes, parseConfig{
		logger:   parserLogger(opts.ParserLog),
		timeout:  limits.parseTimeout(),
		rootPath: opts.RootPath,
		files:    scan.files,
	})
	if err != nil {
		return nil, err
	}
	model := parsed.model

	out := &Document{Version: model.Model.Version, Diagnostics: scan.diagnostics}
	// libopenapi collects every structural error via errors.Join instead of
	// failing fast, so a partial model can still enumerate whatever parsed
	// (pipeline.md stage 1.2: show the user the full problem list, not just
	// the first one).
	for _, e := range flattenErrors(parsed.buildErr) {
		out.Diagnostics = append(out.Diagnostics, domain.Diagnostic{
			Severity: domain.SeverityError,
			Message:  e.Error(),
		})
	}

	if model.Model.Paths == nil {
		return out, nil
	}

	paths := sortedPathKeys(model.Model.Paths.PathItems)
	if count := countOperations(model, paths); count > limits.maxOperations() {
		return nil, errs.Errorf(errs.ClassUnsupported,
			"openapi: document declares %d operations, limit is %d", count, limits.maxOperations())
	}

	rootServers := model.Model.Servers
	var schemes *orderedmap.Map[string, *v3.SecurityScheme]
	if model.Model.Components != nil {
		schemes = model.Model.Components.SecuritySchemes
	}
	for _, path := range paths {
		item, _ := model.Model.Paths.PathItems.Get(path)
		for _, m := range methodOrder {
			op := m.get(item)
			if op == nil {
				continue
			}
			out.Operations = append(out.Operations,
				buildOperation(opts.Namespace, m.name, path, op, item.Parameters, item.Servers, rootServers, model.Model.Security, schemes))
		}
	}
	attachRefDiagnostics(out.Operations, scan.diagnostics)

	return out, nil
}

// checkVersion refuses a document lotsman does not translate, in its own
// words and before the parser is handed the bytes (pipeline.md stage 1.1).
//
// Swagger 2.0 is the case that matters in practice: several major vendors
// still publish it, and "supplied spec is a different version (oas2), try
// BuildV2Model()" is a sentence about libopenapi's API, not about the
// operator's document.
func checkVersion(scan *refScan) error {
	switch {
	case scan.swagger != "":
		return errs.Errorf(errs.ClassUnsupported,
			"openapi: document declares swagger: %s; lotsman translates OpenAPI 3.0 and 3.1 "+
				"(a Swagger 2.0 compatibility adapter is not part of this release)", scan.swagger)
	case scan.version == "":
		return errs.Errorf(errs.ClassSpecInvalid,
			"openapi: document declares no `openapi` version, so it is not an OpenAPI document")
	case !strings.HasPrefix(scan.version, "3."):
		return errs.Errorf(errs.ClassUnsupported,
			"openapi: document declares openapi: %s; lotsman translates OpenAPI 3.0 and 3.1", scan.version)
	}
	return nil
}

// buildModel runs libopenapi under a deadline. libopenapi exposes no
// cancellation hook, so the deadline bounds how long lotsman waits, not the
// work itself: an abandoned goroutine finishes on its own and is collected,
// and the byte, node and $ref budgets are what keep that work finite.
//
// A returned error is fatal; parsedModel.buildErr holds the structural errors
// a partial model survives.
func buildModel(ctx context.Context, specBytes []byte, cfg parseConfig) (*parsedModel, error) {
	logger, timeout := cfg.logger, cfg.timeout
	if err := ctx.Err(); err != nil {
		// The caller is already gone; starting a parse nobody waits for would
		// only burn CPU on an untrusted document.
		return nil, errs.Errorf(errs.ClassInternal, "openapi: parse cancelled: %w", err)
	}

	deadlined, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		parsed *parsedModel
		fatal  error
	}
	done := make(chan outcome, 1)
	go func() {
		config := datamodel.NewDocumentConfiguration()
		config.Logger = logger
		// Remote references are never fetched: a document must not be able to
		// make lotsman issue a request of its choosing.
		config.AllowRemoteReferences = false
		// File references are enabled only for documents lotsman has already
		// resolved, confined to the spec root and budgeted. BasePath alone
		// would point the rolodex at a directory and trust it to stay inside;
		// FileFilter narrows it to the exact list, so a file that is present
		// but never referenced is still never read.
		if len(cfg.files) > 0 {
			config.BasePath = cfg.rootPath
			config.AllowFileReferences = true
			config.FileFilter = append([]string(nil), cfg.files...)
		} else {
			config.AllowFileReferences = false
		}
		// A circular reference is a legitimate shape now that references are
		// published as references rather than inlined (ADR-0009).
		config.IgnorePolymorphicCircularReferences = true
		config.IgnoreArrayCircularReferences = true

		doc, err := libopenapi.NewDocumentWithConfiguration(specBytes, config)
		if err != nil {
			done <- outcome{fatal: errs.Errorf(errs.ClassSpecInvalid, "openapi: parse: %w", err)}
			return
		}
		model, buildErr := doc.BuildV3Model()
		if model == nil {
			done <- outcome{fatal: errs.Errorf(errs.ClassSpecInvalid, "openapi: build model: %w", buildErr)}
			return
		}
		done <- outcome{parsed: &parsedModel{model: model, buildErr: buildErr}}
	}()

	select {
	case res := <-done:
		return res.parsed, res.fatal
	case <-deadlined.Done():
		if ctx.Err() != nil {
			// The caller went away (signal, client disconnect): not the
			// document's fault, so not the document's error class.
			return nil, errs.Errorf(errs.ClassInternal, "openapi: parse cancelled: %w", ctx.Err())
		}
		if errors.Is(deadlined.Err(), context.DeadlineExceeded) {
			return nil, errs.Errorf(errs.ClassSpecInvalid,
				"openapi: parse exceeded the %s budget", timeout)
		}
		return nil, errs.Errorf(errs.ClassInternal, "openapi: parse: %w", deadlined.Err())
	}
}

// parseConfig is what buildModel needs from the caller: the logger and
// deadline, plus the confined file allowlist the rolodex may read.
type parseConfig struct {
	logger   *slog.Logger
	timeout  time.Duration
	rootPath string
	files    []string
}

// parsedModel is libopenapi's output crossing back into lotsman's control:
// the model plus the structural errors it survived.
type parsedModel struct {
	model    *libopenapi.DocumentModel[v3.Document]
	buildErr error
}

// countOperations counts paths x declared methods without building anything,
// so an oversized document is refused before the IR for it is allocated.
func countOperations(model *libopenapi.DocumentModel[v3.Document], paths []string) int {
	count := 0
	for _, path := range paths {
		item, ok := model.Model.Paths.PathItems.Get(path)
		if !ok {
			continue
		}
		for _, m := range methodOrder {
			if m.get(item) != nil {
				count++
			}
		}
	}
	return count
}

func buildOperation(namespace, method, path string, op *v3.Operation, pathParams []*v3.Parameter, pathServers, rootServers []*v3.Server, rootSecurity []*base.SecurityRequirement, schemes *orderedmap.Map[string, *v3.SecurityScheme]) domain.Operation {
	result := domain.Operation{
		Key:               domain.NewOperationKey(namespace, method, path),
		SourceOperationID: op.OperationId,
		Method:            method,
		PathTemplate:      path,
		Summary:           op.Summary,
		Description:       op.Description,
		Tags:              append([]string(nil), op.Tags...),
		Servers:           effectiveServers(op.Servers, pathServers, rootServers),
	}

	pointer := operationPointer(method, path)
	merged := mergeParameters(pathParams, op.Parameters)

	result.Effect = policy.Decide(policy.Candidate{
		Method:       method,
		OperationID:  op.OperationId,
		PathTemplate: path,
		Summary:      op.Summary,
	})

	// One bundle per operation: its parameters and its body share whatever
	// components they both reference, and a tool's input schema is a
	// standalone document that has to carry its own $defs.
	defs := newBundle()
	input := buildInput(merged, pointer, defs)
	result.Input = input.model
	result.Diagnostics = append(result.Diagnostics, input.diagnostics...)
	result.ExecutionBlockers = append(result.ExecutionBlockers, input.blockers...)

	body := buildBody(op.RequestBody, pointer, defs)
	result.Input.Body = body.spec
	if len(defs.defs) > 0 {
		result.Input.Defs = defs.defs
	}
	result.Diagnostics = append(result.Diagnostics, body.diagnostics...)

	// Whether the credentials an operation needs are *available* depends on
	// configuration, not on the document, so that verdict belongs downstream
	// (catalog, which knows the configured profiles). The IR states only what
	// the document asks for.
	result.Security = buildSecurity(op.Security, rootSecurity, schemes)
	security := evaluateSecurity(result.Security)

	rejections := input.rejections
	for _, reason := range body.rejections {
		rejections = appendReason(rejections, reason)
	}
	if security.unsatisfiable {
		rejections = appendReason(rejections, domain.ReasonUnsupportedSecurityScheme)
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{
			Severity: domain.SeverityError,
			Code:     domain.ReasonUnsupportedSecurityScheme,
			Pointer:  pointer + "/security",
			Message: "every security alternative needs a credential lotsman cannot supply " +
				"(only apiKey and HTTP basic/bearer are in this release), so the operation could never be called",
		})
	}
	for _, name := range pathPlaceholder.FindAllStringSubmatch(path, -1) {
		placeholder := name[1]
		p, ok := merged[paramKey{placeholder, "path"}]
		if ok && p.Required != nil && *p.Required {
			continue
		}
		rejections = appendReason(rejections, domain.ReasonPathParameterMismatch)
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{
			Severity: domain.SeverityError,
			Code:     domain.ReasonPathParameterMismatch,
			Pointer:  pointer,
			Message:  fmt.Sprintf("path placeholder %q has no required 'in: path' parameter", placeholder),
		})
	}

	if len(rejections) > 0 {
		result.Support = domain.SupportStatus{Level: domain.SupportRejected, Reasons: rejections}
	} else {
		result.Support = domain.SupportStatus{Level: domain.SupportSupported}
	}
	return result
}

// effectiveServers implements the operation→path→root inheritance from
// pipeline.md 3.1: the first non-empty level wins, no merging across
// levels. Server variables are not substituted yet; a URL that still contains
// "{...}" is passed through as-is and requestbuild rejects it rather than
// guessing a value. ADR-0005 additionally requires explicit --base-url before
// this untrusted metadata can authorize a network call.
func effectiveServers(opServers, pathServers, rootServers []*v3.Server) []string {
	for _, level := range [][]*v3.Server{opServers, pathServers, rootServers} {
		if len(level) == 0 {
			continue
		}
		urls := make([]string, 0, len(level))
		for _, s := range level {
			urls = append(urls, s.URL)
		}
		return urls
	}
	return nil
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
