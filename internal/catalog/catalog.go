package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// maxNameBytes is the portable tool-name ceiling selected for the M0 client
// compatibility matrix (ADR-0004, FR-15); broad desktop confirmation is still pending.
const maxNameBytes = 64

// descriptionByteBudget is the per-tool description ceiling (FR-19). No
// config wiring exists yet, so this is the v0 hardcoded default from the
// example config in docs/spec.md (`descriptionBytesPerTool: 1200`).
const descriptionByteBudget = 1200

// DefaultDescriptionBytesPerTool is that ceiling under the name the example
// configuration gives it (docs/spec.md §5.1).
const DefaultDescriptionBytesPerTool = descriptionByteBudget

// Tool is one MCP-facing entry derived from a supported domain.Operation.
type Tool struct {
	Name         string              `json:"name"`
	OperationKey domain.OperationKey `json:"operationKey"`
	Method       string              `json:"method"`
	PathTemplate string              `json:"pathTemplate"`
	Description  string              `json:"description,omitempty"`
	// Tags carry the document's grouping vocabulary, sanitized like every
	// other piece of spec-authored text that reaches a model.
	Tags []string `json:"tags,omitempty"`
	// Servers is the operation's effective server URL list (v0: no
	// variable substitution), passed through for request execution.
	Servers []string `json:"servers,omitempty"`
	// Input is the normalized argument surface the request builder serializes
	// from; InputSchema is the published JSON Schema the model sees. They are
	// two views of the same thing and must never disagree: one validates, the
	// other writes the wire.
	Input       domain.InputModel `json:"input"`
	InputSchema map[string]any    `json:"inputSchema"`
	// Effect drives the published annotations and the runtime gate. It is
	// carried on the tool so a report can show what was decided and why.
	Effect domain.EffectDecision `json:"effect"`
	// AuthProfiles names the credentials this tool presents. Names and secret
	// references are safe to publish; values never appear anywhere.
	AuthProfiles []string `json:"authProfiles,omitempty"`
	// AuthBinding is how those credentials are applied at call time. It is not
	// serialized: a report is about what lotsman will do, not how it holds it.
	AuthBinding auth.Binding `json:"-"`
	// Executable means every axis agrees: the operation is supported, the
	// runtime can build the call, and policy permits it.
	Executable bool `json:"executable"`
	// ExecutionBlockers are lotsman's own gaps; PolicyBlockers are the
	// operator's decisions. They are reported separately because they call
	// for different actions: wait for a release, or change the configuration.
	ExecutionBlockers []domain.ReasonCode `json:"executionBlockers,omitempty"`
	PolicyBlockers    []domain.ReasonCode `json:"policyBlockers,omitempty"`
	PolicyMessage     string              `json:"policyMessage,omitempty"`
	// AuthMessage says why no configured credential satisfies this operation,
	// in the document's own terms -- "the API carries the key in the query",
	// not "authentication_not_implemented". The code says which category the
	// problem is in; this says which line of the configuration to change, and
	// the two are different questions. It used to be computed and dropped here.
	AuthMessage string `json:"authMessage,omitempty"`
}

// Catalog is the deterministic, immutable snapshot built from parsed
// operations (pipeline stage 4).
type Catalog struct {
	SpecDigest string `json:"specDigest"`
	Tools      []Tool `json:"tools"` // deterministic order
	Digest     string `json:"digest"`
	// Mode is how this catalog is published. Until search mode exists, an
	// `auto` catalog over the budget still publishes tools -- and says so in
	// the report rather than quietly serving something nobody asked for.
	Mode Mode `json:"mode"`
	// Report accounts for every operation in the document, including the ones
	// that never became tools. It is derived from the same pass, so it can
	// never disagree with what was published.
	Report Report `json:"report"`
}

// Mode is how a catalog is published: one tool per operation, or a handful of
// meta-tools over a searchable index (FR-47/48).
type Mode string

// Publication modes. ModeAuto is a request, not a result: it resolves to one
// of the other two against the measured catalog size.
const (
	ModeTools  Mode = "tools"
	ModeSearch Mode = "search"
	ModeAuto   Mode = "auto"
)

// DefaultMaxSerializedBytes is the catalog budget from the example
// configuration in docs/spec.md 5.1 (`catalog.maxSerializedBytes`). It is a
// context budget rather than a transport one: stdio delivers 4 MB in 146 ms,
// and a model's window is what actually runs out (docs/benchmarks.md).
const DefaultMaxSerializedBytes = 120_000

// Options configures catalog derivation. The zero value is the documented
// default: read-only execution with no credentials, tools mode chosen
// automatically (docs/spec.md 5.1).
type Options struct {
	// Mode requests a publication mode. The empty value means ModeAuto.
	Mode Mode
	// MaxSerializedBytes is the catalog budget auto mode decides against.
	MaxSerializedBytes int
	// DescriptionBytesPerTool is the per-tool description ceiling (FR-19).
	// Zero means the documented default, in this one place, so that nothing
	// downstream has to decide what an unset budget means -- which is how a
	// budget turns into no budget.
	DescriptionBytesPerTool int

	Policy policy.Config
	// Auth holds the configured credential profiles. Which operations can be
	// authenticated is a fact about the configuration, not about the
	// document, which is why it is decided here rather than in the adapter.
	Auth auth.Profiles

	// IncludeTags publishes only the operations carrying at least one of
	// these tags (docs/spec.md 5.1). Empty publishes everything.
	IncludeTags []string
	// Overrides are the operator's per-operation statements. Validate them
	// against the document with ValidateOverrides before building: Build
	// ignores one that matches nothing, and an unnoticed override is a
	// control the operator believes is in force.
	Overrides []config.OperationOverride
}

// mode is the requested mode, defaulting to auto.
//
//nolint:gocritic // hugeParam: Options is configuration read once per build; a pointer would let a callee change what the report describes.
func (o Options) mode() Mode {
	if o.Mode == "" {
		return ModeAuto
	}
	return o.Mode
}

//nolint:gocritic // hugeParam: Options is configuration read once per build; a pointer would let a callee change what the report describes.
func (o Options) maxSerializedBytes() int {
	if o.MaxSerializedBytes <= 0 {
		return DefaultMaxSerializedBytes
	}
	return o.MaxSerializedBytes
}

//nolint:gocritic // hugeParam: Options is configuration read once per build; a pointer would let a callee change what the report describes.
func (o Options) descriptionBytesPerTool() int {
	if o.DescriptionBytesPerTool <= 0 {
		return DefaultDescriptionBytesPerTool
	}
	return o.DescriptionBytesPerTool
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
//
//nolint:gocritic // hugeParam: Options is configuration read once per build; a pointer would let a callee change what the report describes.
func Build(specDigest string, operations []domain.Operation, opts Options) Catalog {
	// The overlay runs first and on a copy: everything downstream -- tools,
	// policy verdicts and the report alike -- must see one set of operations,
	// the operator's, or the report would describe a catalog nobody served.
	overlaid := Overlay(operations, opts)
	operations = overlaid.Operations

	supported := make([]domain.Operation, 0, len(operations))
	for i := range operations {
		if operations[i].Support.Level == domain.SupportSupported {
			supported = append(supported, operations[i])
		}
	}
	// operations is already deterministically ordered by the openapi adapter,
	// but re-sorting by Key here makes Catalog's determinism (FR-17) hold
	// independent of caller order.
	sort.Slice(supported, func(i, j int) bool { return supported[i].Key < supported[j].Key })

	seen := make(map[string]domain.OperationKey, len(supported))
	tools := make([]Tool, 0, len(supported))
	for i := range supported {
		op := &supported[i]

		// Selection is decided before policy, and removes rather than
		// refuses: `enabled: false` and includeTags say "this is not part of
		// the surface", which is a different statement from "you may not call
		// this" and deserves a different outcome.
		if _, gone := overlaid.Excluded[op.Key]; gone {
			continue
		}

		verdict := opts.Policy.Evaluate(policy.SubjectOf(op))
		profiles := opts.Auth
		if pinned := overlaid.authProfiles[op.Key]; pinned != "" {
			profiles = profiles.Only(pinned)
		}
		credentials := auth.Select(op.Security, profiles)

		tool := Tool{
			Name:              toolName(op, seen),
			OperationKey:      op.Key,
			Method:            op.Method,
			PathTemplate:      op.PathTemplate,
			Description:       description(op, opts.descriptionBytesPerTool()),
			Tags:              sanitizeTags(op.Tags),
			Servers:           op.Servers,
			Input:             op.Input,
			InputSchema:       inputSchema(op.Input),
			Effect:            op.Effect,
			Executable:        op.Executable() && verdict.Allowed && credentials.Bound(),
			ExecutionBlockers: append([]domain.ReasonCode(nil), op.ExecutionBlockers...),
		}
		if !credentials.Bound() {
			// Published, never executable: the operation is translatable and
			// permitted, but lotsman has no way to authenticate it.
			tool.ExecutionBlockers = append(tool.ExecutionBlockers, credentials.Reason)
			tool.AuthMessage = credentials.Message
			tool.Executable = false
		} else {
			tool.AuthBinding = credentials.Binding
			for i := range credentials.Binding.Credentials {
				tool.AuthProfiles = append(tool.AuthProfiles, credentials.Binding.Credentials[i].Name)
			}
		}
		if !verdict.Allowed {
			tool.PolicyBlockers = []domain.ReasonCode{verdict.Reason}
			tool.PolicyMessage = verdict.Message
		}
		tools = append(tools, tool)
	}

	// The mode is resolved before the digest, because it is part of what the
	// digest identifies. `auto` follows the measurement; an explicit mode is
	// obeyed even when the measurement disagrees, and the report says both so
	// a pinned choice is visible rather than silent.
	recommended := recommendedMode(tools, opts)
	mode := recommended
	if requested := opts.mode(); requested != ModeAuto {
		mode = requested
	}

	catalogDigest := digest(mode, tools)
	built := Catalog{
		SpecDigest: specDigest,
		Tools:      tools,
		Digest:     catalogDigest,
		Mode:       mode,
		Report:     buildReport(operations, tools, overlaid.Excluded, catalogDigest, mode, opts),
	}
	return built
}

// recommendedMode is what the measurement says this catalog needs.
//
//nolint:gocritic // hugeParam: Options is configuration read once per build; a pointer would let a callee change what the report describes.
func recommendedMode(tools []Tool, opts Options) Mode {
	if serializedBytes(tools) > opts.maxSerializedBytes() {
		return ModeSearch
	}
	return ModeTools
}

// nameCharset is the portable charset a real desktop client accepted
// (ADR-0004, FR-15): anything else is replaced with "_".
var nameCharset = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// toolName implements FR-14–17: operationId (or method+path as fallback),
// sanitized to the portable charset and length, with collisions resolved by
// a stable short hash of OperationKey rather than processing order.
func toolName(op *domain.Operation, seen map[string]domain.OperationKey) string {
	base := op.SourceOperationID
	if base == "" {
		base = fallbackName(op.Method, op.PathTemplate)
	}
	name := sanitizeName(base)

	if existing, collides := seen[name]; collides && existing != op.Key {
		name = withCollisionSuffix(name, op.Key)
	}
	seen[name] = op.Key
	return name
}

var (
	camelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronymBoundary = regexp.MustCompile(`([A-Z])([A-Z][a-z])`)
)

func sanitizeName(s string) string {
	s = acronymBoundary.ReplaceAllString(s, `${1}_${2}`)
	s = camelBoundary.ReplaceAllString(s, `${1}_${2}`)
	s = strings.ToLower(s)
	name := nameCharset.ReplaceAllString(s, "_")
	name = strings.Trim(name, "_")
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	if name == "" {
		name = "op"
	}
	return name
}

func fallbackName(method, pathTemplate string) string {
	parts := []string{strings.ToLower(method)}
	for _, segment := range strings.Split(pathTemplate, "/") {
		if segment == "" || (strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")) {
			continue
		}
		parts = append(parts, segment)
	}
	return strings.Join(parts, "_")
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
func description(op *domain.Operation, budget int) string {
	text := op.Summary
	if text == "" {
		text = op.Description
	}
	return budgetBytes(sanitizeText(text), budget)
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
	s = strings.ToValidUTF8(s, "")
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// digest is a content digest over the published tools: it changes only when
// the catalog changes in a way that matters to a client (FR-17, pipeline.md
// stage 4). Field order is fixed by the Tool struct, so json.Marshal is
// stable across runs (Go's encoding/json is deterministic for struct
// fields).
// digest identifies what a client will be shown, which is the tool set *and*
// the mode it is shown in.
//
// The mode belongs in it because `tools/list` differs entirely between the
// two -- one tool per operation, or five meta-tools over the same catalog. A
// digest that ignored the mode would let FR-74's cache hint point a client at
// a tool list that no longer exists, and would let a reload that changed only
// the mode decide there was nothing to publish.
func digest(mode Mode, tools []Tool) string {
	// Marshal errors are impossible here: Tool has no channel/func/complex
	// fields, so this is intentionally not error-checked.
	b, _ := json.Marshal(struct {
		Mode  Mode   `json:"mode"`
		Tools []Tool `json:"tools"`
	}{Mode: mode, Tools: tools})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// maxTags bounds how many tags one tool republishes. A document that attaches
// forty tags to an operation is describing its own taxonomy, not helping a
// model choose, and every one of them costs context.
const maxTags = 8

// maxPathDisplayBytes bounds a path template shown to a person or a model.
// No API has a longer one, and the string is untrusted.
const maxPathDisplayBytes = 256

// SafePath is the path template as it may be shown to a person or a model.
//
// PathTemplate is what a request is built from and stays exactly as the
// document wrote it; there is no room to clean a string that has to match an
// API. This is the same string cleaned and bounded for the places that only
// *read* it: a refusal returned to a model, an approval prompt a person is
// shown, a search result. Those are rendered where a description is rendered,
// and a document able to put its own line into one of them has the injection
// surface Constitution V is about.
func (t *Tool) SafePath() string {
	return budgetBytes(sanitizeText(t.PathTemplate), maxPathDisplayBytes)
}

// sanitizeTags cleans and bounds the document's tags. They are untrusted text
// on their way to an LLM exactly like a description is, and they are also a
// filter vocabulary, so duplicates and empties are dropped rather than carried.
func sanitizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		clean := budgetBytes(sanitizeText(tag), maxTagBytes)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
		if len(out) == maxTags {
			break
		}
	}
	return out
}

// maxTagBytes bounds one tag. A tag is a label; anything longer is prose that
// belongs in the description.
const maxTagBytes = 64
