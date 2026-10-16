package cli

import (
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
