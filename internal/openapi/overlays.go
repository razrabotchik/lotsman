package openapi

import (
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/overlay"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// OverlayDocument is one operator-supplied OpenAPI Overlay, named for messages.
type OverlayDocument struct {
	Path  string
	Bytes []byte
}

// ApplyOverlays applies OpenAPI Overlay documents, in order, to the root
// document's bytes (feature 008). It runs before Parse, so everything an
// overlay writes -- a `$ref` included -- meets the same confinement and
// budgets as what the vendor wrote.
//
// libopenapi's convenience wrappers also build a document, with a default
// configuration lotsman does not use; overlay.Apply works on bytes alone.
// An action whose target matches nothing is an error, not a warning: an
// overlay the operator believes is applied has to be applied.
func ApplyOverlays(spec []byte, overlays []OverlayDocument) ([]byte, error) {
	out := spec
	for _, doc := range overlays {
		parsed, err := libopenapi.NewOverlayDocument(doc.Bytes)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %w", doc.Path, err)
		}
		// libopenapi skips an action without a target without a word, so a
		// missing or misspelled `target:` would be an overlay that applies
		// nothing while the operator believes it applies. An overlay that
		// states no actions at all is the same silence.
		if len(parsed.Actions) == 0 {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: the overlay states no actions", doc.Path)
		}
		for i, action := range parsed.Actions {
			if action == nil || action.Target == "" {
				return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: actions[%d] has no target", doc.Path, i)
			}
		}
		result, err := overlay.Apply(out, parsed)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %w", doc.Path, err)
		}
		if len(result.Warnings) > 0 {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %s", doc.Path, result.Warnings[0].String())
		}
		out = result.Bytes
	}
	return out, nil
}
