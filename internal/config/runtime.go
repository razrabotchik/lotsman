package config

// Runtime is the effective configuration after precedence is applied.
//
// FR-62 fixes the order: defaults < file < environment < flags. Secrets are
// deliberately outside it -- only the *reference* is configured, and a
// reference resolved from three different sources would be three different
// ways to point at the wrong credential.
type Runtime struct {
	AllowMutations bool
	BaseURL        string
	// AllowedOrigins is the egress allowlist. The base URL is added to it, so
	// the common single-origin case needs no configuration at all.
	AllowedOrigins       []string
	AllowPrivateNetworks bool
	AuthProfiles         map[string]Profile
}

// Overrides carries what the command line said. A nil field means the flag was
// not given, which is what keeps "not set" distinct from "set to false" -- the
// distinction the whole precedence model rests on.
type Overrides struct {
	AllowMutations *bool
	BaseURL        *string
}

// Resolve applies the precedence order to produce the effective runtime.
//
// The environment layer is intentionally narrow: LOTSMAN_* bindings exist for
// operational settings, never for credentials, because an environment variable
// holding a token is exactly the literal secret FR-59 forbids in a flag.
func Resolve(file *File, env Environment, flags Overrides) Runtime {
	runtime := Runtime{} // defaults: read-only, no base URL, no credentials

	if file != nil {
		runtime.AllowMutations = file.Execution.AllowMutations
		runtime.BaseURL = file.Execution.BaseURL
		runtime.AllowedOrigins = append([]string(nil), file.Execution.AllowedOrigins...)
		runtime.AllowPrivateNetworks = file.Execution.AllowPrivateNetworks
		if len(file.AuthProfiles) > 0 {
			runtime.AuthProfiles = make(map[string]Profile, len(file.AuthProfiles))
			for name, profile := range file.AuthProfiles {
				runtime.AuthProfiles[name] = profile
			}
		}
	}

	if env.BaseURL != "" {
		runtime.BaseURL = env.BaseURL
	}

	if flags.AllowMutations != nil {
		runtime.AllowMutations = *flags.AllowMutations
	}
	if flags.BaseURL != nil && *flags.BaseURL != "" {
		runtime.BaseURL = *flags.BaseURL
	}
	return runtime
}

// Environment is the subset of LOTSMAN_* bindings that participate in
// precedence.
type Environment struct {
	BaseURL string
}
