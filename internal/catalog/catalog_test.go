package catalog

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/policy"
)

func supportedOp(key, method, path, opID string) domain.Operation {
	return domain.Operation{
		Key:               domain.OperationKey(key),
		SourceOperationID: opID,
		Method:            method,
		PathTemplate:      path,
		Support:           domain.SupportStatus{Level: domain.SupportSupported},
	}
}

func TestBuildExcludesUnsupported(t *testing.T) {
	ops := []domain.Operation{
		supportedOp("a:GET:/x", "GET", "/x", "getX"),
		{
			Key:     "b:GET:/y",
			Method:  "GET",
			Support: domain.SupportStatus{Level: domain.SupportRejected},
		},
	}
	cat := Build("sha256:spec", ops, Options{})
	if len(cat.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1: %+v", len(cat.Tools), cat.Tools)
	}
	if cat.Tools[0].Name != "get_x" {
		t.Errorf("Name = %q, want get_x", cat.Tools[0].Name)
	}
	if cat.SpecDigest != "sha256:spec" {
		t.Errorf("SpecDigest = %q, want sha256:spec", cat.SpecDigest)
	}
}

func TestBuildDeterministicOrderIndependentOfInput(t *testing.T) {
	a := supportedOp("ns:GET:/a", "GET", "/a", "getA")
	b := supportedOp("ns:GET:/b", "GET", "/b", "getB")

	cat1 := Build("d", []domain.Operation{a, b}, Options{})
	cat2 := Build("d", []domain.Operation{b, a}, Options{})

	if len(cat1.Tools) != 2 || len(cat2.Tools) != 2 {
		t.Fatalf("unexpected tool counts: %d, %d", len(cat1.Tools), len(cat2.Tools))
	}
	if cat1.Tools[0].OperationKey != cat2.Tools[0].OperationKey || cat1.Tools[1].OperationKey != cat2.Tools[1].OperationKey {
		t.Errorf("order depends on input order: %+v vs %+v", cat1.Tools, cat2.Tools)
	}
	if cat1.Digest != cat2.Digest {
		t.Errorf("Digest depends on input order: %q vs %q", cat1.Digest, cat2.Digest)
	}
}

func TestToolNameFromOperationID(t *testing.T) {
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/pets", "GET", "/pets", "listPets")}, Options{})
	if got := cat.Tools[0].Name; got != "list_pets" {
		t.Errorf("Name = %q, want list_pets", got)
	}
}

func TestToolNameFallsBackToMethodAndPath(t *testing.T) {
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/pets/{petId}", "GET", "/pets/{petId}", "")}, Options{})
	name := cat.Tools[0].Name
	if strings.Contains(name, " ") {
		t.Errorf("Name %q contains a space", name)
	}
	if !nameCharsetOK(name) {
		t.Errorf("Name %q outside the portable charset", name)
	}
	if name != "get_pets" {
		t.Errorf("Name = %q, want get_pets", name)
	}
}

func TestToolNameSanitizesCharsetAndLength(t *testing.T) {
	longID := strings.Repeat("a", 100) + " weird!name@here"
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/x", "GET", "/x", longID)}, Options{})
	name := cat.Tools[0].Name
	if len(name) > maxNameBytes {
		t.Errorf("len(Name) = %d, want <= %d", len(name), maxNameBytes)
	}
	if !nameCharsetOK(name) {
		t.Errorf("Name %q outside the portable charset", name)
	}
}

func TestToolNameCollisionGetsStableHashSuffix(t *testing.T) {
	// Two different operations that sanitize to the same operationId.
	ops := []domain.Operation{
		supportedOp("ns:GET:/a", "GET", "/a", "dup"),
		supportedOp("ns:POST:/a", "POST", "/a", "dup"),
	}
	cat := Build("d", ops, Options{})
	if len(cat.Tools) != 2 {
		t.Fatalf("len(Tools) = %d, want 2", len(cat.Tools))
	}
	names := map[string]bool{cat.Tools[0].Name: true, cat.Tools[1].Name: true}
	if len(names) != 2 {
		t.Fatalf("collision not resolved: names = %+v", names)
	}
	// GET sorts before POST as OperationKey text, so it keeps the plain name.
	if cat.Tools[0].Name != "dup" {
		t.Errorf("first tool Name = %q, want unsuffixed dup", cat.Tools[0].Name)
	}
	if cat.Tools[1].Name == "dup" || !strings.HasPrefix(cat.Tools[1].Name, "dup_") {
		t.Errorf("second tool Name = %q, want dup_<hash>", cat.Tools[1].Name)
	}
}

func TestToolNameCollisionSuffixIsStableAcrossRuns(t *testing.T) {
	ops := []domain.Operation{
		supportedOp("ns:GET:/a", "GET", "/a", "dup"),
		supportedOp("ns:POST:/a", "POST", "/a", "dup"),
	}
	cat1 := Build("d", ops, Options{})
	cat2 := Build("d", ops, Options{})
	if cat1.Tools[1].Name != cat2.Tools[1].Name {
		t.Errorf("collision suffix not stable: %q vs %q", cat1.Tools[1].Name, cat2.Tools[1].Name)
	}
}

func TestDescriptionPrefersSummaryOverDescription(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = "Short summary."
	op.Description = "Long description that should not be used."
	cat := Build("d", []domain.Operation{op}, Options{})
	if got := cat.Tools[0].Description; got != "Short summary." {
		t.Errorf("Description = %q, want the summary", got)
	}
}

func TestDescriptionFallsBackToDescription(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Description = "Only a description."
	cat := Build("d", []domain.Operation{op}, Options{})
	if got := cat.Tools[0].Description; got != "Only a description." {
		t.Errorf("Description = %q, want the description", got)
	}
}

func TestDescriptionStripsHTMLAndControlChars(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = "Deletes <b>everything</b>.\x07 Multiple   spaces."
	cat := Build("d", []domain.Operation{op}, Options{})
	want := "Deletes everything. Multiple spaces."
	if got := cat.Tools[0].Description; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
}

func TestDescriptionRespectsByteBudget(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = strings.Repeat("a", descriptionByteBudget+500)
	cat := Build("d", []domain.Operation{op}, Options{})
	if len(cat.Tools[0].Description) > descriptionByteBudget {
		t.Errorf("len(Description) = %d, want <= %d", len(cat.Tools[0].Description), descriptionByteBudget)
	}
}

func TestDescriptionBudgetPreservesUTF8(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = strings.Repeat("a", descriptionByteBudget-1) + "é"
	cat := Build("d", []domain.Operation{op}, Options{})
	got := cat.Tools[0].Description
	if !utf8.ValidString(got) {
		t.Fatalf("Description is invalid UTF-8: %q", got)
	}
	if len(got) > descriptionByteBudget {
		t.Errorf("len(Description) = %d, want <= %d", len(got), descriptionByteBudget)
	}
}

func TestBuildCarriesExecutionReadiness(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.ExecutionBlockers = []domain.ReasonCode{domain.ReasonParametersNotImplemented}
	tool := Build("d", []domain.Operation{op}, Options{}).Tools[0]
	if tool.Executable {
		t.Fatal("tool with an execution blocker is executable")
	}
	if len(tool.ExecutionBlockers) != 1 || tool.ExecutionBlockers[0] != domain.ReasonParametersNotImplemented {
		t.Fatalf("ExecutionBlockers = %v", tool.ExecutionBlockers)
	}
}

func TestBuildPropagatesServers(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Servers = []string{"https://api.example.com"}
	cat := Build("d", []domain.Operation{op}, Options{})
	if got := cat.Tools[0].Servers; len(got) != 1 || got[0] != "https://api.example.com" {
		t.Errorf("Servers = %v, want [https://api.example.com]", got)
	}
}

func TestDigestChangesOnlyWhenContentChanges(t *testing.T) {
	ops := []domain.Operation{supportedOp("ns:GET:/x", "GET", "/x", "getX")}
	cat1 := Build("d", ops, Options{})
	cat2 := Build("d", ops, Options{})
	if cat1.Digest != cat2.Digest {
		t.Errorf("digest changed with identical input: %q vs %q", cat1.Digest, cat2.Digest)
	}

	ops[0].SourceOperationID = "getY"
	cat3 := Build("d", ops, Options{})
	if cat3.Digest == cat1.Digest {
		t.Error("digest did not change when a tool name changed")
	}
}

func nameCharsetOK(name string) bool {
	if name == "" || len(name) > maxNameBytes {
		return false
	}
	return !nameCharset.MatchString(name)
}

func operationWithEffect(method, path string, effect domain.Effect, body *domain.BodySpec) domain.Operation {
	return domain.Operation{
		Key:          domain.NewOperationKey("ns", method, path),
		Method:       method,
		PathTemplate: path,
		Effect: domain.EffectDecision{
			Effect: effect, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred,
		},
		Input:   domain.InputModel{Body: body},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
}

// The catalog carries the policy verdict, so a report can distinguish "not
// implemented yet" from "the operator said no" (T021 groups them separately).
func TestPolicyBlockersAreSeparateFromExecutionBlockers(t *testing.T) {
	ops := []domain.Operation{
		operationWithEffect("GET", "/pets", domain.EffectRead, nil),
		operationWithEffect("POST", "/pets", domain.EffectUnknown, nil),
		operationWithEffect("DELETE", "/pets/{petId}", domain.EffectDestructive, nil),
	}

	readOnly := Build("d", ops, Options{})
	byMethod := map[string]Tool{}
	for _, tool := range readOnly.Tools {
		byMethod[tool.Method] = tool
	}

	if get := byMethod["GET"]; !get.Executable || len(get.PolicyBlockers) != 0 {
		t.Errorf("GET = %+v, want executable with no policy blockers", get)
	}
	if post := byMethod["POST"]; post.Executable ||
		!reflect.DeepEqual(post.PolicyBlockers, []domain.ReasonCode{domain.ReasonPolicyUnknownEffectBlocked}) {
		t.Errorf("POST = %+v, want blocked as unknown", post)
	}
	if del := byMethod["DELETE"]; del.Executable ||
		!reflect.DeepEqual(del.PolicyBlockers, []domain.ReasonCode{domain.ReasonPolicyMutationBlocked}) {
		t.Errorf("DELETE = %+v, want blocked as a mutation", del)
	}
	if byMethod["POST"].PolicyMessage == "" {
		t.Error("a policy refusal must say what would change it")
	}
	if len(byMethod["POST"].ExecutionBlockers) != 0 {
		t.Error("a policy decision must not be reported as a missing capability")
	}

	// Enabling mutations changes the verdict, and nothing else.
	allowed := Build("d", ops, Options{Policy: policy.Config{AllowMutations: true}})
	for _, tool := range allowed.Tools {
		if !tool.Executable || len(tool.PolicyBlockers) != 0 {
			t.Errorf("%s %s = %+v, want executable with mutations enabled", tool.Method, tool.PathTemplate, tool)
		}
	}
	if allowed.Digest == readOnly.Digest {
		t.Error("a catalog built under a different policy must have a different digest")
	}
}

func TestBodyIsPublishedAsItsOwnGroup(t *testing.T) {
	body := &domain.BodySpec{
		MediaType: "application/json", Required: true,
		Description: "The pet to create.",
		Schema: domain.Schema{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}
	cat := Build("d", []domain.Operation{operationWithEffect("POST", "/pets", domain.EffectUnknown, body)},
		Options{Policy: policy.Config{AllowMutations: true}})

	schema := cat.Tools[0].InputSchema
	properties, _ := schema["properties"].(map[string]any)
	published, ok := properties[domain.GroupBody].(map[string]any)
	if !ok {
		t.Fatalf("no body group in %+v", schema)
	}
	if published["type"] != "object" {
		t.Errorf("body group = %+v, want the body schema itself", published)
	}
	if got, _ := published["description"].(string); got != "The pet to create." {
		t.Errorf("description = %q", got)
	}
	required, _ := schema["required"].([]string)
	var found bool
	for _, group := range required {
		if group == domain.GroupBody {
			found = true
		}
	}
	if !found {
		t.Errorf("required = %v, want the body group listed", required)
	}
}
