package catalog

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/razrabotchik/lotsman/internal/domain"
)

var portableName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// FuzzToolName pins the portable tool profile for any operationId a document
// can contain (FR-14-17). A client that rejects a name shows the operator an
// API that "does not work", far from the document that caused it -- so the
// charset and the length are enforced here, whatever the input.
func FuzzToolName(f *testing.F) {
	for _, seed := range []string{
		"listPets", "list_pets", "GET /pets", "получитьПитомцев", "a", "",
		strings.Repeat("x", 300), "...", "___", "🙀", "get/pets{id}",
		"APIKeyCreate", "v2.pets.list", "  spaced  name  ", "\x00\x01",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, operationID string) {
		op := domain.Operation{
			Key:               domain.NewOperationKey("ns", "GET", "/pets"),
			SourceOperationID: operationID,
			Method:            "GET",
			PathTemplate:      "/pets",
		}
		name := toolName(&op, map[string]domain.OperationKey{})

		if !portableName.MatchString(name) {
			t.Fatalf("toolName(%q) = %q, which is outside the portable profile", operationID, name)
		}
		if !utf8.ValidString(name) {
			t.Fatalf("toolName(%q) = %q, which is not valid UTF-8", operationID, name)
		}
		// Determinism: the same input always produces the same name, whatever
		// the map iteration order of anything upstream (Constitution IV).
		if again := toolName(&op, map[string]domain.OperationKey{}); again != name {
			t.Fatalf("toolName(%q) = %q then %q", operationID, name, again)
		}
	})
}

// FuzzDuplicateOperationIDs covers pitfall #2: real documents repeat an
// operationId, and two operations must never end up as one tool -- nor may
// the winner depend on the order they were seen in.
func FuzzDuplicateOperationIDs(f *testing.F) {
	for _, seed := range []string{"listPets", "", "x", strings.Repeat("y", 70), "🙀"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, operationID string) {
		first := domain.Operation{
			Key: domain.NewOperationKey("ns", "GET", "/pets"), SourceOperationID: operationID,
			Method: "GET", PathTemplate: "/pets",
			Support: domain.SupportStatus{Level: domain.SupportSupported},
			Effect:  domain.EffectDecision{Effect: domain.EffectRead},
		}
		second := domain.Operation{
			Key: domain.NewOperationKey("ns", "GET", "/pets/{petId}"), SourceOperationID: operationID,
			Method: "GET", PathTemplate: "/pets/{petId}",
			Support: domain.SupportStatus{Level: domain.SupportSupported},
			Effect:  domain.EffectDecision{Effect: domain.EffectRead},
		}

		forward := Build("d", []domain.Operation{first, second}, Options{})
		reverse := Build("d", []domain.Operation{second, first}, Options{})

		if len(forward.Tools) != 2 {
			t.Fatalf("got %d tools, want both operations published", len(forward.Tools))
		}
		if forward.Tools[0].Name == forward.Tools[1].Name {
			t.Fatalf("duplicate operationId %q collapsed two operations into %q", operationID, forward.Tools[0].Name)
		}
		for i := range forward.Tools {
			if name := forward.Tools[i].Name; !portableName.MatchString(name) {
				t.Fatalf("collision suffix broke the portable profile: %q", name)
			}
		}
		// The names may not depend on which operation was seen first.
		if forward.Digest != reverse.Digest {
			t.Fatalf("catalog depends on input order: %q != %q", forward.Digest, reverse.Digest)
		}
	})
}
