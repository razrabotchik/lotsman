package policy

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// mutationVerbs are the tokens that make a nominally read-only method
// suspicious (spec 4.7). Matching is by whole token, never by substring:
// "GET /updates" is a listing, "GET /cache/refresh" is not.
//
// The list is a heuristic and is allowed to be wrong in the safe direction.
// It cannot authorize anything -- its only power is to demote an inferred
// `read` to `unknown`, which the gate then refuses by default.
var mutationVerbs = map[string]bool{
	"activate": true, "approve": true, "cancel": true, "charge": true,
	"create": true, "deactivate": true, "delete": true, "deploy": true,
	"destroy": true, "disable": true, "drop": true, "enable": true,
	"execute": true, "import": true, "invite": true, "merge": true,
	"pay": true, "publish": true, "purchase": true, "purge": true,
	"reboot": true, "rebuild": true, "refresh": true, "refund": true,
	"reject": true, "remove": true, "reset": true, "restart": true,
	"revoke": true, "send": true, "sync": true, "trigger": true,
}

// Candidate is what effect classification looks at. It is spelled out rather
// than taking a domain.Operation so that the adapter can classify while it is
// still building one.
type Candidate struct {
	Method       string
	OperationID  string
	PathTemplate string
	Summary      string
}

// Decide classifies an operation's effect from the HTTP method, then runs the
// suspicious-verb scanner over its name and path (spec 4.7).
//
// Every decision here is `inferred`: only a reviewed override may state an
// effect explicitly, and only an explicit statement may bring a suspicious
// operation back to `read`.
func Decide(c Candidate) domain.EffectDecision {
	decision := domain.EffectDecision{
		Source:     domain.EffectSourceHTTPMethod,
		Confidence: domain.ConfidenceInferred,
	}

	switch strings.ToUpper(c.Method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		decision.Effect = domain.EffectRead
	case http.MethodDelete:
		decision.Effect = domain.EffectDestructive
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		// Not "write": a POST can charge a card or send a message, and the
		// method alone cannot tell which. A recipe or an override classifies
		// it; until then it is unknown and the gate treats it as a mutation.
		decision.Effect = domain.EffectUnknown
	default:
		decision.Effect = domain.EffectUnknown
	}

	if decision.Effect == domain.EffectRead {
		if verb, found := suspiciousVerb(c); found {
			decision.Effect = domain.EffectUnknown
			decision.Warnings = append(decision.Warnings, fmt.Sprintf(
				"%s %s: the method looks read-only but %q is a mutation-like verb; effect raised to unknown",
				strings.ToUpper(c.Method), c.PathTemplate, verb))
		}
	}
	return decision
}

// suspiciousVerb reports the first mutation-like token in the operation's
// identity. The summary is deliberately not scanned: it is untrusted prose
// from the document, and letting it change an effect would hand the spec
// author's copywriting a say in policy.
func suspiciousVerb(c Candidate) (string, bool) {
	for _, token := range append(tokenize(c.OperationID), tokenize(c.PathTemplate)...) {
		if mutationVerbs[token] {
			return token, true
		}
	}
	return "", false
}

// tokenize splits an identifier or path into lower-case words, breaking on
// non-alphanumeric characters and camelCase boundaries so that
// "rebuildCache", "rebuild_cache" and "/rebuild-cache" all yield "rebuild".
func tokenize(s string) []string {
	var tokens []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsUpper(r):
			// A camelCase boundary, but not inside an acronym: "APIKey"
			// breaks before "Key", not between "A" and "P".
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				flush()
			}
			current.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}
