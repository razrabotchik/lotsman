package catalog

import (
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// The overlay is everything the operator says about a document the document
// does not say about itself: which operations are part of the surface at all
// (selection) and what individual ones really do (overrides).
//
// It can make the runtime stricter freely and looser only by being explicit,
// and it can never make an operation lotsman refused to translate callable:
// an override classifies, hides or binds a credential, and translation is not
// a matter of opinion.

// overlayDecision is what the overrides said about one operation. Neither
// field belongs on domain.Operation: they are the operator's decisions, not
// properties of the document, and the IR must keep saying what the document
// said.
type overlayDecision struct {
	disabled    bool
	authProfile string
}

// Overlaid is a document as the operator's configuration leaves it.
type Overlaid struct {
	// Operations is a copy with the overridden effects applied. The caller's
	// slice is untouched.
	Operations []domain.Operation
	// Excluded is why a supported operation is not on the surface, keyed by
	// operation. An excluded operation is still in Operations: the report
	// accounts for everything the document contains, and an operation that
	// vanished from both would be a filter hiding its own effect.
	Excluded map[domain.OperationKey]domain.ReasonCode
	// authProfiles pins credential selection per operation.
	authProfiles map[domain.OperationKey]string
}

// Overlay applies the operator's overrides and selection. Build calls it, and
// so can a command that reports on a document without publishing one: the two
// must not disagree about what the configuration means.
func Overlay(operations []domain.Operation, opts Options) Overlaid {
	effective, decisions := applyOverrides(operations, opts.Overrides)
	filter := newSelection(opts.IncludeTags)

	out := Overlaid{
		Operations:   effective,
		Excluded:     map[domain.OperationKey]domain.ReasonCode{},
		authProfiles: map[domain.OperationKey]string{},
	}
	for i := range effective {
		op := &effective[i]
		// Only a supported operation can be excluded: for the rest, lotsman
		// already refused for a reason of its own, and adding the operator's
		// on top would describe a decision that never got to be made.
		if op.Support.Level != domain.SupportSupported {
			continue
		}
		switch {
		case decisions[op.Key].disabled:
			out.Excluded[op.Key] = domain.ReasonDisabledByOverride
		case !filter.selects(op):
			out.Excluded[op.Key] = domain.ReasonExcludedBySelection
		}
		if pinned := decisions[op.Key].authProfile; pinned != "" {
			out.authProfiles[op.Key] = pinned
		}
	}
	return out
}

// ValidateOverrides checks the overlay against the document it will be
// applied to. It is separate from Build because a mistake here is worth an
// exit code rather than a quietly smaller catalog: an override that matches
// nothing is a security control the operator believes is in force.
func ValidateOverrides(operations []domain.Operation, overrides []config.OperationOverride) error {
	claimed := make(map[domain.OperationKey]int, len(overrides))

	for i := range overrides {
		match := overrides[i].Match
		var matched []domain.OperationKey
		for j := range operations {
			if matches(match, &operations[j]) {
				matched = append(matched, operations[j].Key)
			}
		}

		switch len(matched) {
		case 0:
			return errs.Errorf(errs.ClassUsage,
				"config: operationOverrides[%d]: %s matches no operation in this document", i, describe(match))
		case 1:
		default:
			// docs/spec.md 5.2: a match on a colliding operationId is an
			// error. Applying it to all of them would be a guess about which
			// one the operator reviewed.
			return errs.Errorf(errs.ClassUsage,
				"config: operationOverrides[%d]: %s matches %d operations (%s); match by method and path instead",
				i, describe(match), len(matched), joinKeys(matched))
		}

		key := matched[0]
		if first, seen := claimed[key]; seen {
			return errs.Errorf(errs.ClassUsage,
				"config: operationOverrides[%d] and [%d] both match %s; one operation, one override",
				first, i, key)
		}
		claimed[key] = i
	}
	return nil
}

// applyOverrides returns a copy of operations with the overlay applied, plus
// what it decided per operation. The input is never mutated: a catalog built
// twice from the same document must be the same catalog (Principle IV), and a
// caller that reuses its operations would otherwise get a different one.
//
// Unmatched or ambiguous overrides are ignored here; ValidateOverrides is
// where they are refused, before anything is served.
func applyOverrides(operations []domain.Operation, overrides []config.OperationOverride) ([]domain.Operation, map[domain.OperationKey]overlayDecision) {
	if len(overrides) == 0 {
		return operations, nil
	}

	out := make([]domain.Operation, len(operations))
	copy(out, operations)
	decisions := make(map[domain.OperationKey]overlayDecision, len(overrides))

	for i := range overrides {
		override := &overrides[i]
		for j := range out {
			op := &out[j]
			if !matches(override.Match, op) {
				continue
			}
			if override.Effect != "" {
				// The one path in the codebase that produces an explicit
				// effect: a human read the operation and said what it does
				// (docs/spec.md 4.7). The scanner's warnings are kept, since
				// "this GET is named rebuild" stays worth reading even after
				// someone decided it is a read anyway.
				op.Effect = domain.EffectDecision{
					Effect:     domain.Effect(override.Effect),
					Source:     domain.EffectSourceLocalOverride,
					Confidence: domain.ConfidenceExplicit,
					Warnings:   op.Effect.Warnings,
				}
			}
			decision := decisions[op.Key]
			if override.Enabled != nil && !*override.Enabled {
				decision.disabled = true
			}
			if override.AuthProfile != "" {
				decision.authProfile = override.AuthProfile
			}
			decisions[op.Key] = decision
		}
	}
	return out, decisions
}

// matches reports whether one match specification names this operation.
// Method comparison is case-insensitive; the path template is compared
// verbatim, because a path that differs by a character is a different path.
func matches(match config.Match, op *domain.Operation) bool {
	if match.OperationID != "" {
		return op.SourceOperationID == match.OperationID
	}
	return strings.EqualFold(op.Method, match.Method) && op.PathTemplate == match.Path
}

func describe(match config.Match) string {
	if match.OperationID != "" {
		return "operationId " + match.OperationID
	}
	return strings.ToUpper(match.Method) + " " + match.Path
}

func joinKeys(keys []domain.OperationKey) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = string(key)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// selection is the publication filter (catalog.includeTags). It decides what
// is on the surface at all -- a different statement from the policy's "you
// may not call this", and it is why an excluded operation leaves the catalog
// entirely instead of staying as a tool that always refuses.
type selection struct {
	includeTags map[string]bool
}

func newSelection(tags []string) selection {
	if len(tags) == 0 {
		return selection{}
	}
	include := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if tag != "" {
			include[tag] = true
		}
	}
	return selection{includeTags: include}
}

// selects reports whether an operation is published. With no filter
// configured everything is; with one, an operation carrying no tag at all is
// out -- the operator named a surface, and "untagged" is not on it.
//
// Matching is against the sanitized tags, which is the vocabulary the report
// and list_tags show. A filter that had to be written against the raw
// document instead would be a filter nobody could read off lotsman's own
// output.
func (s selection) selects(op *domain.Operation) bool {
	if len(s.includeTags) == 0 {
		return true
	}
	for _, tag := range sanitizeTags(op.Tags) {
		if s.includeTags[tag] {
			return true
		}
	}
	return false
}
