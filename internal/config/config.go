package config

import (
	"os"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"

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

// Execution mirrors the execution section of the configuration file. Only the
// settings the runtime reads today are present; the rest of docs/spec.md 5.1
// arrives with the stages that need them.
type Execution struct {
	AllowMutations bool   `yaml:"allowMutations,omitempty"`
	BaseURL        string `yaml:"baseURL,omitempty"`
}

// File is a parsed configuration document.
type File struct {
	APIVersion   string             `yaml:"apiVersion"`
	Kind         string             `yaml:"kind,omitempty"`
	Execution    Execution          `yaml:"execution,omitempty"`
	AuthProfiles map[string]Profile `yaml:"authProfiles,omitempty"`
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
	return nil
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
