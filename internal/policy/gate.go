package policy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// anyMatch reports whether any rule in the list names this operation.
func anyMatch(rules []Rule, subject Subject) bool {
	for _, rule := range rules {
		if rule.matches(subject) {
			return true
		}
	}
	return false
}

// Config is the execution policy in force for a run. The zero value is the
// documented default: read-only, no rules (FR-40, docs/spec.md 5.1
// `execution.defaultPolicy: read-only`).
type Config struct {
	// AllowMutations corresponds to `execution.allowMutations`. Without it,
	// only operations whose effect is read may reach the network.
	AllowMutations bool
	// Allow and Deny are the operator's rules (FR-41). They are two lists
	// rather than one ordered one because first-match-wins would make the
	// safety of a configuration depend on the order somebody pasted it in.
	Allow []Rule
	Deny  []Rule
}

// Rule matches operations on the four axes FR-41 names. The fields inside one
// rule are ANDed, a list of rules is ORed, and an empty rule matches
// everything -- which the config loader refuses rather than interprets.
type Rule struct {
	Namespace    string
	OperationKey domain.OperationKey
	Tag          string
	Effect       domain.Effect
}

// matches reports whether the rule names this operation.
func (r Rule) matches(subject Subject) bool {
	if r.Namespace != "" && r.Namespace != subject.Key.Namespace() {
		return false
	}
	if r.OperationKey != "" && r.OperationKey != subject.Key {
		return false
	}
	if r.Effect != "" && r.Effect != subject.Effect.Effect {
		return false
	}
	if r.Tag != "" && !slices.Contains(subject.Tags, r.Tag) {
		return false
	}
	return true
}

// describe names the axes a rule matched on, so a refusal can say which line
// of the configuration produced it.
func (r Rule) describe() string {
	var parts []string
	for _, part := range [][2]string{
		{"namespace", r.Namespace},
		{"operationKey", string(r.OperationKey)},
		{"tag", r.Tag},
		{"effect", string(r.Effect)},
	} {
		if part[1] != "" {
			parts = append(parts, part[0]+"="+part[1])
		}
	}
	return strings.Join(parts, ", ")
}

// Subject is the operation a verdict is about. It is spelled out rather than
// taking a catalog tool or a domain.Operation because the gate runs at three
// different points in the pipeline, on three different representations, and
// must reach the same answer at all of them.
type Subject struct {
	Key    domain.OperationKey
	Tags   []string
	Effect domain.EffectDecision
}

// SubjectOf is the gate's view of a parsed operation.
func SubjectOf(op *domain.Operation) Subject {
	return Subject{Key: op.Key, Tags: op.Tags, Effect: op.Effect}
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
func (c Config) Evaluate(subject Subject) Verdict {
	// A deny rule is the operator saying "not this one", and it applies
	// whatever the effect: refusing a read on request can never be the unsafe
	// answer, and an operator who has to enable mutations to hide an endpoint
	// would have to make things worse to make them better.
	for _, rule := range c.Deny {
		if rule.matches(subject) {
			return Verdict{
				Reason:  domain.ReasonPolicyDeniedByRule,
				Message: "refused by execution.denyRules (" + rule.describe() + ")",
			}
		}
	}

	effect := subject.Effect
	if effect.IsRead() {
		return Verdict{Allowed: true}
	}

	// The allow list gates mutations, which is what FR-41 says it does: a
	// read is permitted by the default policy and does not need a rule to
	// license it. Only a deny rule can take one away.
	if len(c.Allow) > 0 && !anyMatch(c.Allow, subject) {
		return Verdict{
			Reason: domain.ReasonPolicyNotAllowedByRule,
			Message: fmt.Sprintf("effect is %s and execution.allowRules does not list this operation; "+
				"add a rule that matches it or remove the allow list", effect.Effect),
		}
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
