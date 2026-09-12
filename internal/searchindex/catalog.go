package searchindex

import (
	"github.com/razrabotchik/lotsman/internal/catalog"
)

// FromCatalog builds the index for a catalog snapshot.
//
// The direction of this dependency is deliberate: a catalog knows nothing about
// search, and an index is derived from a catalog. Only published tools are
// indexed -- an operation lotsman refused to translate is not something an
// agent should be able to find, because finding it could only lead to a
// refusal.
func FromCatalog(cat *catalog.Catalog) *Index {
	if cat == nil {
		return Build(nil)
	}
	documents := make([]Document, 0, len(cat.Tools))
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		documents = append(documents, Document{
			Key:      tool.OperationKey,
			ToolName: tool.Name,
			Method:   tool.Method,
			Path:     tool.PathTemplate,
			Summary:  tool.Description,
			Tags:     tool.Tags,
			Effect:   tool.Effect.Effect,
		})
	}
	return Build(documents)
}
