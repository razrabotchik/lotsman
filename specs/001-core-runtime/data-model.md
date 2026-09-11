# Data Model: Core Runtime

The IR (`internal/domain`) is the contract between the OpenAPI adapter and everything else.
It stores only the semantics needed for catalog, validation and serialization + provenance.
It does NOT mirror the OpenAPI AST.

## Entities

```go
// OperationKey: "{namespace}:{METHOD}:{normalizedPath}" — stable identity,
// independent of operationId (real specs duplicate/omit it).
type OperationKey string

type Operation struct {
    Key               OperationKey
    SourceOperationID string      // as authored; diagnostics only
    ToolName          string      // MCP-facing; [A-Za-z0-9_.-], 1–64, collision-hash suffixed
    Method            string
    PathTemplate      string      // "/projects/{projectId}/members"
    Servers           []Server    // effective after op→path→root resolution
    Inputs            InputModel
    RequestBodies     []RequestBody // chosen media type marked; deterministic choice
    Responses         []ResponseModel
    Security          []SecurityAlternative // OR across; AND within
    Effect            EffectDecision
    Support           SupportStatus
    Diagnostics       []Diagnostic
    Doc               DocMeta     // sanitized summary/description + provenance
}

type InputModel struct { // grouped ALWAYS (R4)
    Path    []Param
    Query   []Param
    Header  []Param
    Cookie  []Param
}

type Param struct {
    Name          string
    Style         string      // normalized with per-location defaults
    Explode       bool
    Required      bool        // path params forced true or op rejected
    AllowReserved bool
    Schema        *Schema     // normalized JSON Schema 2020-12 subtree
}

type Schema struct {
    // Normalized 2020-12 form; $ref/$defs preserved as bundle (no full inlining).
    JSON       json.RawMessage
    Truncated  bool   // cyclic cut at depth N → partial support
}

type RequestBody struct {
    MediaType string // M1: application/json only
    Schema    *Schema
    Required  bool
    Selected  bool
}

type SecurityAlternative struct {
    Requirements []SecurityRequirement // ALL must hold (AND)
}
type SecurityRequirement struct {
    SchemeName string
    Scopes     []string
}

type Effect string // read | write | destructive | unknown
type EffectDecision struct {
    Effect     Effect
    Source     EffectSource     // http_method | recipe | local_override
    Confidence EffectConfidence // inferred | explicit
    Warnings   []string         // e.g. suspicious-verb hits
}

type SupportLevel string // supported | partially_supported | rejected
type SupportStatus struct {
    Level   SupportLevel
    Reasons []ReasonCode // machine-readable: unsupported_parameter_style,
                         // unsupported_media_type, ambiguous_security,
                         // invalid_schema, cyclic_schema_truncated,
                         // path_parameter_mismatch, ...
}

type Diagnostic struct {
    Severity string // error | warning | info
    Code     ReasonCode
    Pointer  string // JSON Pointer into source doc
    Line, Col int   // when available
    Message  string
}
```

## Catalog (derived, immutable)

```go
type Catalog struct {
    SpecDigest string
    Mode       string      // tools (001); search added in 002
    Tools      []Tool      // deterministic order
    Digest     string      // content digest; changes only on substantive change
    Report     Report      // capability report — first-class output
}

type Report struct {
    SchemaVersion string   // versioned for --json CI consumers
    Totals        struct{ Found, Supported, Partial, Rejected int }
    ByReason      map[ReasonCode]int
    Operations    []OperationVerdict
    CatalogEstimate struct{ SerializedBytes int; SelectedMode string; ToolCount int }
    SecuritySummary struct{ RemoteRefs, Redirects bool; UnknownMutationsBlocked bool }
}
```

## Invariants

1. Two loads of identical spec+config ⇒ deep-equal Catalog and equal Digest (Constitution IV).
2. `Support.Level != supported` ⇒ operation absent from Tools (strict) or absent unless
   documented fallback + opt-in (partial).
3. `Effect == read` guaranteed side-effect-free routing: read-only gate checks Effect only,
   never annotations.
4. Grouped input schema sets `additionalProperties:false` at every level; unknown argument
   fails validation, never silently dropped.
5. Secrets never appear in any entity above — auth profiles hold `SecretRef` strings
   (`env:NAME`, `file:/path`), resolved only inside providers at call time.
