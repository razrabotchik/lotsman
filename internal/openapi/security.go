package openapi

import (
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// coreSchemeTypes are the security scheme types lotsman's providers can
// satisfy (FR-58): API keys in a header, query or cookie, and HTTP basic or
// bearer. OAuth2 and OpenID Connect are an M4 concern, and mutual TLS is a
// transport arrangement rather than a credential lotsman can supply.
var coreSchemeTypes = map[string]bool{
	"apiKey": true,
	"http":   true,
}

// coreHTTPSchemes are the HTTP authentication schemes among those types.
var coreHTTPSchemes = map[string]bool{
	"basic":  true,
	"bearer": true,
}

// buildSecurity resolves an operation's effective security (FR-55/56).
//
// Inheritance is not "merge": an operation's own `security` replaces the
// document's entirely, and an explicit empty list is a statement -- "this
// endpoint is public" -- not an omission (pitfall #4). Only a nil list
// inherits.
//
// Alternatives are OR, requirements within one alternative are AND. Both are
// kept: collapsing them into "needs auth: yes/no" would lose exactly the
// information an auth policy needs to choose between them.
func buildSecurity(operation, root []*base.SecurityRequirement, schemes *orderedmap.Map[string, *v3.SecurityScheme]) []domain.SecurityAlternative {
	effective := operation
	if operation == nil {
		effective = root
	}

	out := make([]domain.SecurityAlternative, 0, len(effective))
	for _, alternative := range effective {
		if alternative == nil {
			continue
		}
		// An empty requirement object inside the list is OAS's way of saying
		// "or no authentication at all".
		if alternative.ContainsEmptyRequirement || orderedmap.Len(alternative.Requirements) == 0 {
			out = append(out, domain.SecurityAlternative{})
			continue
		}

		var converted domain.SecurityAlternative
		for name, scopes := range alternative.Requirements.FromOldest() {
			converted.Requirements = append(converted.Requirements, describeScheme(name, scopes, schemes))
		}
		out = append(out, converted)
	}
	return out
}

// describeScheme resolves a requirement against the document's declared
// schemes. A name with no declaration is not satisfiable: the document asks
// for a credential it never defines.
func describeScheme(name string, scopes []string, schemes *orderedmap.Map[string, *v3.SecurityScheme]) domain.SecurityRequirement {
	requirement := domain.SecurityRequirement{Scheme: name, Scopes: append([]string(nil), scopes...)}

	scheme, ok := schemes.Get(name)
	if !ok || scheme == nil {
		return requirement
	}
	requirement.Type = scheme.Type
	requirement.In = scheme.In
	requirement.Name = scheme.Name
	requirement.HTTP = scheme.Scheme

	if !coreSchemeTypes[scheme.Type] {
		return requirement
	}
	if scheme.Type == "http" && !coreHTTPSchemes[lower(scheme.Scheme)] {
		return requirement
	}
	if scheme.Type == "apiKey" && scheme.Name == "" {
		return requirement
	}
	requirement.Satisfiable = true
	return requirement
}

// securityVerdict turns the alternatives into the two facts the rest of the
// pipeline needs: whether the operation can ever be authenticated, and
// whether it needs authentication at all.
type securityVerdict struct {
	// unsatisfiable means no alternative could be met by any provider lotsman
	// has or will have in this release: the operation is rejected, because a
	// tool that can never be called is worse than no tool.
	unsatisfiable bool
	// needsAuth means at least one credential must be supplied before a call.
	// Until the auth stage exists, that is an execution blocker.
	needsAuth bool
}

func evaluateSecurity(alternatives []domain.SecurityAlternative) securityVerdict {
	if len(alternatives) == 0 {
		return securityVerdict{}
	}

	var verdict securityVerdict
	verdict.needsAuth = true
	for _, alternative := range alternatives {
		if len(alternative.Requirements) == 0 {
			// A public alternative: the operation can be called without
			// credentials, whatever else is on offer.
			return securityVerdict{}
		}
		if alternative.Satisfiable() {
			return verdict
		}
	}
	verdict.unsatisfiable = true
	return verdict
}

func lower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}
