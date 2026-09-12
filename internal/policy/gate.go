package policy

import (
	"fmt"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// Config is the execution policy in force for a run. The zero value is the
// documented default: read-only (FR-40, docs/spec.md 5.1
// `execution.defaultPolicy: read-only`).
//
// Allow/deny rules by namespace, operationKey, tag and effect (FR-41) arrive
// with the config layer; this is the gate they will refine, not replace.
type Config struct {
	// AllowMutations corresponds to `execution.allowMutations`. Without it,
	// only operations whose effect is read may reach the network.
	AllowMutations bool
}

// Verdict is the gate's answer for one operation.
type Verdict struct {
	Allowed bool
	// Reason is empty when allowed, and a stable code otherwise so a report
	// can group refusals and a client can branch on them.
	Reason  domain.ReasonCode
	Message string
}

// Evaluate applies the read-only gate to an effect decision.
//
// The rule is short on purpose: `read` passes, everything else needs
// mutations to be enabled. An unset effect counts as unknown, so a caller
// that forgets to classify gets a refusal rather than a free pass.
// `unknown` is not a third state that gets the benefit of the doubt -- it is refused with its own reason code, because
// "we could not tell" and "we know it writes" are different things to an
// operator reading a report, even though both are blocked.
func (c Config) Evaluate(effect domain.EffectDecision) Verdict {
	if effect.IsRead() {
		return Verdict{Allowed: true}
	}
	if c.AllowMutations {
		return Verdict{Allowed: true}
	}
	if effect.Effect == domain.EffectUnknown || effect.Effect == "" {
		return Verdict{
			Reason: domain.ReasonPolicyUnknownEffectBlocked,
			Message: "effect could not be determined; enable execution.allowMutations " +
				"or classify the operation explicitly before calling it",
		}
	}
	return Verdict{
		Reason:  domain.ReasonPolicyMutationBlocked,
		Message: fmt.Sprintf("effect is %s; enable execution.allowMutations to call it", effect.Effect),
	}
}

// Annotations are the conservative MCP tool hints derived from an effect
// (FR-42). They are hints for a client's UX: the gate above does not consult
// them and cannot be relaxed by them (FR-43).
type Annotations struct {
	ReadOnly    bool
	Destructive bool
}

// AnnotationsFor derives hints from an effect. Anything that is not a known
// read is advertised as potentially destructive, including `unknown`: a
// client must not be told an operation is safe on the strength of a method.
func AnnotationsFor(effect domain.EffectDecision) Annotations {
	if effect.IsRead() {
		return Annotations{ReadOnly: true}
	}
	return Annotations{Destructive: true}
}
