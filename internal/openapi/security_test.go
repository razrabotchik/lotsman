package openapi

import (
	"reflect"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

const securitySchemes = `
components:
  securitySchemes:
    apiKeyAuth: { type: apiKey, in: header, name: X-API-Key }
    bearerAuth: { type: http, scheme: bearer }
    basicAuth: { type: http, scheme: basic }
    signature: { type: apiKey, in: query, name: sig }
    oauth:
      type: oauth2
      flows:
        clientCredentials: { tokenUrl: https://example.com/token, scopes: { read: r } }
`

func securitySpec(operation string) []byte {
	return []byte(`openapi: 3.0.3
info: { title: Security, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: listWidgets
` + operation + `
      responses: { "200": { description: ok } }
` + securitySchemes)
}

// FR-56: alternatives are OR, and the schemes inside one alternative are AND.
// Both have to survive into the IR, because an auth policy chooses between
// alternatives and must satisfy every requirement in the one it picks.
func TestSecurityAlternativesKeepOrAndAnd(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - apiKeyAuth: []
        - bearerAuth: []
        - apiKeyAuth: []
          signature: []`))

	if len(op.Security) != 3 {
		t.Fatalf("alternatives = %+v, want 3", op.Security)
	}
	if len(op.Security[0].Requirements) != 1 || op.Security[0].Requirements[0].Scheme != "apiKeyAuth" {
		t.Errorf("first alternative = %+v", op.Security[0])
	}
	third := op.Security[2]
	if len(third.Requirements) != 2 {
		t.Fatalf("third alternative = %+v, want two schemes ANDed", third)
	}
	var names []string
	for _, requirement := range third.Requirements {
		names = append(names, requirement.Scheme)
	}
	if !reflect.DeepEqual(names, []string{"apiKeyAuth", "signature"}) {
		t.Errorf("AND requirements = %v, want both schemes in document order", names)
	}
}

// The scheme's own definition travels with the requirement: an auth provider
// needs to know it is an API key in a header called X-API-Key, not just that
// something named apiKeyAuth is wanted.
func TestSecurityRequirementCarriesTheSchemeDefinition(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - apiKeyAuth: []`))

	requirement := op.Security[0].Requirements[0]
	if requirement.Type != "apiKey" || requirement.In != "header" || requirement.Name != "X-API-Key" {
		t.Errorf("requirement = %+v", requirement)
	}
	if !requirement.Satisfiable {
		t.Error("an API key in a header is exactly what the core providers cover")
	}
}

func TestSecurityScopesArePreserved(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - oauth: [read, write]`))

	requirement := op.Security[0].Requirements[0]
	if !reflect.DeepEqual(requirement.Scopes, []string{"read", "write"}) {
		t.Errorf("scopes = %v, want both", requirement.Scopes)
	}
}

// FR-55: an operation's security replaces the document's; only a missing one
// inherits. And an explicit empty list means public (pitfall #4).
func TestSecurityInheritanceAndOverride(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
info: { title: Security, version: "1.0" }
security:
  - bearerAuth: []
paths:
  /inherits:
    get:
      operationId: inherits
      responses: { "200": { description: ok } }
  /overrides:
    get:
      operationId: overrides
      security:
        - apiKeyAuth: []
      responses: { "200": { description: ok } }
  /public:
    get:
      operationId: public
      security: []
      responses: { "200": { description: ok } }
  /optional:
    get:
      operationId: optional
      security:
        - {}
        - bearerAuth: []
      responses: { "200": { description: ok } }
` + securitySchemes)

	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byID := map[string]*domain.Operation{}
	for i := range doc.Operations {
		byID[doc.Operations[i].SourceOperationID] = &doc.Operations[i]
	}

	if got := byID["inherits"].Security; len(got) != 1 || got[0].Requirements[0].Scheme != "bearerAuth" {
		t.Errorf("inherited security = %+v", got)
	}
	if got := byID["overrides"].Security; len(got) != 1 || got[0].Requirements[0].Scheme != "apiKeyAuth" {
		t.Errorf("overridden security = %+v, want the operation's own, not a merge", got)
	}
	if got := byID["public"].Security; len(got) != 0 {
		t.Errorf("explicit empty security = %+v, want public", got)
	}
	// An empty requirement object among the alternatives means "or no auth",
	// so the operation carries no security at all.
	if got := byID["optional"].Security; len(got) != 2 || len(got[0].Requirements) != 0 {
		t.Errorf("optional security = %+v, want an empty alternative first", got)
	}
}

// An operation whose every alternative needs a credential lotsman will never
// supply in this release can never be called, so it is rejected rather than
// published as a tool that always fails.
func TestUnsatisfiableSecurityRejectsTheOperation(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - oauth: [read]`))

	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %q, want rejected", op.Support.Level)
	}
	if !hasReason(op.Support.Reasons, domain.ReasonUnsupportedSecurityScheme) {
		t.Errorf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonUnsupportedSecurityScheme)
	}
}

// One satisfiable alternative is enough: OAuth2 OR an API key means lotsman
// can take the second road.
func TestOneSatisfiableAlternativeIsEnough(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - oauth: [read]
        - apiKeyAuth: []`))

	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v, want supported", op.Support)
	}
	if op.Security[0].Satisfiable() {
		t.Error("the OAuth2 alternative must be marked unsatisfiable")
	}
	if !op.Security[1].Satisfiable() {
		t.Error("the API key alternative must be marked satisfiable")
	}
}

// A requirement naming a scheme the document never defines cannot be met, and
// an AND alternative is only as satisfiable as its weakest member.
func TestUndefinedSchemeIsNotSatisfiable(t *testing.T) {
	op := parseOne(t, securitySpec(`      security:
        - apiKeyAuth: []
          ghostAuth: []`))

	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %q, want rejected", op.Support.Level)
	}
	if op.Security[0].Satisfiable() {
		t.Error("an alternative naming an undefined scheme must not be satisfiable")
	}
}
