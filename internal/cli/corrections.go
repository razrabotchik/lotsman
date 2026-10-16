package cli

import (
	"reflect"
	"strings"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/openapi"
)

// correctionsFrom translates the configuration's corrections into the
// adapter's vocabulary. Entries that only decide (effect, enabled,
// authProfile) stay with the catalog.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, read once per load.
func correctionsFrom(runtime config.Runtime) openapi.Corrections {
	var out openapi.Corrections
	for i := range runtime.ComponentOverrides {
		c := &runtime.ComponentOverrides[i]
		out.Components = append(out.Components, openapi.ComponentCorrection{
			Index: i, Schema: c.Match.Schema, Patch: domain.Schema(c.Patch), Note: c.Note,
		})
	}
	for i := range runtime.Overrides {
		o := &runtime.Overrides[i]
		if o.MediaType == "" && len(o.Parameters) == 0 && o.RequestBody == nil {
			continue
		}
		c := openapi.OperationCorrection{
			Index: i, OperationID: o.Match.OperationID, Method: o.Match.Method, Path: o.Match.Path,
			MediaType: o.MediaType, Note: o.Note,
		}
		for j := range o.Parameters {
			p := &o.Parameters[j]
			c.Parameters = append(c.Parameters, openapi.ParameterCorrection{
				Name: p.Name, In: p.In, Schema: domain.Schema(p.Schema),
				Required: p.Required, Style: p.Style, Explode: p.Explode,
			})
		}
		if o.RequestBody != nil {
			c.BodyPatch = domain.Schema(o.RequestBody.Patch)
		}
		out.Operations = append(out.Operations, c)
	}
	return out
}

// correctionError turns the adapter's problems into the one error a command
// reports. All of them, because an operator fixing a configuration wants the
// list, not the first item of it.
func correctionError(problems []openapi.CorrectionProblem) error {
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		lines = append(lines, p.Error())
	}
	return errs.Errorf(errs.ClassUsage, "%s", strings.Join(lines, "\n"))
}

// checkCorrectedSchemas compiles the input schema of every corrected tool. An
// uncorrected schema that does not compile is the document's problem and the
// runner already publishes it as not executable; a corrected one is the
// operator's, and the operator is told now rather than at the first call.
func checkCorrectedSchemas(cat *catalog.Catalog) error {
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		if len(tool.Corrections) == 0 {
			continue
		}
		if _, err := argvalidate.Compile(tool.Name, tool.InputSchema); err != nil {
			return errs.Errorf(errs.ClassUsage,
				"config: the corrected input schema of %s does not compile: %v", tool.OperationKey, err)
		}
	}
	return nil
}

// reloadRuntime is the configuration a reload builds from: the file as it is
// now, provided it differs from the running one only where a catalog is
// shaped. Policy, credentials and the server are fixed for the life of the
// process -- FR-72 republishes a catalog, not a policy.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration; the running one is never modified.
func reloadRuntime(running config.Runtime, configPath string,
	resolve func() (config.Runtime, error)) (config.Runtime, catalog.Mode, error) {
	current := running
	if configPath != "" {
		fresh, err := resolve()
		if err != nil {
			return config.Runtime{}, "", err
		}
		if changed := changedOutsideCatalog(running, fresh); len(changed) > 0 {
			return config.Runtime{}, "", errs.Errorf(errs.ClassUsage,
				"reload: the configuration changed in %s; a reload applies spec.overlays, componentOverrides, "+
					"operationOverrides and catalog only, the rest needs a restart", strings.Join(changed, ", "))
		}
		current = fresh
	}
	mode, err := parseMode(current.Mode)
	if err != nil {
		return config.Runtime{}, "", err
	}
	return current, mode, nil
}

// changedOutsideCatalog names, as the configuration file spells them, the
// settings that differ outside the sections a reload may change.
//
//nolint:gocritic // hugeParam: compared by value on purpose.
func changedOutsideCatalog(before, after config.Runtime) []string {
	b, a := reflect.ValueOf(withoutCatalog(before)), reflect.ValueOf(withoutCatalog(after))
	var changed []string
	for i := range b.NumField() {
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			changed = append(changed, configSpelling(b.Type().Field(i).Name))
		}
	}
	return changed
}

// runtimeSpelling maps a Runtime field to the setting an operator wrote. The
// operator edited a YAML file, and "AllowMutations" is not a line in it.
var runtimeSpelling = map[string]string{
	"AllowMutations":       "execution.allowMutations",
	"BaseURL":              "execution.baseURL",
	"InteractiveApproval":  "execution.interactiveApproval",
	"AllowRules":           "execution.allowRules",
	"DenyRules":            "execution.denyRules",
	"AllowedOrigins":       "execution.allowedOrigins",
	"AllowPrivateNetworks": "execution.allowPrivateNetworks",
	"Timeout":              "execution.timeout",
	"MaxResponseBytes":     "execution.maxResponseBytes",
	"MaxConcurrentCalls":   "execution.maxConcurrentCalls",
	"Lax":                  "spec.strict",
	"Source":               "spec.source",
	"Root":                 "spec.root",
	"AuthProfiles":         "authProfiles",
	"Server":               "server",
}

// configSpelling is the configuration's name for a Runtime field, or the
// field's own name when it has none.
func configSpelling(field string) string {
	if name, ok := runtimeSpelling[field]; ok {
		return name
	}
	return field
}

//nolint:gocritic // hugeParam: returns a modified copy.
func withoutCatalog(r config.Runtime) config.Runtime {
	r.Overrides, r.ComponentOverrides, r.Overlays, r.IncludeTags = nil, nil, nil, nil
	r.Mode, r.MaxSerializedBytes, r.DescriptionBytesPerTool = "", 0, 0
	return r
}
