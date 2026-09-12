package searchindex

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// petStore is a fixture small enough to reason about and varied enough to rank:
// names that overlap, paths that share segments, and tags that group.
func petStore() []Document {
	return []Document{
		{
			Key: "default:GET:/pets", ToolName: "list_pets", Method: "GET", Path: "/pets",
			Summary: "List pets", Tags: []string{"pets"}, Effect: domain.EffectRead,
		},
		{
			Key: "default:POST:/pets", ToolName: "create_pet", Method: "POST", Path: "/pets",
			Summary: "Create a pet", Tags: []string{"pets"}, Effect: domain.EffectUnknown,
		},
		{
			Key: "default:GET:/pets/{petId}", ToolName: "get_pet", Method: "GET", Path: "/pets/{petId}",
			Summary: "Get a pet by ID", Tags: []string{"pets"}, Effect: domain.EffectRead,
		},
		{
			Key: "default:DELETE:/pets/{petId}", ToolName: "delete_pet", Method: "DELETE", Path: "/pets/{petId}",
			Summary: "Delete a pet by ID", Tags: []string{"pets"}, Effect: domain.EffectDestructive,
		},
		{
			Key: "default:GET:/orders", ToolName: "list_orders", Method: "GET", Path: "/orders",
			Summary: "List orders for the current customer", Tags: []string{"billing"}, Effect: domain.EffectRead,
		},
		{
			Key: "default:POST:/orders/{orderId}/refund", ToolName: "refund_order", Method: "POST",
			Path: "/orders/{orderId}/refund", Summary: "Refund an order", Tags: []string{"billing"},
			Effect: domain.EffectUnknown,
		},
	}
}

func keys(results []Result) []string {
	out := make([]string, 0, len(results))
	for i := range results {
		out = append(out, string(results[i].Key))
	}
	return out
}

func TestSearchRanksTheObviousMatchFirst(t *testing.T) {
	index := Build(petStore())

	for _, tt := range []struct {
		query string
		want  domain.OperationKey
	}{
		{"list pets", "default:GET:/pets"},
		{"create pet", "default:POST:/pets"},
		{"delete pet", "default:DELETE:/pets/{petId}"},
		{"refund an order", "default:POST:/orders/{orderId}/refund"},
		// The method is indexed as a word, so a verb finds the right operation
		// even when the name does not contain it.
		{"DELETE pets", "default:DELETE:/pets/{petId}"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			results := index.Search(tt.query, Filters{}, 5)
			if len(results) == 0 {
				t.Fatalf("no results for %q", tt.query)
			}
			if results[0].Key != tt.want {
				t.Errorf("top hit = %q, want %q (got %v)", results[0].Key, tt.want, keys(results))
			}
		})
	}
}

// Principle I in the search path: nothing matches, nothing is returned. A
// "closest" operation is how an agent calls the wrong endpoint confidently.
func TestSearchReturnsNothingForNoMatch(t *testing.T) {
	if results := Build(petStore()).Search("kubernetes deployment scale", Filters{}, 5); len(results) != 0 {
		t.Errorf("results = %v, want none", keys(results))
	}
}

// A filter is a constraint, not a signal: no score may bring back an operation
// it excluded.
func TestFiltersExcludeRatherThanDemote(t *testing.T) {
	index := Build(petStore())

	readOnly := index.Search("pet", Filters{Effects: []domain.Effect{domain.EffectRead}}, 10)
	if len(readOnly) == 0 {
		t.Fatal("no read operations matched")
	}
	for _, result := range readOnly {
		if result.Effect != domain.EffectRead {
			t.Errorf("%s leaked past the effect filter with effect %q", result.Key, result.Effect)
		}
	}

	billing := index.Search("list", Filters{Tags: []string{"billing"}}, 10)
	for _, result := range billing {
		if len(result.Tags) == 0 || result.Tags[0] != "billing" {
			t.Errorf("%s leaked past the tag filter (tags %v)", result.Key, result.Tags)
		}
	}

	posts := index.Search("pets", Filters{Methods: []string{"post"}}, 10)
	for _, result := range posts {
		if result.Method != http.MethodPost {
			t.Errorf("%s leaked past the method filter", result.Key)
		}
	}
}

// An empty query with filters is browsing, which is more useful than nothing
// and still deterministic.
func TestEmptyQueryBrowsesTheFilteredSet(t *testing.T) {
	results := Build(petStore()).Search("", Filters{Tags: []string{"pets"}}, 10)
	if len(results) != 4 {
		t.Fatalf("results = %v, want the four pet operations", keys(results))
	}
	if results[0].Key != "default:DELETE:/pets/{petId}" {
		t.Errorf("browse order = %v, want it sorted by key when scores tie", keys(results))
	}
}

func TestLimitIsHonoured(t *testing.T) {
	results := Build(petStore()).Search("pet", Filters{}, 2)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestTagVocabulary(t *testing.T) {
	tags := Build(petStore()).Tags()
	want := []TagCount{{Tag: "pets", Count: 4}, {Tag: "billing", Count: 2}}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("tags = %+v, want %+v", tags, want)
	}
}

// Constitution IV: same catalog, same query, same bytes -- which is also what
// makes the recall benchmark mean anything.
func TestSearchIsDeterministic(t *testing.T) {
	first := Build(petStore())
	second := Build(petStore())

	for _, query := range []string{"pet", "list orders", "", "delete"} {
		a, err := json.Marshal(first.Search(query, Filters{}, 10))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(second.Search(query, Filters{}, 10))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("query %q ranked differently across builds:\n%s\n%s", query, a, b)
		}
	}
}

// A term in most documents carries no information, and a negative weight would
// let it push real matches down.
func TestUbiquitousTermDoesNotOutrankASpecificOne(t *testing.T) {
	index := Build(petStore())
	// "pets" appears in four of six documents; "refund" in one.
	results := index.Search("pets refund", Filters{}, 5)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Key != "default:POST:/orders/{orderId}/refund" {
		t.Errorf("top hit = %q, want the specific term to win (%v)", results[0].Key, keys(results))
	}
}

func TestEmptyIndexIsUsable(t *testing.T) {
	index := Build(nil)
	if index.Len() != 0 || len(index.Tags()) != 0 {
		t.Errorf("empty index = %+v", index)
	}
	if results := index.Search("anything", Filters{}, 5); len(results) != 0 {
		t.Errorf("results = %v, want none", keys(results))
	}
}
