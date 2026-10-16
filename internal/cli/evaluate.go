package cli

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

// pipelineInputs are what the command line fixed for the life of a
// workbench: which configuration file the draft stands for, and the document
// when the command line names one.
type pipelineInputs struct {
	configPath string
	spec       string
	root       string
}

// newPipeline hands the workbench the pipeline serve runs. Nothing here
// resolves a secret or makes a request: the catalog is built, never served.
func newPipeline(in pipelineInputs) workbench.Pipeline {
	return workbench.Pipeline{
		Evaluate: func(ctx context.Context, data []byte) (*workbench.Snapshot, error) {
			snap, _, _ := evaluate(ctx, data, in)
			return snap, ctx.Err()
		},
		Explain: func(ctx context.Context, data []byte, key string, arguments []byte) (string, error) {
			snap, cat, runtime := evaluate(ctx, data, in)
			if cat == nil {
				return "", errs.Errorf(errs.ClassUsage, "nothing to explain: %s", firstNonEmpty(snap.SpecError, "no catalog"))
			}
			tool := findTool(cat, key)
			if tool == nil {
				return "", errs.Errorf(errs.ClassUnsupported, "%s is not published under this configuration", key)
			}
			args, err := decodeArguments(arguments)
			if err != nil {
				return "", err
			}
			var out bytes.Buffer
			explain(&out, tool, args, runtime)
			return out.String(), nil
		},
	}
}

var quiet = slog.New(slog.DiscardHandler)

// evaluate runs stages 0-4 over a configuration's bytes and turns every
// problem into data. It returns the catalog too, for Explain.
func evaluate(ctx context.Context, data []byte, in pipelineInputs) (*workbench.Snapshot, *catalog.Catalog, config.Runtime) {
	snap := &workbench.Snapshot{}
	var file *config.File
	if len(bytes.TrimSpace(data)) > 0 {
		loaded, err := config.LoadBytes(data, in.configPath)
		if err != nil {
			snap.ConfigError = err.Error()
		} else {
			file = loaded
		}
	}
	runtime := config.Resolve(file, config.Environment{}, config.Overrides{})
	if in.root != "" {
		runtime.Root = in.root
	}
	snap.Overlays = writtenOverlays(data, &runtime)
	snap.Components = append([]config.ComponentOverride(nil), runtime.ComponentOverrides...)
	for _, name := range sortedProfileNames(runtime.AuthProfiles) {
		snap.Profiles = append(snap.Profiles, workbench.Profile{Name: name, Scheme: string(runtime.AuthProfiles[name].Scheme)})
	}

	spec := pick(in.spec, runtime.Source)
	if spec == "" {
		snap.SpecError = "no specification: pass SPEC or set spec.source in the configuration"
		return snap, nil, runtime
	}
	if err := checkOverlayFiles(runtime.Overlays); err != nil {
		snap.SpecError = err.Error()
		return snap, nil, runtime
	}
	doc, err := loadDocument(ctx, spec, runtime, quiet)
	if err != nil {
		snap.SpecError = err.Error()
		return snap, nil, runtime
	}
	for _, p := range doc.CorrectionProblems {
		snap.Problems = append(snap.Problems, problemFrom(p.Section, p.Index, p.Message, &runtime))
	}

	opts, err := catalogOptions(runtime, catalog.ModeTools, doc.Operations)
	if err != nil {
		// An override that matches nothing (or several): reported, and the
		// catalog is built without the overrides so the tree still shows.
		snap.Problems = append(snap.Problems, workbench.Problem{Section: "operationOverrides", Index: -1, Message: err.Error()})
		stripped := runtime
		stripped.Overrides = nil
		opts, _ = catalogOptions(stripped, catalog.ModeTools, doc.Operations)
	}
	cat := catalog.Build(doc.digest, doc.Operations, opts)
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		if len(tool.Corrections) == 0 {
			continue
		}
		if _, err := argvalidate.Compile(tool.Name, tool.InputSchema); err != nil {
			snap.Problems = append(snap.Problems, workbench.Problem{Section: "operationOverrides", Index: -1,
				Message: fmt.Sprintf("the corrected input schema of %s does not compile: %v", tool.OperationKey, err)})
		}
	}

	snap.Operations = snapshotOperations(doc.Operations, &cat, &runtime)
	return snap, &cat, runtime
}

// maxOverlayBytes bounds an overlay the workbench reads: an overlay is a
// page of corrections, not a document.
const maxOverlayBytes = 4 << 20

// checkOverlayFiles refuses, before anything reads them, overlays that are
// not regular files or are too large: a path typed in the browser could name
// a FIFO or a device, and reading one would block the workbench.
func checkOverlayFiles(paths []string) error {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue // readOverlays says it cannot read it
		}
		if !info.Mode().IsRegular() {
			return errs.Errorf(errs.ClassUsage, "config: spec.overlays: %s is not a regular file", path)
		}
		if info.Size() > maxOverlayBytes {
			return errs.Errorf(errs.ClassUsage, "config: spec.overlays: %s is larger than %d MiB", path, maxOverlayBytes>>20)
		}
	}
	return nil
}

// writtenOverlays are the overlay paths as the configuration file spells
// them, so the landing panel hands them back unchanged when it edits the
// list. When the file cannot be read that way, the resolved paths are said
// relative to the configuration's directory when they lie under it.
func writtenOverlays(data []byte, runtime *config.Runtime) []string {
	if file, err := config.Parse(data); err == nil && len(file.Spec.Overlays) == len(runtime.Overlays) {
		return append([]string(nil), file.Spec.Overlays...)
	}
	out := make([]string, 0, len(runtime.Overlays))
	for _, path := range runtime.Overlays {
		if runtime.ConfigDir != "" {
			if rel, err := filepath.Rel(runtime.ConfigDir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				path = filepath.ToSlash(rel)
			}
		}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func snapshotOperations(ops []domain.Operation, cat *catalog.Catalog, runtime *config.Runtime) []workbench.Operation {
	verdicts := map[domain.OperationKey]*catalog.OperationVerdict{}
	for i := range cat.Report.Operations {
		verdicts[cat.Report.Operations[i].Key] = &cat.Report.Operations[i]
	}
	tools := map[domain.OperationKey]*catalog.Tool{}
	for i := range cat.Tools {
		tools[cat.Tools[i].OperationKey] = &cat.Tools[i]
	}
	out := make([]workbench.Operation, 0, len(ops))
	for i := range ops {
		op := &ops[i]
		v := verdicts[op.Key]
		w := workbench.Operation{
			Key: string(op.Key), Method: op.Method, Path: op.PathTemplate, OperationID: op.SourceOperationID,
			Summary: op.Summary, Tags: op.Tags, Support: string(op.Support.Level),
			Effect: string(op.Effect.Effect), EffectSource: string(op.Effect.Source),
			MediaTypes: op.RequestMediaTypes, BodyComponent: op.BodyComponent, Corrections: op.Corrections,
		}
		if v != nil {
			w.Support, w.Published, w.Excluded, w.Executable = string(v.Support), v.Published, v.Excluded, v.Executable
			w.Effect, w.EffectSource = string(v.Effect.Effect), string(v.Effect.Source)
			w.AuthProfiles = v.AuthProfiles
			for _, r := range v.Reasons {
				w.Reasons = append(w.Reasons, workbench.Reason{Code: string(r.Code), Detail: r.Detail, Pointer: r.Pointer})
			}
			for _, b := range append(append([]domain.ReasonCode(nil), v.ExecutionBlockers...), v.PolicyBlockers...) {
				w.Blockers = append(w.Blockers, string(b))
			}
		}
		published := map[string]domain.Schema{}
		for _, p := range op.Input.Parameters {
			published[string(p.In)+"/"+p.Name] = p.Schema
		}
		for _, d := range op.DeclaredParameters {
			w.Parameters = append(w.Parameters, workbench.Parameter{DeclaredParameter: d,
				Schema: published[string(d.In)+"/"+d.Name]})
		}
		if op.Input.Body != nil {
			w.Body = &workbench.Body{MediaType: op.Input.Body.MediaType, Required: op.Input.Body.Required,
				Schema: op.Input.Body.Schema}
		}
		if tool := tools[op.Key]; tool != nil {
			w.InputSchema = tool.InputSchema
		}
		w.Override = overrideFor(op, runtime.Overrides)
		workbench.Classify(&w)
		out = append(out, w)
	}
	return out
}

// overrideFor finds the operationOverrides entry naming op, matched the way
// the catalog matches.
func overrideFor(op *domain.Operation, overrides []config.OperationOverride) *config.OperationOverride {
	for i := range overrides {
		m := overrides[i].Match
		if (m.OperationID != "" && m.OperationID == op.SourceOperationID) ||
			(m.OperationID == "" && strings.EqualFold(m.Method, op.Method) && m.Path == op.PathTemplate) {
			entry := overrides[i]
			return &entry
		}
	}
	return nil
}

func problemFrom(section string, index int, message string, runtime *config.Runtime) workbench.Problem {
	p := workbench.Problem{Section: section, Index: index, Message: message}
	switch {
	case section == "operationOverrides" && index >= 0 && index < len(runtime.Overrides):
		m := runtime.Overrides[index].Match
		p.Match = &m
	case section == "componentOverrides" && index >= 0 && index < len(runtime.ComponentOverrides):
		p.Schema = runtime.ComponentOverrides[index].Match.Schema
	}
	return p
}

func sortedProfileNames(profiles map[string]config.Profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
