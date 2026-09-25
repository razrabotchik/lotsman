package config

// Export renders the resolved configuration back into the shape of a
// configuration file (§5.2's `config export`).
//
// It is the effective settings, after precedence: defaults, then the file, then
// the environment, then the flags (FR-62). That is the point of it — the
// question an operator cannot otherwise answer is "which of the four layers
// won", and the answer is a document they can read and paste back.
//
// Secrets do not appear, and not because they are stripped: the configuration
// holds *references* and nothing else, so there is no value here to remove.
// Export never resolves one. A test carries a canary through this to say so,
// because "there was nothing to leak" is a property worth checking rather than
// assuming.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func (r Runtime) Export() File {
	file := File{
		APIVersion: APIVersion,
		Kind:       "Runtime",
		Spec: Spec{
			Source:     r.Source,
			Root:       r.Root,
			RemoteRefs: boolField(false),
			Strict:     boolField(!r.Lax),
		},
		Catalog: Catalog{
			Mode:                    r.Mode,
			MaxSerializedBytes:      r.MaxSerializedBytes,
			DescriptionBytesPerTool: r.DescriptionBytesPerTool,
			IncludeTags:             append([]string(nil), r.IncludeTags...),
		},
		Execution: Execution{
			DefaultPolicy:        postureOf(r.AllowMutations),
			AllowMutations:       r.AllowMutations,
			InteractiveApproval:  r.InteractiveApproval,
			Redirects:            RedirectsDeny,
			BaseURL:              r.BaseURL,
			AllowedOrigins:       append([]string(nil), r.AllowedOrigins...),
			AllowPrivateNetworks: r.AllowPrivateNetworks,
			AllowRules:           append([]Rule(nil), r.AllowRules...),
			DenyRules:            append([]Rule(nil), r.DenyRules...),
			MaxResponseBytes:     r.MaxResponseBytes,
		},
		Server:             r.Server.export(),
		OperationOverrides: append([]OperationOverride(nil), r.Overrides...),
	}
	if r.Timeout > 0 {
		file.Execution.Timeout = r.Timeout.String()
	}
	if len(r.AuthProfiles) > 0 {
		file.AuthProfiles = make(map[string]Profile, len(r.AuthProfiles))
		for name, profile := range r.AuthProfiles {
			file.AuthProfiles[name] = profile
		}
	}
	return file
}

// export renders the resolved server settings.
//
//nolint:gocritic // hugeParam: ServerRuntime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func (s ServerRuntime) export() Server {
	out := Server{
		Transport:                      s.Transport,
		Listen:                         s.Listen,
		LogLevel:                       s.LogLevel,
		AllowedOrigins:                 append([]string(nil), s.AllowedOrigins...),
		AllowedHosts:                   append([]string(nil), s.AllowedHosts...),
		AllowUnauthenticatedPublicBind: s.AllowUnauthenticatedPublicBind,
		InboundAuth:                    s.InboundAuth,
	}
	if s.DrainTimeout > 0 {
		out.DrainTimeout = s.DrainTimeout.String()
	}
	return out
}

// postureOf is the `defaultPolicy` spelling of the mutation switch. It is only
// emitted when it agrees with `allowMutations`, because emitting both when they
// disagree would produce a document this parser refuses -- and an export that
// cannot be read back is not an export.
func postureOf(allowMutations bool) DefaultPolicy {
	if allowMutations {
		return ""
	}
	return DefaultPolicyReadOnly
}

func boolField(b bool) *bool { return &b }
