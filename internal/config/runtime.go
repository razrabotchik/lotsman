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
	// InteractiveApproval is never empty after Resolve: the documented
	// default (`always`) is applied here, so no downstream caller has to
	// decide what an unset approval mode means.
	InteractiveApproval Approval
	AllowRules          []Rule
	DenyRules           []Rule
	// IncludeTags is the publication filter; empty publishes everything.
	IncludeTags []string
	// Overrides are the operator's per-operation statements, in file order.
	Overrides []OperationOverride
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
	Approval       *Approval
}

// Resolve applies the precedence order to produce the effective runtime.
//
// The environment layer is intentionally narrow: LOTSMAN_* bindings exist for
// operational settings, never for credentials, because an environment variable
// holding a token is exactly the literal secret FR-59 forbids in a flag.
func Resolve(file *File, env Environment, flags Overrides) Runtime {
	// defaults: read-only, no base URL, no credentials, and a mutation asks
	// before it happens.
	runtime := Runtime{InteractiveApproval: ApprovalAlways}

	if file != nil {
		runtime.AllowMutations = file.Execution.AllowMutations
		if file.Execution.InteractiveApproval != "" {
			runtime.InteractiveApproval = file.Execution.InteractiveApproval
		}
		runtime.AllowRules = append([]Rule(nil), file.Execution.AllowRules...)
		runtime.DenyRules = append([]Rule(nil), file.Execution.DenyRules...)
		runtime.IncludeTags = append([]string(nil), file.Catalog.IncludeTags...)
		runtime.Overrides = append([]OperationOverride(nil), file.OperationOverrides...)
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
	if flags.Approval != nil {
		runtime.InteractiveApproval = *flags.Approval
	}
	return runtime
}

// Environment is the subset of LOTSMAN_* bindings that participate in
// precedence.
type Environment struct {
	BaseURL string
}
