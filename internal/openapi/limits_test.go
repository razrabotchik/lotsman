package openapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

const limitsHeader = `openapi: 3.0.3
info: {title: Limits, version: "1.0"}
`

// A reference to a document that is not there is refused by name, with the
// pointer and position of the reference that asked for it.
func TestParseRefusesUnresolvableFileRef(t *testing.T) {
	spec := limitsHeader + `paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: './common.yaml#/components/schemas/Pet'
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !doc.HasErrors() {
		t.Fatal("a reference to a missing document must be a document-level error")
	}

	var found *domain.Diagnostic
	for i, d := range doc.Diagnostics {
		if d.Code == domain.ReasonRefUnresolvable {
			found = &doc.Diagnostics[i]
		}
	}
	if found == nil {
		t.Fatalf("no %s diagnostic in %+v", domain.ReasonRefUnresolvable, doc.Diagnostics)
	}
	if found.Pointer == "" || found.Line == 0 {
		t.Errorf("diagnostic lacks provenance: %+v", *found)
	}
}

func TestParseExternalRefFromStdinExplainsWhy(t *testing.T) {
	spec := limitsHeader + `paths:
  /pets:
    get:
      parameters:
        - $ref: 'params.yaml#/Limit'
      responses: {"200": {description: ok}}
`
	doc, err := Parse(t.Context(), []byte(spec), Options{}) // no RootPath: stdin
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, d := range doc.Diagnostics {
		if d.Code == domain.ReasonExternalRefUnsupported {
			if !strings.Contains(d.Message, "stdin") {
				t.Errorf("message = %q, want it to explain that stdin has no directory", d.Message)
			}
			return
		}
	}
	t.Fatalf("no external ref diagnostic in %+v", doc.Diagnostics)
}

func TestParseAcceptsLocalRefs(t *testing.T) {
	spec := limitsHeader + `paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Pet'
components:
  schemas:
    Pet:
      type: object
      properties:
        tag:
          $ref: '#/components/schemas/Tag'
    Tag:
      type: string
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("local refs must resolve: %+v", doc.Diagnostics)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(doc.Operations))
	}
}

// refChain builds a spec whose schema refs form a chain of the given length:
// S0 -> S1 -> ... -> Sn.
func refChain(length int) string {
	var b strings.Builder
	b.WriteString(limitsHeader)
	b.WriteString(`paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/S0'
components:
  schemas:
`)
	for i := 0; i < length; i++ {
		fmt.Fprintf(&b, "    S%d:\n      $ref: '#/components/schemas/S%d'\n", i, i+1)
	}
	fmt.Fprintf(&b, "    S%d:\n      type: string\n", length)
	return b.String()
}

func TestParseRefusesDeepRefChain(t *testing.T) {
	_, err := Parse(t.Context(), []byte(refChain(10)), Options{Limits: Limits{MaxRefDepth: 4}})
	if err == nil {
		t.Fatal("want an error for a chain deeper than the limit")
	}
	if got := errs.ClassOf(err); got != errs.ClassUnsupported {
		t.Errorf("class = %q, want %q (%v)", got, errs.ClassUnsupported, err)
	}
	if !strings.Contains(err.Error(), "hops deep") {
		t.Errorf("error = %q, want it to name the depth budget", err)
	}
}

func TestParseAllowsChainWithinRefDepth(t *testing.T) {
	if _, err := Parse(t.Context(), []byte(refChain(3)), Options{Limits: Limits{MaxRefDepth: 8}}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestParseRefusesCyclicRefsWithoutHanging(t *testing.T) {
	// The budget scan must terminate on a cycle; whether the cycle itself is
	// truncated or rejected is T025's decision, not the budget's.
	spec := limitsHeader + `paths:
  /pets:
    get:
      responses: {"200": {description: ok}}
components:
  schemas:
    A:
      $ref: '#/components/schemas/B'
    B:
      $ref: '#/components/schemas/A'
`
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Parse(context.Background(), []byte(spec), Options{})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Parse hung on a cyclic $ref")
	}
}

func TestParseRefusesTooManyDocumentsInClosure(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString(limitsHeader)
	b.WriteString("paths:\n  /pets:\n    get:\n      parameters:\n")
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("part%d.yaml", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("Param:\n  name: p\n  in: query\n  schema: {type: string}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "        - $ref: '%s#/Param'\n", name)
	}
	b.WriteString(`      responses: {"200": {description: ok}}
`)

	_, err := Parse(t.Context(), []byte(b.String()), Options{RootPath: root, Limits: Limits{MaxRefDocuments: 3}})
	if err == nil {
		t.Fatal("want an error when the closure spans more documents than the limit")
	}
	if !strings.Contains(err.Error(), "documents") {
		t.Errorf("error = %q, want it to name the document budget", err)
	}
	if got := errs.ClassOf(err); got != errs.ClassUnsupported {
		t.Errorf("class = %q, want %q", got, errs.ClassUnsupported)
	}
}

func TestParseRefusesOversizedClosure(t *testing.T) {
	_, err := Parse(t.Context(), []byte(refChain(2)), Options{Limits: Limits{MaxRefBytes: 32}})
	if err == nil {
		t.Fatal("want an error when the closure exceeds the byte budget")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error = %q, want it to name the byte budget", err)
	}
}

func TestParseRefusesTooManyOperations(t *testing.T) {
	var b strings.Builder
	b.WriteString(limitsHeader)
	b.WriteString("paths:\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "  /r%d:\n    get:\n      responses: {\"200\": {description: ok}}\n", i)
	}

	_, err := Parse(t.Context(), []byte(b.String()), Options{Limits: Limits{MaxOperations: 10}})
	if err == nil {
		t.Fatal("want an error for a document over the operation limit")
	}
	if got := errs.ClassOf(err); got != errs.ClassUnsupported {
		t.Errorf("class = %q, want %q", got, errs.ClassUnsupported)
	}
	if !strings.Contains(err.Error(), "12 operations") {
		t.Errorf("error = %q, want it to report the actual count", err)
	}
}

func TestParseHonoursParseTimeout(t *testing.T) {
	spec := limitsHeader + "paths:\n  /pets:\n    get:\n      responses: {\"200\": {description: ok}}\n"

	_, err := Parse(t.Context(), []byte(spec), Options{Limits: Limits{ParseTimeout: time.Nanosecond}})
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
		t.Errorf("class = %q, want %q (%v)", got, errs.ClassSpecInvalid, err)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error = %q, want it to name the parse budget", err)
	}
}

func TestParseCancellationIsNotTheDocumentsFault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	spec := limitsHeader + "paths:\n  /pets:\n    get:\n      responses: {\"200\": {description: ok}}\n"
	_, err := Parse(ctx, []byte(spec), Options{})
	if err == nil {
		t.Fatal("want an error for a cancelled context")
	}
	if got := errs.ClassOf(err); got != errs.ClassInternal {
		t.Errorf("class = %q, want %q: an operator's ctrl-C is not an invalid spec", got, errs.ClassInternal)
	}
}

func TestParseErrorClassesForBadDocuments(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec string
	}{
		{"not yaml", "key: [unterminated\n"},
		{"not openapi", "hello: world\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(t.Context(), []byte(tt.spec), Options{})
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
				t.Errorf("class = %q, want %q (%v)", got, errs.ClassSpecInvalid, err)
			}
		})
	}
}

func TestRefusedRefRejectsItsOperation(t *testing.T) {
	// The operation must not read as more executable than it is: libopenapi
	// drops the parameter whose $ref was refused, which would otherwise leave
	// an operation with no parameters and therefore no blocker at all.
	spec := limitsHeader + `paths:
  /pets:
    get:
      parameters:
        - $ref: './common.yaml#/Limit'
      responses: {"200": {description: ok}}
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: "/srv"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(doc.Operations))
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportRejected {
		t.Errorf("support = %q, want %q", op.Support.Level, domain.SupportRejected)
	}
	if op.Executable() {
		t.Error("an operation with a refused reference must not be executable")
	}
	if len(op.Diagnostics) == 0 {
		t.Error("the refusal must be visible on the operation, not only on the document")
	}
}

func TestRefusedPathLevelRefBlocksEveryMethod(t *testing.T) {
	spec := limitsHeader + `paths:
  /pets:
    parameters:
      - $ref: 'shared.yaml#/Tenant'
    get:
      responses: {"200": {description: ok}}
    post:
      responses: {"201": {description: created}}
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: "/srv"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// libopenapi fails the whole path item when a shared parameter cannot be
	// resolved, so the methods may not enumerate at all. Either way the
	// invariant holds: the document fails closed and nothing under that path
	// is executable.
	if !doc.HasErrors() {
		t.Error("a refused path-level reference must be a document-level error")
	}
	for _, op := range doc.Operations {
		if op.Executable() {
			t.Errorf("%s is executable despite a refused path-level reference", op.Key)
		}
	}
}

func TestLocalRefsLeaveOperationSupported(t *testing.T) {
	doc, err := Parse(t.Context(), []byte(refChain(2)), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Operations[0].Support.Level; got != domain.SupportSupported {
		t.Errorf("support = %q, want %q", got, domain.SupportSupported)
	}
}

// The version is decided from the document itself, before the parser sees it,
// so an unsupported one is named in lotsman's terms (pipeline.md 1.1). Found
// by running the M0 corpus: GitLab publishes Swagger 2.0, and the parser's
// own message told the operator to call BuildV2Model().
func TestParseRefusesNonOpenAPI3Documents(t *testing.T) {
	for _, tt := range []struct {
		name    string
		spec    string
		class   errs.Class
		mention string
	}{
		{
			name:    "swagger 2.0",
			spec:    "swagger: \"2.0\"\ninfo: {title: X, version: \"1\"}\npaths: {}\n",
			class:   errs.ClassUnsupported,
			mention: "Swagger 2.0",
		},
		{
			name:    "no version at all",
			spec:    "info: {title: X, version: \"1\"}\npaths: {}\n",
			class:   errs.ClassSpecInvalid,
			mention: "not an OpenAPI document",
		},
		{
			name:    "a future major version",
			spec:    "openapi: 4.0.0\ninfo: {title: X, version: \"1\"}\npaths: {}\n",
			class:   errs.ClassUnsupported,
			mention: "4.0.0",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(t.Context(), []byte(tt.spec), Options{})
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errs.ClassOf(err); got != tt.class {
				t.Errorf("class = %q, want %q (%v)", got, tt.class, err)
			}
			if !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("error = %q, want it to mention %q", err, tt.mention)
			}
			if strings.Contains(err.Error(), "BuildV2Model") {
				t.Errorf("error leaks the parser's API: %q", err)
			}
		})
	}
}
