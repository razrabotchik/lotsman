package searchindex

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/textnorm"
)

// FuzzSearch pins the two properties a search tool must have whatever a model
// sends: it does not fall over, and a filter it was given is never violated.
//
// The second is the security-relevant one. Filters are how a caller narrows to
// the operations it is allowed to think about, and a scoring path that could
// return an excluded operation would make the filter advisory.
func FuzzSearch(f *testing.F) {
	for _, seed := range []struct {
		query, tag, method, effect string
		limit                      int
	}{
		{query: "list pets", limit: 5},
		{query: "", tag: "pets", limit: 3},
		{query: "delete", method: "DELETE", limit: 10},
		{query: "pet", effect: "read", limit: 1},
		{query: strings.Repeat("pet ", 200), limit: 50},
		{query: "🙀 щука", limit: 5},
		{query: "\x00\x01", limit: 0},
		{query: "pets", tag: "PETS", method: "get", effect: "READ", limit: -1},
		{query: "*", limit: 1000},
	} {
		f.Add(seed.query, seed.tag, seed.method, seed.effect, seed.limit)
	}

	index := Build(petStore())

	f.Fuzz(func(t *testing.T, query, tag, method, effect string, limit int) {
		filters := Filters{}
		if tag != "" {
			filters.Tags = []string{tag}
		}
		if method != "" {
			filters.Methods = []string{method}
		}
		if effect != "" {
			filters.Effects = []domain.Effect{domain.Effect(effect)}
		}

		results := index.Search(query, filters, limit)

		if limit > 0 && len(results) > limit {
			t.Fatalf("got %d results for limit %d", len(results), limit)
		}
		if len(results) > index.Len() {
			t.Fatalf("got %d results from %d documents", len(results), index.Len())
		}

		seen := map[domain.OperationKey]bool{}
		previous := results
		for i := range results {
			result := &results[i]

			// No operation appears twice: a duplicate would mean a model paying
			// twice for one candidate.
			if seen[result.Key] {
				t.Fatalf("%s appeared twice", result.Key)
			}
			seen[result.Key] = true

			// Every filter that was given still holds.
			if tag != "" && !containsFold(result.Tags, tag) {
				t.Fatalf("%s has tags %v, which do not include the filter %q", result.Key, result.Tags, tag)
			}
			if method != "" && !strings.EqualFold(result.Method, method) {
				t.Fatalf("%s is %s, which is not the filtered method %q", result.Key, result.Method, method)
			}
			if effect != "" && !strings.EqualFold(string(result.Effect), effect) {
				t.Fatalf("%s is %s, which is not the filtered effect %q", result.Key, result.Effect, effect)
			}

			// Ranking is monotonic: a later result never scores higher.
			if i > 0 && previous[i-1].Score < result.Score {
				t.Fatalf("results are not ordered by score: %v before %v", previous[i-1], result)
			}
		}
	})
}

// FuzzTokens pins the tokenizer both the index and the effect scanner rely on:
// it terminates, it produces no empty tokens, and it lower-cases everything --
// a token with a capital in it would never match a query.
func FuzzTokens(f *testing.F) {
	for _, seed := range []string{
		"listPets", "/pets/{petId}", "getAPIKey", "", "🙀", "a_b-c.d",
		strings.Repeat("aB", 500), "\x00\x01\x02", "ЗаписьДанных", "v2.0",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		for _, token := range textnorm.Tokens(input) {
			if token == "" {
				t.Fatalf("empty token from %q", input)
			}
			if token != strings.ToLower(token) {
				t.Fatalf("token %q from %q is not lower-case", token, input)
			}
			if strings.ContainsAny(token, " \t\n/{}.-_") {
				t.Fatalf("token %q from %q still contains a separator", token, input)
			}
		}
	})
}
