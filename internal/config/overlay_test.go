package config

import (
	"strings"
	"testing"
)

func parseOrFatal(t *testing.T, yaml string) *File {
	t.Helper()
	file, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return file
}

func TestOverridesAndRulesRoundTrip(t *testing.T) {
	file := parseOrFatal(t, `
apiVersion: lotsman.dev/v1alpha1
catalog:
  includeTags: [droplets, images]
execution:
  allowMutations: true
  interactiveApproval: client-capability
  denyRules:
    - { tag: billing }
  allowRules:
    - { effect: write }
authProfiles:
  example:
    scheme: bearer
    tokenRef: env:EXAMPLE_TOKEN
operationOverrides:
  - match: { method: GET, path: /legacy/rebuild-cache }
    enabled: false
  - match: { operationId: createDraft }
    effect: write
    authProfile: example
`)

	if got := file.Catalog.IncludeTags; len(got) != 2 || got[0] != "droplets" {
		t.Errorf("includeTags = %v", got)
	}
	if file.Execution.InteractiveApproval != ApprovalClientCapability {
		t.Errorf("interactiveApproval = %q", file.Execution.InteractiveApproval)
	}
	if len(file.Execution.DenyRules) != 1 || file.Execution.DenyRules[0].Tag != "billing" {
		t.Errorf("denyRules = %+v", file.Execution.DenyRules)
	}
	if len(file.OperationOverrides) != 2 {
		t.Fatalf("operationOverrides = %+v", file.OperationOverrides)
	}
	if file.OperationOverrides[0].Enabled == nil || *file.OperationOverrides[0].Enabled {
		t.Errorf("enabled = %v, want an explicit false", file.OperationOverrides[0].Enabled)
	}
	if file.OperationOverrides[1].AuthProfile != "example" {
		t.Errorf("authProfile = %q", file.OperationOverrides[1].AuthProfile)
	}
}

func TestOverrideValidation(t *testing.T) {
	const header = "apiVersion: lotsman.dev/v1alpha1\n"
	for _, tt := range []struct {
		name    string
		yaml    string
		mention string
	}{
		{
			name:    "a match by both coordinates is ambiguous",
			yaml:    "operationOverrides:\n  - match: { operationId: x, method: GET, path: /x }\n    effect: read\n",
			mention: "not both",
		},
		{
			name:    "half a coordinate would match by accident",
			yaml:    "operationOverrides:\n  - match: { method: GET }\n    effect: read\n",
			mention: "needs a path",
		},
		{
			name:    "an override that states nothing is a typo",
			yaml:    "operationOverrides:\n  - match: { operationId: x }\n",
			mention: "states nothing",
		},
		{
			name:    "an effect outside the vocabulary would never be applied",
			yaml:    "operationOverrides:\n  - match: { operationId: x }\n    effect: readonly\n",
			mention: "not read, write, destructive or unknown",
		},
		{
			name:    "a pinned profile that does not exist",
			yaml:    "operationOverrides:\n  - match: { operationId: x }\n    authProfile: missing\n",
			mention: "not one of the configured authProfiles",
		},
		{
			name:    "an unknown approval mode",
			yaml:    "execution:\n  interactiveApproval: sometimes\n",
			mention: "always, client-capability or never",
		},
		{
			name:    "a rule that matches everything",
			yaml:    "execution:\n  denyRules:\n    - {}\n",
			mention: "matches every operation",
		},
		{
			name:    "a rule effect outside the vocabulary",
			yaml:    "execution:\n  allowRules:\n    - { effect: mutating }\n",
			mention: "not read, write, destructive or unknown",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(header + tt.yaml))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("error = %v, want it to mention %q", err, tt.mention)
			}
		})
	}
}

func TestApprovalDefaultsToAlways(t *testing.T) {
	// FR-44's default. It costs nothing while mutations are disabled and is
	// the safe answer the moment they are not, so it must not depend on the
	// operator having written it down.
	if got := Resolve(nil, Environment{}, Overrides{}).InteractiveApproval; got != ApprovalAlways {
		t.Errorf("default approval = %q, want always", got)
	}
	file := parseOrFatal(t, "apiVersion: lotsman.dev/v1alpha1\nexecution:\n  allowMutations: true\n")
	if got := Resolve(file, Environment{}, Overrides{}).InteractiveApproval; got != ApprovalAlways {
		t.Errorf("approval with a config that omits it = %q, want always", got)
	}
	never := ApprovalNever
	if got := Resolve(file, Environment{}, Overrides{Approval: &never}).InteractiveApproval; got != ApprovalNever {
		t.Errorf("approval from a flag = %q, want never", got)
	}
}
