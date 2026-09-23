package config

import (
	"os"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// APIVersion is the configuration schema this build understands. An unknown
// major version is refused rather than partially honoured: a config lotsman
// half-reads is a security control half-applied (docs/spec.md 5.2).
const APIVersion = "lotsman.dev/v1alpha1"

// Scheme is an authentication scheme a profile implements. These are the core
// providers of FR-58; OAuth2 modes arrive in M4.
type Scheme string

// Supported schemes.
const (
	SchemeAPIKey Scheme = "apikey"
	SchemeBasic  Scheme = "basic"
	SchemeBearer Scheme = "bearer"
)

// Location is where an API key is carried.
type Location string

// API key locations.
const (
	InHeader Location = "header"
	InQuery  Location = "query"
	InCookie Location = "cookie"
)

// Profile is one credential lotsman can present. It holds references only.
type Profile struct {
	Scheme Scheme   `yaml:"scheme"`
	In     Location `yaml:"in,omitempty"`   // apikey: where the key goes
	Name   string   `yaml:"name,omitempty"` // apikey: the header/query/cookie name

	TokenRef    SecretRef `yaml:"tokenRef,omitempty"`    // bearer, apikey
	UsernameRef SecretRef `yaml:"usernameRef,omitempty"` // basic
	PasswordRef SecretRef `yaml:"passwordRef,omitempty"` // basic

	// Satisfies names the document's security schemes this profile can meet.
	// When empty, the profile's own key is matched against the scheme name,
	// which is the common case and needs no ceremony.
	Satisfies []string `yaml:"satisfies,omitempty"`
}

// Approval is when a call that changes something asks a human first (FR-44).
// It is a UX mechanism and never a security boundary: the protocol cannot
// promise a person saw the prompt, and a client may answer it on its own
// (FR-44a). The boundary is the policy gate, which has already run by the
// time anyone is asked.
type Approval string

// Approval modes. The default is Always, which costs nothing while mutations
// are disabled and is the safe answer the moment they are not.
const (
	ApprovalAlways           Approval = "always"
	ApprovalClientCapability Approval = "client-capability"
	ApprovalNever            Approval = "never"
)

// Valid reports whether the mode is one lotsman implements.
func (a Approval) Valid() bool {
	switch a {
	case ApprovalAlways, ApprovalClientCapability, ApprovalNever:
		return true
	default:
		return false
	}
}

// Rule matches operations by the four axes FR-41 names. Fields inside one
// rule are ANDed; a list of rules is ORed. An empty rule matches everything,
// which is a mistake in a deny list and a no-op in an allow list, so it is
// refused at load time rather than interpreted.
type Rule struct {
	Namespace    string `yaml:"namespace,omitempty"`
	OperationKey string `yaml:"operationKey,omitempty"`
	Tag          string `yaml:"tag,omitempty"`
	Effect       string `yaml:"effect,omitempty"`
}

// Execution mirrors the execution section of the configuration file. Only the
// settings the runtime reads today are present; the rest of docs/spec.md 5.1
// arrives with the stages that need them.
type Execution struct {
	AllowMutations bool   `yaml:"allowMutations,omitempty"`
	BaseURL        string `yaml:"baseURL,omitempty"`
	// InteractiveApproval is when a mutating call asks before it happens.
	// Empty means the documented default, `always`.
	InteractiveApproval Approval `yaml:"interactiveApproval,omitempty"`
	// AllowRules and DenyRules refine `allowMutations` (FR-41). A deny always
	// wins; a non-empty allow list means an unmatched operation is refused.
	AllowRules []Rule `yaml:"allowRules,omitempty"`
	DenyRules  []Rule `yaml:"denyRules,omitempty"`
	// AllowedOrigins is the egress allowlist (FR-32). An origin authored by
	// the specification is never authorization by itself.
	AllowedOrigins []string `yaml:"allowedOrigins,omitempty"`
	// AllowPrivateNetworks permits an allowed origin whose *hostname*
	// resolves into a private or link-local range. An origin written as an
	// address needs no such permission.
	AllowPrivateNetworks bool `yaml:"allowPrivateNetworks,omitempty"`
}

// Transport is how a client reaches lotsman (docs/spec.md 5.1).
type Transport string

// Transports. stdio is the local one and the default: one client, one
// process, stdout reserved for protocol frames (FR-68). http is the
// stateless Streamable HTTP profile of MCP 2026-07-28 (FR-69) — sessionless,
// which is why the deployment it enables needs no sticky routing.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Valid reports whether the transport is one this build serves.
func (t Transport) Valid() bool {
	switch t {
	case TransportStdio, TransportHTTP:
		return true
	default:
		return false
	}
}

// Server mirrors the server section of the configuration file: how lotsman is
// reached, as distinct from what it may do once reached. Nothing in here can
// widen a policy; the two questions are answered by different sections on
// purpose.
type Server struct {
	// Transport is stdio unless stated. A default that opens a socket would
	// be a default that changes the threat model.
	Transport Transport `yaml:"transport,omitempty"`
	// Listen is the bind address of the HTTP transport, loopback by default
	// (FR-70). A runtime whose targets are named by an untrusted document
	// does not reach a public interface by omission.
	Listen string `yaml:"listen,omitempty"`
	// LogLevel is the file layer of the same setting --log-level carries.
	LogLevel string `yaml:"logLevel,omitempty"`
	// AllowedOrigins are the browser origins permitted to reach the endpoint
	// (FR-71). A request carrying no Origin at all is not a browser request
	// and is judged by the other guards instead.
	AllowedOrigins []string `yaml:"allowedOrigins,omitempty"`
	// AllowedHosts, when set, is the exact set of Host headers accepted. Left
	// empty, the transport keeps the SDK's rebinding protection and nothing
	// more, which is the right answer for a loopback bind and not a claim
	// about a deployment behind a proxy.
	AllowedHosts []string `yaml:"allowedHosts,omitempty"`
	// DrainTimeout bounds graceful shutdown (FR-75), written as a Go duration
	// ("10s"). A drain that outlives its budget is a hung deployment; a drain
	// of zero is a dropped mutation.
	DrainTimeout string `yaml:"drainTimeout,omitempty"`
	// AllowUnauthenticatedPublicBind is the explicitly dangerous opt-in FR-70
	// permits, spelled out rather than abbreviated: it is the only way to put
	// an endpoint nothing authenticates on a public interface.
	AllowUnauthenticatedPublicBind bool `yaml:"allowUnauthenticatedPublicBind,omitempty"`
}

// Catalog mirrors the catalog section: what gets published, before any
// question of what may be called.
type Catalog struct {
	// IncludeTags publishes only the operations carrying at least one of
	// these tags (docs/spec.md 5.1). An empty list publishes everything.
	IncludeTags []string `yaml:"includeTags,omitempty"`
}

// Match identifies the operations an override applies to. Either an
// `operationId` or a method+path pair -- never a mix, and never a fragment:
// half a coordinate would match by accident (docs/spec.md 5.2).
type Match struct {
	OperationID string `yaml:"operationId,omitempty"`
	Method      string `yaml:"method,omitempty"`
	Path        string `yaml:"path,omitempty"`
}

// OperationOverride is the operator's reviewed statement about one operation:
// what it does, whether to publish it at all, and which credential it uses.
//
// It is the only thing that can raise an effect from `inferred` to `explicit`
// (docs/spec.md 4.7), which is why it lives in the operator's configuration
// and not in anything the document can influence.
type OperationOverride struct {
	Match Match `yaml:"match"`
	// Effect states what the operation actually does. Empty leaves lotsman's
	// own classification in place.
	Effect string `yaml:"effect,omitempty"`
	// Enabled: false removes the operation from publication entirely. Nil
	// means "not stated", which is the difference between an operator who
	// wants it hidden and one who never mentioned it.
	Enabled *bool `yaml:"enabled,omitempty"`
	// AuthProfile pins which configured credential satisfies this operation,
	// which is how an ambiguous_security refusal is resolved by decision
	// rather than by deleting a profile the rest of the catalog needs.
	AuthProfile string `yaml:"authProfile,omitempty"`
}

// File is a parsed configuration document.
type File struct {
	APIVersion         string              `yaml:"apiVersion"`
	Kind               string              `yaml:"kind,omitempty"`
	Catalog            Catalog             `yaml:"catalog,omitempty"`
	Execution          Execution           `yaml:"execution,omitempty"`
	Server             Server              `yaml:"server,omitempty"`
	AuthProfiles       map[string]Profile  `yaml:"authProfiles,omitempty"`
	OperationOverrides []OperationOverride `yaml:"operationOverrides,omitempty"`
}

// Load reads and validates a configuration file.
func Load(path string) (*File, error) {
	// #nosec G304 -- the path is an operator-supplied CLI argument.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "config: cannot read %s", path)
	}
	return Parse(data)
}

// Parse decodes and validates a configuration document.
//
// Decoding is strict: an unknown field is an error, not a shrug. A typo in
// `allowMutations` that silently leaves it false is a pleasant failure; the
// same typo in a field that gates something open is not, and the parser
// cannot tell the two apart.
func Parse(data []byte) (*File, error) {
	var file File
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "config: %w", err)
	}
	if err := file.validate(); err != nil {
		return nil, err
	}
	return &file, nil
}

func (f *File) validate() error {
	if f.APIVersion == "" {
		return errs.Errorf(errs.ClassUsage, "config: apiVersion is required (expected %s)", APIVersion)
	}
	if f.APIVersion != APIVersion {
		return errs.Errorf(errs.ClassUsage,
			"config: apiVersion %q is not understood by this build (expected %s)", f.APIVersion, APIVersion)
	}

	for _, name := range sortedNames(f.AuthProfiles) {
		profile := f.AuthProfiles[name]
		if err := validateProfile(name, &profile); err != nil {
			return err
		}
	}
	if err := validateApproval(f.Execution.InteractiveApproval); err != nil {
		return err
	}
	if err := validateServer(&f.Server); err != nil {
		return err
	}
	for _, list := range []struct {
		name  string
		rules []Rule
	}{{"allowRules", f.Execution.AllowRules}, {"denyRules", f.Execution.DenyRules}} {
		for i := range list.rules {
			if err := validateRule(list.name, i, &list.rules[i]); err != nil {
				return err
			}
		}
	}
	for i := range f.OperationOverrides {
		if err := f.validateOverride(i, &f.OperationOverrides[i]); err != nil {
			return err
		}
	}
	return nil
}

// validateServer refuses a server section this build cannot honour. A
// transport named but not implemented must fail here rather than fall back to
// stdio: an operator who wrote `transport: http` and got a pipe would learn
// about it from the absence of a socket.
func validateServer(server *Server) error {
	if server.Transport != "" && !server.Transport.Valid() {
		return errs.Errorf(errs.ClassUsage,
			"config: server.transport %q is not stdio or http", server.Transport)
	}
	if server.DrainTimeout != "" {
		d, err := time.ParseDuration(server.DrainTimeout)
		if err != nil {
			return errs.Errorf(errs.ClassUsage,
				"config: server.drainTimeout %q is not a duration (for example \"10s\")", server.DrainTimeout)
		}
		if d < 0 {
			return errs.Errorf(errs.ClassUsage, "config: server.drainTimeout %q is negative", server.DrainTimeout)
		}
	}
	return nil
}

func validateApproval(mode Approval) error {
	if mode == "" || mode.Valid() {
		return nil
	}
	return errs.Errorf(errs.ClassUsage,
		"config: execution.interactiveApproval %q is not always, client-capability or never", mode)
}

func validateRule(list string, index int, rule *Rule) error {
	if rule.Namespace == "" && rule.OperationKey == "" && rule.Tag == "" && rule.Effect == "" {
		return errs.Errorf(errs.ClassUsage,
			"config: execution.%s[%d] matches every operation; name a namespace, operationKey, tag or effect", list, index)
	}
	if rule.Effect != "" && !knownEffect(rule.Effect) {
		return errs.Errorf(errs.ClassUsage,
			"config: execution.%s[%d]: effect %q is not read, write, destructive or unknown", list, index, rule.Effect)
	}
	return nil
}

func (f *File) validateOverride(index int, override *OperationOverride) error {
	where := func(format string, args ...any) error {
		return errs.Errorf(errs.ClassUsage, "config: operationOverrides[%d]: "+format, append([]any{index}, args...)...)
	}

	match := override.Match
	switch {
	case match.OperationID != "" && (match.Method != "" || match.Path != ""):
		return where("match by operationId or by method+path, not both")
	case match.OperationID == "" && match.Method == "" && match.Path == "":
		return where("match needs an operationId or a method and a path")
	case match.OperationID == "" && (match.Method == "" || match.Path == ""):
		return where("match by method needs a path, and a path needs a method")
	}

	// An override that states nothing is a typo, and a typo in an override is
	// a security control that silently did not apply.
	if override.Effect == "" && override.Enabled == nil && override.AuthProfile == "" {
		return where("states nothing; set effect, enabled or authProfile")
	}
	if override.Effect != "" && !knownEffect(override.Effect) {
		return where("effect %q is not read, write, destructive or unknown", override.Effect)
	}
	if override.AuthProfile != "" {
		if _, configured := f.AuthProfiles[override.AuthProfile]; !configured {
			return where("authProfile %q is not one of the configured authProfiles", override.AuthProfile)
		}
	}
	return nil
}

// knownEffect reports whether s is one of the domain's effect classes. The
// vocabulary is shared rather than repeated: a configuration that accepts an
// effect the runtime does not know is a config that lies about being applied.
func knownEffect(s string) bool {
	switch domain.Effect(s) {
	case domain.EffectRead, domain.EffectWrite, domain.EffectDestructive, domain.EffectUnknown:
		return true
	default:
		return false
	}
}

func validateProfile(name string, profile *Profile) error {
	switch profile.Scheme {
	case SchemeBearer:
		if profile.TokenRef == "" {
			return errs.Errorf(errs.ClassUsage, "config: auth profile %q: bearer needs a tokenRef", name)
		}
	case SchemeAPIKey:
		if profile.Name == "" {
			return errs.Errorf(errs.ClassUsage, "config: auth profile %q: apikey needs the name it is sent under", name)
		}
		switch profile.In {
		case InHeader, InQuery, InCookie:
		default:
			return errs.Errorf(errs.ClassUsage,
				"config: auth profile %q: apikey needs in: header, query or cookie", name)
		}
		if profile.TokenRef == "" {
			return errs.Errorf(errs.ClassUsage, "config: auth profile %q: apikey needs a tokenRef", name)
		}
	case SchemeBasic:
		if profile.UsernameRef == "" || profile.PasswordRef == "" {
			return errs.Errorf(errs.ClassUsage,
				"config: auth profile %q: basic needs a usernameRef and a passwordRef", name)
		}
	default:
		return errs.Errorf(errs.ClassUsage,
			"config: auth profile %q: scheme %q is not one of apikey, basic, bearer", name, profile.Scheme)
	}

	// Every reference is validated at load time, so a literal secret is caught
	// before it can be used -- and before it reaches a log line.
	for _, ref := range []SecretRef{profile.TokenRef, profile.UsernameRef, profile.PasswordRef} {
		if ref == "" {
			continue
		}
		if _, err := ParseSecretRef(string(ref)); err != nil {
			return errs.Errorf(errs.ClassUsage, "config: auth profile %q: %w", name, err)
		}
	}
	return nil
}

func sortedNames(profiles map[string]Profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
