package config

import "time"

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
	// Server is how the runtime is reached. It is never zero after Resolve --
	// the transport defaults to stdio, the bind to loopback and the drain to
	// a bounded wait -- so no caller downstream has to decide what an unset
	// listening socket means.
	Server ServerRuntime
}

// DefaultListen is the bind address of the HTTP transport when none is given.
// Loopback, and a port rather than a privileged one: FR-70 says a public
// interface is a decision, and a decision has to be written down somewhere a
// reader can find it.
const DefaultListen = "127.0.0.1:8080"

// DefaultDrainTimeout bounds graceful shutdown when the operator says nothing
// (FR-75).
const DefaultDrainTimeout = 10 * time.Second

// DefaultLogLevel is what the process logs at when nobody says otherwise.
const DefaultLogLevel = "info"

// ServerRuntime is the server section with its defaults applied and its
// durations parsed.
type ServerRuntime struct {
	Transport                      Transport
	Listen                         string
	LogLevel                       string
	AllowedOrigins                 []string
	AllowedHosts                   []string
	DrainTimeout                   time.Duration
	AllowUnauthenticatedPublicBind bool
}

// Overrides carries what the command line said. A nil field means the flag was
// not given, which is what keeps "not set" distinct from "set to false" -- the
// distinction the whole precedence model rests on.
type Overrides struct {
	AllowMutations *bool
	BaseURL        *string
	Approval       *Approval
	// The server layer. Each is a pointer for the same reason as the rest:
	// a flag nobody passed must not overwrite a file that said something.
	Transport                      *Transport
	Listen                         *string
	LogLevel                       *string
	DrainTimeout                   *time.Duration
	AllowUnauthenticatedPublicBind *bool
}

// Resolve applies the precedence order to produce the effective runtime.
//
// The environment layer is intentionally narrow: LOTSMAN_* bindings exist for
// operational settings, never for credentials, because an environment variable
// holding a token is exactly the literal secret FR-59 forbids in a flag.
func Resolve(file *File, env Environment, flags Overrides) Runtime {
	// defaults: read-only, no base URL, no credentials, a mutation asks
	// before it happens, and the transport is the one that cannot be reached
	// from off the machine.
	runtime := Runtime{
		InteractiveApproval: ApprovalAlways,
		Server: ServerRuntime{
			Transport:    TransportStdio,
			Listen:       DefaultListen,
			DrainTimeout: DefaultDrainTimeout,
			LogLevel:     DefaultLogLevel,
		},
	}

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
		applyServerFile(&runtime.Server, &file.Server)
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
	if env.LogLevel != "" {
		runtime.Server.LogLevel = env.LogLevel
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
	applyServerFlags(&runtime.Server, flags)
	return runtime
}

// applyServerFile folds the file layer into the resolved server settings. A
// field the file left empty keeps its default; the file cannot un-set one.
func applyServerFile(resolved *ServerRuntime, file *Server) {
	if file.Transport != "" {
		resolved.Transport = file.Transport
	}
	if file.Listen != "" {
		resolved.Listen = file.Listen
	}
	if file.LogLevel != "" {
		resolved.LogLevel = file.LogLevel
	}
	resolved.AllowedOrigins = append([]string(nil), file.AllowedOrigins...)
	resolved.AllowedHosts = append([]string(nil), file.AllowedHosts...)
	if file.DrainTimeout != "" {
		// Already validated at load; an unparseable value never reaches here.
		if d, err := time.ParseDuration(file.DrainTimeout); err == nil {
			resolved.DrainTimeout = d
		}
	}
	resolved.AllowUnauthenticatedPublicBind = file.AllowUnauthenticatedPublicBind
}

// applyServerFlags is the last word, per FR-62.
func applyServerFlags(resolved *ServerRuntime, flags Overrides) {
	if flags.Transport != nil {
		resolved.Transport = *flags.Transport
	}
	if flags.Listen != nil {
		resolved.Listen = *flags.Listen
	}
	if flags.LogLevel != nil {
		resolved.LogLevel = *flags.LogLevel
	}
	if flags.DrainTimeout != nil {
		resolved.DrainTimeout = *flags.DrainTimeout
	}
	if flags.AllowUnauthenticatedPublicBind != nil {
		resolved.AllowUnauthenticatedPublicBind = *flags.AllowUnauthenticatedPublicBind
	}
}

// Environment is the subset of LOTSMAN_* bindings that participate in
// precedence.
type Environment struct {
	BaseURL  string
	LogLevel string
}
