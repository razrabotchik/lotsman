package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// maxNameBytes is the portable tool-name ceiling confirmed against a real
// desktop client (ADR-0004, FR-15).
const maxNameBytes = 64

// descriptionByteBudget is the per-tool description ceiling (FR-19). No
// config wiring exists yet, so this is the v0 hardcoded default from the
// example config in docs/spec.md (`descriptionBytesPerTool: 1200`).
const descriptionByteBudget = 1200

// Tool is one MCP-facing entry derived from a supported domain.Operation.
type Tool struct {
	Name         string              `json:"name"`
	OperationKey domain.OperationKey `json:"operationKey"`
	Method       string              `json:"method"`
	PathTemplate string              `json:"pathTemplate"`
	Description  string              `json:"description,omitempty"`
}

// Catalog is the deterministic, immutable snapshot built from parsed
// operations (pipeline stage 4).
type Catalog struct {
	SpecDigest string `json:"specDigest"`
	Tools      []Tool `json:"tools"` // deterministic order
	Digest     string `json:"digest"`
}

// Build derives a deterministic tool catalog from parsed operations.
// Only operations at SupportSupported are published (data-model.md
// invariant 2); partially-supported operations wait for a documented
// fallback and opt-in that does not exist yet.
//
// Method-specific policy (e.g. "GET only, for now") is not this package's
// job: catalog.Build publishes every supported operation regardless of
// method, and callers (mcpserver) decide which of those they are prepared
// to serve.
func Build(specDigest string, operations []domain.Operation) Catalog {
	supported := make([]domain.Operation, 0, len(operations))
	for _, op := range operations {
		if op.Support.Level == domain.SupportSupported {
			supported = append(supported, op)
		}
	}
	// operations is already deterministically ordered by the openapi adapter,
	// but re-sorting by Key here makes Catalog's determinism (FR-17) hold
	// independent of caller order.
	sort.Slice(supported, func(i, j int) bool { return supported[i].Key < supported[j].Key })

	seen := make(map[string]domain.OperationKey, len(supported))
	tools := make([]Tool, 0, len(supported))
	for _, op := range supported {
		tools = append(tools, Tool{
			Name:         toolName(op, seen),
			OperationKey: op.Key,
			Method:       op.Method,
			PathTemplate: op.PathTemplate,
			Description:  description(op),
		})
	}

	return Catalog{
		SpecDigest: specDigest,
		Tools:      tools,
		Digest:     digest(tools),
	}
}

// nameCharset is the portable charset a real desktop client accepted
// (ADR-0004, FR-15): anything else is replaced with "_".
var nameCharset = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// toolName implements FR-14–17: operationId (or method+path as fallback),
// sanitized to the portable charset and length, with collisions resolved by
// a stable short hash of OperationKey rather than processing order.
func toolName(op domain.Operation, seen map[string]domain.OperationKey) string {
	base := op.SourceOperationID
	if base == "" {
		base = op.Method + "_" + op.PathTemplate
	}
	name := sanitizeName(base)

	if existing, collides := seen[name]; collides && existing != op.Key {
		name = withCollisionSuffix(name, op.Key)
	}
	seen[name] = op.Key
	return name
}

func sanitizeName(s string) string {
	name := nameCharset.ReplaceAllString(s, "_")
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	if name == "" {
		name = "op"
	}
	return name
}

// withCollisionSuffix appends a short stable hash of key, truncating base so
// the result still fits maxNameBytes (FR-15/FR-16).
func withCollisionSuffix(base string, key domain.OperationKey) string {
	sum := sha256.Sum256([]byte(key))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	if len(base)+len(suffix) > maxNameBytes {
		base = base[:maxNameBytes-len(suffix)]
	}
	return base + suffix
}

// description implements the v0 slice of FR-18/19: summary takes priority
// over description (no config override mechanism exists yet), HTML/control
// characters are stripped, and the result is capped to the per-tool byte
// budget. The source spec is untrusted text that reaches an LLM's context,
// so this is a security control, not cosmetics.
func description(op domain.Operation) string {
	text := op.Summary
	if text == "" {
		text = op.Description
	}
	return budgetBytes(sanitizeText(text), descriptionByteBudget)
}

var controlChars = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

// sanitizeText strips control characters and HTML tags and normalizes
// whitespace. It is not a full HTML sanitizer (no entity decoding, no
// script/style awareness beyond tag stripping) -- sufficient for a v0 that
// keeps spec-authored prose from injecting terminal control sequences or
// markup into a tool description.
func sanitizeText(s string) string {
	s = htmlTag.ReplaceAllString(s, "")
	s = controlChars.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

var htmlTag = regexp.MustCompile(`</?[A-Za-z][^>]*>`)

// budgetBytes truncates s to at most n UTF-8 bytes without splitting a rune.
func budgetBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s)[:n]
	for len(b) > 0 && !isRuneStart(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return string(b)
}

func isRuneStart(c byte) bool { return c&0xC0 != 0x80 }

// digest is a content digest over the published tools: it changes only when
// the catalog changes in a way that matters to a client (FR-17, pipeline.md
// stage 4). Field order is fixed by the Tool struct, so json.Marshal is
// stable across runs (Go's encoding/json is deterministic for struct
// fields).
func digest(tools []Tool) string {
	// Marshal errors are impossible here: Tool has no channel/func/complex
	// fields, so this is intentionally not error-checked.
	b, _ := json.Marshal(tools)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
