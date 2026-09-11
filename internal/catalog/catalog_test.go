package catalog

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
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
	cat := Build("sha256:spec", ops)
	if len(cat.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1: %+v", len(cat.Tools), cat.Tools)
	}
	if cat.Tools[0].Name != "getX" {
		t.Errorf("Name = %q, want getX", cat.Tools[0].Name)
	}
	if cat.SpecDigest != "sha256:spec" {
		t.Errorf("SpecDigest = %q, want sha256:spec", cat.SpecDigest)
	}
}

func TestBuildDeterministicOrderIndependentOfInput(t *testing.T) {
	a := supportedOp("ns:GET:/a", "GET", "/a", "getA")
	b := supportedOp("ns:GET:/b", "GET", "/b", "getB")

	cat1 := Build("d", []domain.Operation{a, b})
	cat2 := Build("d", []domain.Operation{b, a})

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
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/pets", "GET", "/pets", "listPets")})
	if got := cat.Tools[0].Name; got != "listPets" {
		t.Errorf("Name = %q, want listPets", got)
	}
}

func TestToolNameFallsBackToMethodAndPath(t *testing.T) {
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/pets/{petId}", "GET", "/pets/{petId}", "")})
	name := cat.Tools[0].Name
	if strings.Contains(name, " ") {
		t.Errorf("Name %q contains a space", name)
	}
	if !nameCharsetOK(name) {
		t.Errorf("Name %q outside the portable charset", name)
	}
}

func TestToolNameSanitizesCharsetAndLength(t *testing.T) {
	longID := strings.Repeat("a", 100) + " weird!name@here"
	cat := Build("d", []domain.Operation{supportedOp("ns:GET:/x", "GET", "/x", longID)})
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
	cat := Build("d", ops)
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
	cat1 := Build("d", ops)
	cat2 := Build("d", ops)
	if cat1.Tools[1].Name != cat2.Tools[1].Name {
		t.Errorf("collision suffix not stable: %q vs %q", cat1.Tools[1].Name, cat2.Tools[1].Name)
	}
}

func TestDescriptionPrefersSummaryOverDescription(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = "Short summary."
	op.Description = "Long description that should not be used."
	cat := Build("d", []domain.Operation{op})
	if got := cat.Tools[0].Description; got != "Short summary." {
		t.Errorf("Description = %q, want the summary", got)
	}
}

func TestDescriptionFallsBackToDescription(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Description = "Only a description."
	cat := Build("d", []domain.Operation{op})
	if got := cat.Tools[0].Description; got != "Only a description." {
		t.Errorf("Description = %q, want the description", got)
	}
}

func TestDescriptionStripsHTMLAndControlChars(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = "Deletes <b>everything</b>.\x07 Multiple   spaces."
	cat := Build("d", []domain.Operation{op})
	want := "Deletes everything. Multiple spaces."
	if got := cat.Tools[0].Description; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
}

func TestDescriptionRespectsByteBudget(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Summary = strings.Repeat("a", descriptionByteBudget+500)
	cat := Build("d", []domain.Operation{op})
	if len(cat.Tools[0].Description) > descriptionByteBudget {
		t.Errorf("len(Description) = %d, want <= %d", len(cat.Tools[0].Description), descriptionByteBudget)
	}
}

func TestBuildPropagatesServers(t *testing.T) {
	op := supportedOp("ns:GET:/x", "GET", "/x", "getX")
	op.Servers = []string{"https://api.example.com"}
	cat := Build("d", []domain.Operation{op})
	if got := cat.Tools[0].Servers; len(got) != 1 || got[0] != "https://api.example.com" {
		t.Errorf("Servers = %v, want [https://api.example.com]", got)
	}
}

func TestDigestChangesOnlyWhenContentChanges(t *testing.T) {
	ops := []domain.Operation{supportedOp("ns:GET:/x", "GET", "/x", "getX")}
	cat1 := Build("d", ops)
	cat2 := Build("d", ops)
	if cat1.Digest != cat2.Digest {
		t.Errorf("digest changed with identical input: %q vs %q", cat1.Digest, cat2.Digest)
	}

	ops[0].SourceOperationID = "getY"
	cat3 := Build("d", ops)
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
