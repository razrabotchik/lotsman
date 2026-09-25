package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// OperationKey is the stable identity of an operation: independent of
// operationId, because real specs duplicate or omit it.
type OperationKey string

// NewOperationKey builds the canonical "{namespace}:{METHOD}:{normalizedPath}"
// key (data-model.md). Method is upper-cased so callers do not have to agree
// on a case convention.
func NewOperationKey(namespace, method, pathTemplate string) OperationKey {
	if namespace == "" {
		namespace = "default"
	}
	return OperationKey(namespace + ":" + strings.ToUpper(method) + ":" + pathTemplate)
}

// Namespace is the first field of the key: which API this operation belongs
// to. There is one namespace today ("default"), and rules match on it anyway
// -- a policy written against a single-API deployment should keep meaning the
// same thing when a second API arrives.
func (k OperationKey) Namespace() string {
	namespace, _, found := strings.Cut(string(k), ":")
	if !found {
		return ""
	}
	return namespace
}

// Operation is the enumeration-stage IR: identity, method, path template and
// diagnostics. Later pipeline stages add inputs, security, effect and doc
// metadata (data-model.md); this is deliberately the minimal slice needed by
// Step 2 (operations enumeration).
type Operation struct {
	Key OperationKey `json:"key"`
	// SourceOperationID is the operationId as authored, kept for diagnostics
	// only -- it is never part of Key because it is unreliable in real specs.
	SourceOperationID string `json:"sourceOperationId,omitempty"`
	Method            string `json:"method"`
	PathTemplate      string `json:"pathTemplate"`
	// Summary and Description are copied from the source document as
	// authored -- untrusted text (FR-18): a later stage sanitizes and
	// budgets them before they reach a tool description.
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	// Tags are the document's own grouping vocabulary, kept as authored
	// (untrusted text). They are what a model filters and browses by when a
	// catalog is too large to read (FR-48's list_tags).
	Tags []string `json:"tags,omitempty"`
	// Servers is the effective server URL list after operation->path->root
	// inheritance (first non-empty level wins). Server variables are not
	// substituted yet -- a v0 limitation shared with requestbuild.
	Servers []string `json:"servers,omitempty"`
	// Effect is what calling this operation does to the outside world. It is
	// the only thing the read-only gate consults: annotations are hints to a
	// client, never an input to policy (data-model.md invariant 4).
	Effect EffectDecision `json:"effect"`
	// Security is the effective security after operation->root inheritance:
	// alternatives are OR, requirements inside one alternative are AND
	// (FR-55/56). An empty slice means the operation is public, which an
	// explicit `security: []` on the operation is entitled to say.
	Security []SecurityAlternative `json:"security,omitempty"`
	// Input is the operation's argument surface after parameter merging and
	// normalization; the tool's grouped input schema is derived from it.
	Input   InputModel    `json:"input"`
	Support SupportStatus `json:"support"`
	// ExecutionBlockers are temporary/runtime-capability reasons why an
	// otherwise supported operation must not reach the network in this build.
	// Publishing a discoverable tool is not permission to approximate a call.
	ExecutionBlockers []ReasonCode `json:"executionBlockers,omitempty"`
	Diagnostics       []Diagnostic `json:"diagnostics,omitempty"`
}

// Effect classifies what an operation does to the outside world (spec 4.7).
type Effect string

// Effect classes.
const (
	// EffectRead must not change external state.
	EffectRead Effect = "read"
	// EffectWrite changes state without being marked destructive.
	EffectWrite Effect = "write"
	// EffectDestructive deletes, sends, publishes, charges, or is otherwise irreversible.
	EffectDestructive Effect = "destructive"
	// EffectUnknown means the effect could not be determined reliably -- which is a refusal, not a shrug.
	EffectUnknown Effect = "unknown"
)

// EffectSource records where an effect decision came from.
type EffectSource string

// Effect sources.
const (
	EffectSourceHTTPMethod    EffectSource = "http_method"
	EffectSourceRecipe        EffectSource = "recipe"
	EffectSourceLocalOverride EffectSource = "local_override"
)

// EffectConfidence separates a heuristic from a statement.
type EffectConfidence string

// Effect confidences.
const (
	// ConfidenceInferred means lotsman worked it out; only a reviewed override raises it.
	ConfidenceInferred EffectConfidence = "inferred"
	// ConfidenceExplicit means a human or a recipe stated it.
	ConfidenceExplicit EffectConfidence = "explicit"
)

// EffectDecision carries the verdict with its provenance, so a report can
// show not just what lotsman decided but on what basis (spec 4.7).
type EffectDecision struct {
	Effect     Effect           `json:"effect"`
	Source     EffectSource     `json:"source"`
	Confidence EffectConfidence `json:"confidence"`
	// Warnings explain a decision a reader would otherwise find surprising,
	// such as a GET raised to unknown by the suspicious-verb scanner.
	Warnings []string `json:"warnings,omitempty"`
}

// IsRead reports whether the operation is safe for a read-only runtime. It is
// deliberately not "!= write": unknown is not read.
func (d EffectDecision) IsRead() bool { return d.Effect == EffectRead }

// SecurityAlternative is one way to authenticate a call. Every requirement
// inside it must be satisfied together (AND); satisfying any one alternative
// is enough (OR).
type SecurityAlternative struct {
	Requirements []SecurityRequirement `json:"requirements"`
}

// SecurityRequirement names a scheme and the scopes the operation asks for.
type SecurityRequirement struct {
	Scheme string   `json:"scheme"`
	Type   string   `json:"type,omitempty"`       // apiKey | http | oauth2 | openIdConnect | mutualTLS
	In     string   `json:"in,omitempty"`         // apiKey: header | query | cookie
	Name   string   `json:"name,omitempty"`       // apiKey: the header/query/cookie name
	HTTP   string   `json:"httpScheme,omitempty"` // http: basic | bearer | ...
	Scopes []string `json:"scopes,omitempty"`
	// Flows are the OAuth2 flows the scheme declares, in document order.
	// Which flows a scheme offers decides whether lotsman can satisfy it at
	// all: `clientCredentials` is a credential a process can obtain on its
	// own, and `authorizationCode` is one that needs a person.
	Flows []string `json:"flows,omitempty"`
	// Satisfiable reports whether lotsman has a provider that could ever meet
	// this requirement. An undefined scheme cannot be satisfied at all, and
	// neither can an OAuth2 scheme offering only flows that need a human
	// (FR-58, FR-64: delegated mode is M4b).
	Satisfiable bool `json:"satisfiable"`
}

// Satisfiable reports whether every requirement in the alternative is one
// lotsman could meet with an auth profile. An alternative with no
// requirements is the OAS way of saying "no authentication", which is always
// satisfiable.
func (a SecurityAlternative) Satisfiable() bool {
	for i := range a.Requirements {
		if !a.Requirements[i].Satisfiable {
			return false
		}
	}
	return true
}

// Severity classifies a Diagnostic.
type Severity string

// Diagnostic severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// RefDocument is one document a specification pulls in through a `$ref`,
// with the digest of the bytes lotsman actually read (FR-13b).
//
// It exists because a root document's digest answers a different question than
// the one an operator asks. For an exploded specification -- a small index over
// hundreds of files, which is how large vendors publish -- editing a referenced
// document leaves the root byte-identical, so `specDigest` alone reports two
// different specifications as the same one.
type RefDocument struct {
	// Path is relative to the spec root, slash-separated, so the manifest is
	// the same on every platform and says nothing about the host.
	Path string `json:"path"`
	// Digest is "sha256:<hex>" of the document's bytes.
	Digest string `json:"digest"`
}

// ManifestDigest identifies a specification and everything it read.
//
// The root is named "." rather than by filename: the manifest answers "are
// these the same bytes", and a document that was moved or renamed on the way in
// is the same specification. Referenced documents keep their paths, because a
// `$ref` resolving to a different file *is* a different specification.
func ManifestDigest(rootDigest string, references []RefDocument) string {
	var canonical strings.Builder
	fmt.Fprintf(&canonical, ".\x00%s\n", rootDigest)
	// The caller sorts; sorting again here makes the digest independent of that
	// promise, because a digest that depends on caller order is not an identity.
	sorted := append([]RefDocument(nil), references...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for i := range sorted {
		fmt.Fprintf(&canonical, "%s\x00%s\n", sorted[i].Path, sorted[i].Digest)
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReasonCode is a machine-readable diagnostic or rejection code
// (data-model.md), stable across releases so consumers can branch on it.
type ReasonCode string

const (
	// ReasonPathParameterMismatch marks a path template placeholder without a matching required path parameter.
	ReasonPathParameterMismatch ReasonCode = "path_parameter_mismatch"
	// ReasonInvalidPath marks a path template that cannot be requested as written.
	ReasonInvalidPath ReasonCode = "invalid_path"
	// ReasonParametersNotImplemented blocks execution until the serialization for a parameter location is implemented (cookies, as of now).
	ReasonParametersNotImplemented ReasonCode = "parameters_not_implemented"
	// ReasonAuthenticationNotImplemented blocks execution of an operation that requires unresolved authentication.
	ReasonAuthenticationNotImplemented ReasonCode = "authentication_not_implemented"
	// ReasonExternalRefUnsupported marks a $ref that leaves the root document: resolving it would read a file or fetch a URL chosen by an untrusted spec.
	ReasonExternalRefUnsupported ReasonCode = "external_ref_unsupported"
	// ReasonRefOutsideRoot marks a file reference that leaves the directory the document was loaded from.
	ReasonRefOutsideRoot ReasonCode = "ref_outside_root"
	// ReasonRefUnresolvable marks a file reference inside the root that could not be read or parsed.
	ReasonRefUnresolvable ReasonCode = "ref_unresolvable"
	// ReasonUnsupportedParameterStyle marks a style/location combination lotsman will not serialize (docs/spec.md 4.5).
	ReasonUnsupportedParameterStyle ReasonCode = "unsupported_parameter_style"
	// ReasonUnsupportedParameterSchema marks a parameter schema outside the scalar/array-of-scalars shape a path or query value can carry.
	ReasonUnsupportedParameterSchema ReasonCode = "unsupported_parameter_schema"
	// ReasonUnsupportedMediaType marks a content-typed parameter or body media type lotsman does not translate.
	ReasonUnsupportedMediaType ReasonCode = "unsupported_media_type"
	// ReasonInvalidParameter marks a structurally invalid parameter: no name, an unknown location, or neither schema nor content.
	ReasonInvalidParameter ReasonCode = "invalid_parameter"
	// ReasonPolicyMutationBlocked marks an operation refused because mutations are not enabled.
	ReasonPolicyMutationBlocked ReasonCode = "policy_mutation_blocked"
	// ReasonPolicyUnknownEffectBlocked marks an operation refused because its effect could not be determined.
	ReasonPolicyUnknownEffectBlocked ReasonCode = "policy_unknown_effect_blocked"
	// ReasonUnsupportedSecurityScheme marks an operation no configured provider could ever authenticate.
	ReasonUnsupportedSecurityScheme ReasonCode = "unsupported_security_scheme"
	// ReasonAmbiguousSecurity marks several satisfiable alternatives with nothing to choose between them (FR-57).
	ReasonAmbiguousSecurity ReasonCode = "ambiguous_security"
	// ReasonUnsupportedBodySchema marks a request body schema lotsman will not translate.
	ReasonUnsupportedBodySchema ReasonCode = "unsupported_body_schema"
	// ReasonDocumentInvalid marks a structural problem the parser reported that lotsman has no narrower code for. Every diagnostic carries a code: a machine-readable report whose entries have no code cannot be branched on (FR-11).
	ReasonDocumentInvalid ReasonCode = "document_invalid"
	// ReasonInvalidSchema marks a schema that is not valid JSON Schema once normalized, so no argument can be validated against it.
	ReasonInvalidSchema ReasonCode = "invalid_schema"
	// ReasonDisabledByOverride marks an operation the operator switched off with `enabled: false`.
	ReasonDisabledByOverride ReasonCode = "disabled_by_override"
	// ReasonExcludedBySelection marks an operation outside the publication filter (catalog.includeTags).
	ReasonExcludedBySelection ReasonCode = "excluded_by_selection"
	// ReasonPolicyDeniedByRule marks an operation a deny rule refused (FR-41).
	ReasonPolicyDeniedByRule ReasonCode = "policy_denied_by_rule"
	// ReasonPolicyNotAllowedByRule marks an operation refused because a non-empty allow list did not match it (FR-41).
	ReasonPolicyNotAllowedByRule ReasonCode = "policy_not_allowed_by_rule"
	// ReasonApprovalUnavailable marks a call refused because approval was required and the client cannot be asked (FR-45).
	ReasonApprovalUnavailable ReasonCode = "approval_unavailable"
	// ReasonApprovalDeclined marks a call refused because the approval prompt was declined or dismissed.
	ReasonApprovalDeclined ReasonCode = "approval_declined"
)

// Executable reports whether the operation is both semantically supported
// and fully executable by the current runtime slice.
func (o *Operation) Executable() bool {
	return o.Support.Level == SupportSupported && len(o.ExecutionBlockers) == 0
}

// Diagnostic records a problem found while normalizing an operation, with
// enough provenance to point back at the source document.
type Diagnostic struct {
	Severity Severity   `json:"severity"`
	Code     ReasonCode `json:"code,omitempty"`
	Pointer  string     `json:"pointer,omitempty"` // JSON Pointer into the source document
	Line     int        `json:"line,omitempty"`
	Col      int        `json:"col,omitempty"`
	Message  string     `json:"message"`
}

// SupportLevel classifies whether an operation can be published as a tool.
type SupportLevel string

// Support levels (data-model.md). An operation absent Level == Supported is
// excluded from the catalog by default.
const (
	SupportSupported          SupportLevel = "supported"
	SupportPartiallySupported SupportLevel = "partially_supported"
	SupportRejected           SupportLevel = "rejected"
)

// SupportStatus is the verdict for one operation plus the reasons behind it.
type SupportStatus struct {
	Level   SupportLevel `json:"level"`
	Reasons []ReasonCode `json:"reasons,omitempty"`
}

// ParameterLocation is an OAS parameter `in` value.
type ParameterLocation string

// Parameter locations (OAS 3.x).
const (
	LocationPath   ParameterLocation = "path"
	LocationQuery  ParameterLocation = "query"
	LocationHeader ParameterLocation = "header"
	LocationCookie ParameterLocation = "cookie"
)

// ParameterStyle is an OAS serialization style.
type ParameterStyle string

// Serialization styles (OAS 3.x). Only Simple and Form are serialized by the
// current runtime; the rest exist so an operation can be rejected by name
// rather than approximated (docs/spec.md 4.5).
const (
	StyleSimple         ParameterStyle = "simple"
	StyleForm           ParameterStyle = "form"
	StyleLabel          ParameterStyle = "label"
	StyleMatrix         ParameterStyle = "matrix"
	StyleSpaceDelimited ParameterStyle = "spaceDelimited"
	StylePipeDelimited  ParameterStyle = "pipeDelimited"
	StyleDeepObject     ParameterStyle = "deepObject"
)

// DefaultStyle is the style OAS implies when a parameter omits it. The
// default depends on the location, which is the single most commonly
// mis-implemented detail of parameter serialization (pipeline.md 3.3).
func DefaultStyle(in ParameterLocation) ParameterStyle {
	switch in {
	case LocationQuery, LocationCookie:
		return StyleForm
	case LocationPath, LocationHeader:
		return StyleSimple
	default:
		return ""
	}
}

// DefaultExplode is the explode OAS implies when a parameter omits it: true
// for form, false for everything else. A query array defaulting to
// explode=false produces "?ids=1,2,3" where the spec means "?ids=1&ids=2"
// (pitfall #5).
func DefaultExplode(style ParameterStyle) bool {
	return style == StyleForm
}

// Parameter is one normalized operation input: identity, location and the
// serialization rules that turn a validated argument into bytes on the wire.
type Parameter struct {
	Name string            `json:"name"`
	In   ParameterLocation `json:"in"`
	// Required is authoritative: for path parameters OAS requires it, and an
	// operation whose template disagrees is rejected upstream (FR-27).
	Required   bool `json:"required"`
	Deprecated bool `json:"deprecated,omitempty"`
	// Style and Explode are always resolved -- never empty or "unset" -- so no
	// consumer has to re-derive a location-dependent default.
	Style   ParameterStyle `json:"style"`
	Explode bool           `json:"explode"`
	// AllowReserved keeps RFC 3986 reserved characters literal in a form-style
	// query value. It is why query building cannot go through
	// url.Values.Encode (FR-26).
	AllowReserved bool   `json:"allowReserved,omitempty"`
	Description   string `json:"description,omitempty"` // untrusted, sanitized downstream
	// Schema is the JSON Schema 2020-12 fragment describing the argument.
	Schema Schema `json:"schema"`
}

// protectedHeaders are header names a spec may not hand to a model.
//
// Some control the transport (Host, Content-Length, Transfer-Encoding) and
// would corrupt the request; others are lotsman's to set, not the caller's: a
// spec-declared "Authorization" parameter would let a model supply its own
// credentials, bypassing the auth layer entirely, and "Content-Type" would let
// it contradict the body lotsman is sending.
var protectedHeaders = map[string]bool{
	"authorization": true, "connection": true, "content-length": true,
	"content-type": true, "cookie": true, "expect": true, "host": true,
	"proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

// IsProtectedHeader reports whether name is a header lotsman refuses to let a
// spec parameterize. It lives here rather than in the request builder so the
// adapter can reject such an operation at translation time, from the same
// list the builder enforces at call time.
func IsProtectedHeader(name string) bool {
	return protectedHeaders[strings.ToLower(strings.TrimSpace(name))]
}

// Schema is a JSON Schema 2020-12 fragment. A map rather than a struct
// because it is published verbatim to the client and validated as data;
// encoding/json sorts map keys, so the rendering stays deterministic
// (Constitution IV).
type Schema map[string]any

// How a schema's keywords must be walked.
//
// Two packages walk a published schema for different reasons -- catalog
// sanitizes its prose before it reaches a model (Constitution V), mcpserver
// strips prose to fit a response budget (FR-52) -- and both have to agree
// about one fact of JSON Schema: which values are schemas and which are the
// API's own data.
//
// Getting it wrong is not cosmetic. A walker that treats `properties` as a
// list of keywords deletes the API's field called `description`; one that
// walks into `enum` edits the API's values. That bug existed in the budget
// prune, which is why the answer lives here once instead of being written out
// twice and drifting.
//
// Anything not named below is copied as it was found. lotsman does not know
// what an unrecognised keyword means, and guessing is how a schema stops
// describing the API.
var (
	// SchemaValuedKeywords hold one schema.
	SchemaValuedKeywords = map[string]bool{
		"items": true, "not": true, "if": true, "then": true, "else": true,
		"contains": true, "propertyNames": true, "additionalProperties": true,
		"additionalItems": true, "unevaluatedItems": true, "unevaluatedProperties": true,
	}
	// SchemaListKeywords hold an array of schemas.
	SchemaListKeywords = map[string]bool{
		"allOf": true, "anyOf": true, "oneOf": true, "prefixItems": true,
	}
	// NamedSchemaKeywords map a *name* to a schema. The names belong to the
	// API and are never keywords.
	NamedSchemaKeywords = map[string]bool{
		"properties": true, "$defs": true, "definitions": true,
		"patternProperties": true, "dependentSchemas": true,
	}
	// ProseKeywords explain a schema to a reader rather than constrain what
	// it accepts.
	ProseKeywords = map[string]bool{
		"description": true, "title": true, "examples": true,
	}
)

// Argument groups (FR-22). Inputs are ALWAYS grouped, even when a group is
// empty: a flat-until-collision shape would change retroactively the day an
// API adds a path parameter colliding with a query one, and a tool's schema
// must not change shape for a reason the operation itself did not.
const (
	GroupPath    = "path"
	GroupQuery   = "query"
	GroupHeaders = "headers"
	GroupCookies = "cookies"
	GroupBody    = "body"
)

// GroupOrder fixes the order groups are emitted in. JSON objects are
// unordered, but a fixed order keeps the serialized schema -- and with it the
// catalog digest -- stable.
var GroupOrder = []string{GroupPath, GroupQuery, GroupHeaders, GroupCookies, GroupBody}

// GroupOf maps a parameter location to the argument group carrying it.
func GroupOf(in ParameterLocation) string {
	switch in {
	case LocationPath:
		return GroupPath
	case LocationQuery:
		return GroupQuery
	case LocationHeader:
		return GroupHeaders
	case LocationCookie:
		return GroupCookies
	default:
		return ""
	}
}

// InputModel is an operation's full argument surface. Parameters are in a
// deterministic order (location, then name), which is also the order the
// query string is built in.
type InputModel struct {
	Parameters []Parameter `json:"parameters,omitempty"`
	Body       *BodySpec   `json:"body,omitempty"`
	// Defs holds the schemas that are referenced rather than inlined, keyed by
	// the name a "#/$defs/<name>" pointer uses. Keeping references as
	// references is what stops a schema that appears five times in one
	// operation from being published five times -- and what lets a recursive
	// schema be published at all.
	Defs SchemaDefs `json:"defs,omitempty"`
}

// SchemaDefs is the `$defs` bundle of an input schema.
type SchemaDefs map[string]Schema

// BodySpec is the request body lotsman will send: exactly one media type,
// chosen deterministically, with the schema the argument is validated
// against (FR-29).
type BodySpec struct {
	MediaType   string `json:"mediaType"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"` // untrusted, sanitized downstream
	Schema      Schema `json:"schema"`
}
