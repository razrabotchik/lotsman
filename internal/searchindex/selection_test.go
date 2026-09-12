package searchindex_test

import (
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/searchindex"
)

func tagged(key, path, opID, tag string) domain.Operation {
	return domain.Operation{
		Key:               domain.OperationKey(key),
		SourceOperationID: opID,
		Method:            "GET",
		PathTemplate:      path,
		Tags:              []string{tag},
		Effect:            domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod},
		Support:           domain.SupportStatus{Level: domain.SupportSupported},
	}
}

// An operation the operator took off the surface must not be findable. A
// search result that can only end in "no such tool" is worse than no result:
// it tells a model something exists that it cannot have.
func TestExcludedOperationsAreNotInTheIndexOrTheTagVocabulary(t *testing.T) {
	ops := []domain.Operation{
		tagged("ns:GET:/droplets", "/droplets", "listDroplets", "droplets"),
		tagged("ns:GET:/invoices", "/invoices", "listInvoices", "billing"),
	}
	cat := catalog.Build("d", ops, catalog.Options{IncludeTags: []string{"droplets"}})
	index := searchindex.FromCatalog(&cat)

	if index.Len() != 1 {
		t.Fatalf("index has %d documents, want 1", index.Len())
	}
	if results := index.Search("invoices billing", searchindex.Filters{}, 10); len(results) != 0 {
		t.Errorf("search found an excluded operation: %+v", results)
	}
	for _, tag := range index.Tags() {
		if tag.Tag == "billing" {
			t.Errorf("list_tags offers %q, which no published operation carries", tag.Tag)
		}
	}
}
