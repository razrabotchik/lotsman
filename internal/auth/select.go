package auth

import (
	"fmt"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// Credential is a configured profile under the name the operator gave it.
type Credential struct {
	Name    string
	Profile config.Profile
	// Requirement is what the document asked for. The document is
	// authoritative about *where* a key goes -- it describes the API -- and
	// the profile is authoritative about the value.
	Requirement domain.SecurityRequirement
}

// Binding is the chosen way to authenticate one operation: every credential
// in it is presented together (the AND inside one security alternative).
type Binding struct {
	Credentials []Credential
}

// Empty reports whether the operation needs no credential at all.
func (b Binding) Empty() bool { return len(b.Credentials) == 0 }

// Verdict is the result of matching an operation's security against the
// configured profiles.
type Verdict struct {
	Binding Binding
	// Reason is empty when the operation can be called. Otherwise it is the
	// machine-readable code for why not.
	Reason  domain.ReasonCode
	Message string
}

// Bound reports whether the operation can be called.
func (v Verdict) Bound() bool { return v.Reason == "" }

// Profiles indexes the configured credentials by the document security scheme
// each one satisfies.
type Profiles struct {
	byScheme map[string][]Credential
	count    int
}

// NewProfiles builds the index. A profile satisfies the schemes it lists in
// `satisfies`, or -- the common case -- the scheme that shares its name.
func NewProfiles(configured map[string]config.Profile) Profiles {
	profiles := Profiles{byScheme: map[string][]Credential{}, count: len(configured)}

	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for every downstream decision

	for _, name := range names {
		profile := configured[name]
		schemes := profile.Satisfies
		if len(schemes) == 0 {
			schemes = []string{name}
		}
		for _, scheme := range schemes {
			profiles.byScheme[scheme] = append(profiles.byScheme[scheme], Credential{Name: name, Profile: profile})
		}
	}
	return profiles
}

// Select chooses the security alternative to satisfy, or explains why it
// cannot (FR-55-57).
//
// The one thing it may never do is pick arbitrarily: if two alternatives are
// both satisfiable, the operator has configured two ways in and nothing says
// which the API expects, so the operation is refused with ambiguous_security
// until one of the profiles is removed.
func Select(alternatives []domain.SecurityAlternative, profiles Profiles) Verdict {
	if len(alternatives) == 0 {
		return Verdict{}
	}

	var bound []Binding
	var missing []string
	for _, alternative := range alternatives {
		if len(alternative.Requirements) == 0 {
			// The document offers a way in with no credential at all; nothing
			// configured can beat that.
			return Verdict{}
		}
		binding, why := bind(alternative, profiles)
		if why != "" {
			missing = append(missing, why)
			continue
		}
		bound = append(bound, binding)
	}

	// Two alternatives that resolve to the same credentials are not a choice:
	// a document that offers `bearer_auth: []` and `bearer_auth: [scope]`
	// (DigitalOcean does) means the same token either way, and refusing it as
	// ambiguous would be pedantry with an outage attached.
	bound = distinct(bound)

	switch len(bound) {
	case 0:
		return Verdict{
			Reason: domain.ReasonAuthenticationNotImplemented,
			Message: fmt.Sprintf("no configured auth profile satisfies this operation (%s)",
				strings.Join(unique(missing), "; ")),
		}
	case 1:
		return Verdict{Binding: bound[0]}
	default:
		return Verdict{
			Reason: domain.ReasonAmbiguousSecurity,
			Message: fmt.Sprintf("%d configured profiles satisfy this operation and nothing says which the API expects; "+
				"leave exactly one in the configuration", len(bound)),
		}
	}
}

// bind tries to satisfy every requirement in one alternative, and says what
// stopped it if it could not.
func bind(alternative domain.SecurityAlternative, profiles Profiles) (binding Binding, why string) {
	for r := range alternative.Requirements {
		requirement := &alternative.Requirements[r]
		candidates := profiles.byScheme[requirement.Scheme]
		if len(candidates) == 0 {
			return Binding{}, fmt.Sprintf("no profile for scheme %q", requirement.Scheme)
		}
		var matched *Credential
		for i := range candidates {
			if mismatch := compatible(&candidates[i].Profile, requirement); mismatch != "" {
				continue
			}
			matched = &candidates[i]
			break
		}
		if matched == nil {
			return Binding{}, fmt.Sprintf("profile for %q does not match how the API carries it", requirement.Scheme)
		}
		credential := *matched
		credential.Requirement = *requirement
		binding.Credentials = append(binding.Credentials, credential)
	}
	return binding, ""
}

// compatible checks a profile against what the document says the API expects.
// A bearer profile cannot satisfy an API key, and an API key configured for a
// header cannot satisfy one the API reads from the query string: sending it
// anyway would leak the credential to a place the API never looks.
func compatible(profile *config.Profile, requirement *domain.SecurityRequirement) string {
	switch requirement.Type {
	case "apiKey":
		if profile.Scheme != config.SchemeAPIKey {
			return "the API wants an API key"
		}
		if profile.In != "" && string(profile.In) != requirement.In {
			return "the API carries the key in the " + requirement.In
		}
		if profile.Name != "" && profile.Name != requirement.Name {
			return "the API names the key " + requirement.Name
		}
		return ""
	case "http":
		switch strings.ToLower(requirement.HTTP) {
		case "bearer":
			if profile.Scheme != config.SchemeBearer {
				return "the API wants a bearer token"
			}
			return ""
		case "basic":
			if profile.Scheme != config.SchemeBasic {
				return "the API wants HTTP basic"
			}
			return ""
		default:
			return "unsupported HTTP authentication scheme " + requirement.HTTP
		}
	case "":
		// The document referenced a scheme it never defined; nothing can be
		// matched against a definition that does not exist.
		return "the document defines no such security scheme"
	default:
		return "unsupported security scheme type " + requirement.Type
	}
}

// distinct collapses bindings that present exactly the same credentials.
func distinct(bindings []Binding) []Binding {
	seen := map[string]bool{}
	out := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		names := make([]string, 0, len(binding.Credentials))
		for i := range binding.Credentials {
			names = append(names, binding.Credentials[i].Name)
		}
		sort.Strings(names)
		key := strings.Join(names, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, binding)
	}
	return out
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
