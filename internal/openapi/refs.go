package openapi

import (
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// refScan is the result of auditing a document's $ref graph before libopenapi
// resolves it: stage 2 of the pipeline, run as a budget check rather than as a
// resolver. Doing it first means a document that would be expensive or illegal
// to resolve is refused before the expensive resolution starts.
type refScan struct {
	diagnostics []domain.Diagnostic
	maxDepth    int
	documents   int
	bytes       int64
	// version and swagger are read from the root before anything else, so an
	// unsupported document is named in lotsman's terms rather than in the
	// parser's (pipeline.md stage 1.1).
	version string
	swagger string
	// files are the confined file references the parser is allowed to read,
	// relative to the spec root.
	files []string
}

// scanRefs enforces the ref closure budget and refuses every reference that
// leaves the root document.
//
// rootPath is the confinement root a file reference would have to stay inside
// (specsource.Source.RootPath). It is recorded but not yet honoured as an
// allowance: until T025 implements root confinement, symlink escape and cycle
// truncation, every external reference is refused outright -- resolving one
// means reading a file or making a request chosen by an untrusted document
// (Constitution V).
func scanRefs(specBytes []byte, rootPath string, limits Limits) (*refScan, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(specBytes, &root); err != nil {
		// Stage 0 already reported this in operator terms; reaching here means
		// Parse was called with bytes that never went through specsource.
		return nil, errs.Errorf(errs.ClassSpecInvalid, "openapi: document is not valid YAML or JSON: %w", err)
	}

	scanVersion := rootVersion(&root)
	refs := collectRefs(&root)

	// The ref closure is the root document plus every distinct document a
	// reference reaches for. External documents are refused one by one below,
	// but the aggregate is bounded first: a spec that fans out to hundreds of
	// files is refused as a whole, not one diagnostic at a time.
	scan := &refScan{documents: 1, bytes: int64(len(specBytes)), version: scanVersion.openapi, swagger: scanVersion.swagger}

	// File references are followed only inside the directory the document came
	// from, and only for documents that actually exist there; the closure is
	// budgeted as it is walked. Everything else -- remote references, paths
	// that escape the root, a spec read from stdin that has no root at all --
	// is refused by name.
	files, err := resolveClosure(specBytes, rootPath, refs, limits)
	if err != nil {
		return nil, err
	}
	scan.files = files.files
	scan.documents = 1 + len(files.files)
	scan.bytes = files.bytes
	scan.diagnostics = append(scan.diagnostics, files.diagnostics...)
	if rootPath == "" {
		for _, ref := range refs {
			if externalDocument(ref.value) != "" {
				scan.diagnostics = append(scan.diagnostics, diagnostic(domain.ReasonExternalRefUnsupported, ref,
					"reference leaves the document, and a spec read from stdin has no directory to resolve it against"))
			}
		}
	}
	if scan.documents > limits.maxRefDocuments() {
		return nil, errs.Errorf(errs.ClassUnsupported,
			"openapi: $ref closure spans %d documents, limit is %d", scan.documents, limits.maxRefDocuments())
	}
	if scan.bytes > limits.maxRefBytes() {
		return nil, errs.Errorf(errs.ClassUnsupported,
			"openapi: $ref closure is %d bytes, limit is %d", scan.bytes, limits.maxRefBytes())
	}

	depths := make(map[*yaml.Node]int)
	for _, ref := range refs {
		if !strings.HasPrefix(ref.value, "#") {
			continue // handled by the closure walk above
		}
		depth := chainDepth(&root, ref.value, depths, make(map[*yaml.Node]bool))
		if depth > scan.maxDepth {
			scan.maxDepth = depth
		}
	}
	if scan.maxDepth > limits.maxRefDepth() {
		return nil, errs.Errorf(errs.ClassUnsupported,
			"openapi: $ref chain is %d hops deep, limit is %d", scan.maxDepth, limits.maxRefDepth())
	}
	return scan, nil
}

// attachRefDiagnostics moves every refused reference onto the operation it
// occurs in.
//
// Without this an operation reads *more* executable than it is: libopenapi
// drops the parameter whose $ref could not be resolved, so the operation
// enumerates with no parameters at all and therefore with no
// parameters_not_implemented blocker. An operation containing a reference
// lotsman refuses to follow is rejected -- the missing semantics are exactly
// what Principle I says must not be approximated.
//
// The diagnostic stays at document level as well, so serve keeps failing
// closed in both strictness modes (ADR-0005).
func attachRefDiagnostics(operations []domain.Operation, diagnostics []domain.Diagnostic) {
	for i := range operations {
		op := &operations[i]
		scope := operationPointer(op.Method, op.PathTemplate) + "/"
		shared := "#/paths/" + pointerEscape(op.PathTemplate) + "/"
		for _, d := range diagnostics {
			if !refRefusal(d.Code) {
				continue
			}
			// A reference on the path item (its own $ref, or its shared
			// parameters) affects every method under that path.
			if !strings.HasPrefix(d.Pointer, scope) &&
				d.Pointer != shared+"$ref" &&
				!strings.HasPrefix(d.Pointer, shared+"parameters/") {
				continue
			}
			op.Diagnostics = append(op.Diagnostics, d)
			op.Support.Level = domain.SupportRejected
			op.Support.Reasons = appendReason(op.Support.Reasons, d.Code)
		}
	}
}

// refRefusal reports whether a diagnostic code means a reference lotsman
// would not follow. All three are attributed to the operation that contains
// the reference, because the operation is what a reader has to act on.
func refRefusal(code domain.ReasonCode) bool {
	switch code {
	case domain.ReasonExternalRefUnsupported, domain.ReasonRefOutsideRoot, domain.ReasonRefUnresolvable:
		return true
	default:
		return false
	}
}

// operationPointer is the JSON Pointer of one operation in the source
// document; refs.go and the enumerator must agree on it byte for byte.
func operationPointer(method, path string) string {
	return "#/paths/" + pointerEscape(path) + "/" + strings.ToLower(method)
}

func appendReason(reasons []domain.ReasonCode, code domain.ReasonCode) []domain.ReasonCode {
	for _, existing := range reasons {
		if existing == code {
			return reasons
		}
	}
	return append(reasons, code)
}

// externalDocument returns the document part of a reference that leaves the
// root document ("./common.yaml#/Pet" -> "./common.yaml"), or "" for a local
// one. Distinct documents are what the closure budget counts; the fragment
// within a document is not a separate document.
func externalDocument(ref string) string {
	if ref == "" || strings.HasPrefix(ref, "#") {
		return ""
	}
	if hash := strings.Index(ref, "#"); hash >= 0 {
		return ref[:hash]
	}
	return ref
}

// documentVersion is the version declaration read straight from the root
// mapping, before any model is built.
type documentVersion struct {
	openapi string
	swagger string
}

// rootVersion reads the `openapi` and `swagger` keys from the root mapping.
// Deciding the version first is what lets lotsman say "this is Swagger 2.0,
// which I do not translate" instead of passing along a parser error about a
// method the operator has never heard of.
func rootVersion(root *yaml.Node) documentVersion {
	node := root
	for node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return documentVersion{}
	}
	var out documentVersion
	if value := mappingValue(node, "openapi"); value != nil {
		out.openapi = value.Value
	}
	if value := mappingValue(node, "swagger"); value != nil {
		out.swagger = value.Value
	}
	return out
}

type refSite struct {
	pointer string
	value   string
	node    *yaml.Node
}

// collectRefs finds every `$ref: <scalar>` in the tree, recording the JSON
// Pointer of the site so a diagnostic can point back at the source document.
func collectRefs(root *yaml.Node) []refSite {
	var out []refSite
	var walk func(n *yaml.Node, pointer string)
	walk = func(n *yaml.Node, pointer string) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, child := range n.Content {
				walk(child, pointer)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				child := pointer + "/" + pointerEscape(key.Value)
				if key.Value == "$ref" && value.Kind == yaml.ScalarNode {
					out = append(out, refSite{pointer: child, value: value.Value, node: value})
					continue
				}
				walk(value, child)
			}
		case yaml.SequenceNode:
			for i, child := range n.Content {
				walk(child, pointer+"/"+strconv.Itoa(i))
			}
		}
		// Alias nodes are not followed: their anchor is walked where it is
		// defined, and specsource already bounded how far aliasing can reach.
	}
	walk(root, "#")
	return out
}

// chainDepth returns how many $ref hops resolving ref takes, counting the hop
// into ref itself. A reference whose target cannot be resolved counts as one
// hop: libopenapi reports the unresolvable reference itself.
//
// A cycle is cut rather than reported: truncation policy and its
// cyclic_schema_truncated diagnostic belong to T025, and libopenapi already
// fails the build on a circular reference. Cutting here only keeps the budget
// check terminating.
func chainDepth(root *yaml.Node, ref string, memo map[*yaml.Node]int, visiting map[*yaml.Node]bool) int {
	target := resolvePointer(root, ref)
	if target == nil {
		return 1
	}
	if depth, ok := memo[target]; ok {
		return depth + 1
	}
	if visiting[target] {
		return 1
	}
	visiting[target] = true
	defer delete(visiting, target)

	deepest := 0
	for _, inner := range collectRefs(target) {
		if !strings.HasPrefix(inner.value, "#") {
			continue
		}
		if depth := chainDepth(root, inner.value, memo, visiting); depth > deepest {
			deepest = depth
		}
	}
	memo[target] = deepest
	return deepest + 1
}

// resolvePointer walks a local "#/a/b/0" JSON Pointer through the node tree.
// It returns nil for anything it cannot follow; an unresolvable reference is
// libopenapi's error to report, not the budget's.
func resolvePointer(root *yaml.Node, ref string) *yaml.Node {
	if !strings.HasPrefix(ref, "#") {
		return nil
	}
	node := root
	for node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}

	path := strings.TrimPrefix(ref, "#")
	if path == "" || path == "/" {
		return node
	}
	for _, raw := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if node == nil {
			return nil
		}
		segment := pointerUnescape(raw)
		switch node.Kind {
		case yaml.MappingNode:
			node = mappingValue(node, segment)
		case yaml.SequenceNode:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node.Content) {
				return nil
			}
			node = node.Content[index]
		default:
			return nil
		}
	}
	return node
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

var (
	pointerEscaper   = strings.NewReplacer("~", "~0", "/", "~1")
	pointerUnescaper = strings.NewReplacer("~1", "/", "~0", "~")
)

func pointerEscape(s string) string   { return pointerEscaper.Replace(s) }
func pointerUnescape(s string) string { return pointerUnescaper.Replace(s) }
