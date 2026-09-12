package argvalidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// schemaURL is the identity the compiled schema gets. Each Validator owns its
// compiler, so one fixed name cannot collide with another tool's schema.
const schemaURL = "https://lotsman.invalid/tool-input.schema.json"

// maxReportedProblems bounds how many validation problems are reported back.
// A model that sent the wrong shape needs the first few, not all of them.
const maxReportedProblems = 5

// Validator checks arguments against one tool's input schema.
type Validator struct {
	tool   string
	schema *jsonschema.Schema
}

// Compile prepares a validator for the tool's published input schema.
//
// The schema is marshalled and re-read as JSON on purpose: it makes the
// compiled schema byte-for-byte the one the client receives, so validation
// cannot drift from the published contract.
//
// A schema that does not compile is a spec problem, not a runtime one: the
// caller must refuse to publish the tool as executable rather than accept
// arguments it cannot check.
func Compile(toolName string, schema map[string]any) (*Validator, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "argvalidate: %s: marshal schema: %w", toolName, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "argvalidate: %s: read schema: %w", toolName, err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// Formats are annotations by default in 2020-12. Asserting them is the
	// fail-closed reading: if a spec says a path parameter is a uuid, a value
	// that is not one must not reach the API. Formats the library does not
	// know stay annotations, so this cannot reject an unknown OAS format.
	compiler.AssertFormat()

	if addErr := compiler.AddResource(schemaURL, doc); addErr != nil {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "argvalidate: %s: %w", toolName, addErr)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "argvalidate: %s: %w", toolName, err)
	}
	return &Validator{tool: toolName, schema: compiled}, nil
}

// Validate reports whether args satisfy the tool's input schema.
//
// Arguments are re-read through the JSON decoder the validator expects, so a
// number that arrived as a float64 is checked as the JSON number it was.
func (v *Validator) Validate(args map[string]any) error {
	if args == nil {
		// A call with no arguments at all is a call with an empty argument
		// object, not a null one: marshalling a nil map gives "null", which
		// every object schema rejects, including the schema of a tool that
		// takes no arguments.
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return errs.Errorf(errs.ClassUsage, "argvalidate: %s: arguments are not JSON: %w", v.tool, err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return errs.Errorf(errs.ClassUsage, "argvalidate: %s: arguments are not JSON: %w", v.tool, err)
	}

	if err := v.schema.Validate(instance); err != nil {
		var invalid *jsonschema.ValidationError
		if errors.As(err, &invalid) {
			return errs.Errorf(errs.ClassUsage, "invalid arguments for %s: %s", v.tool, problems(invalid))
		}
		return errs.Errorf(errs.ClassUsage, "invalid arguments for %s: %w", v.tool, err)
	}
	return nil
}

// problems flattens a validation error into a short, stable, one-line
// summary: the location in the arguments plus what was wrong with it.
func problems(err *jsonschema.ValidationError) string {
	basic := err.BasicOutput()
	seen := make(map[string]bool)
	var found []string

	var walk func(unit *jsonschema.OutputUnit)
	walk = func(unit *jsonschema.OutputUnit) {
		if unit == nil {
			return
		}
		if unit.Error != nil {
			location := unit.InstanceLocation
			if location == "" {
				location = "/"
			}
			problem := location + ": " + strings.Join(strings.Fields(unit.Error.String()), " ")
			if !seen[problem] {
				seen[problem] = true
				found = append(found, problem)
			}
		}
		for i := range unit.Errors {
			walk(&unit.Errors[i])
		}
	}
	walk(basic)

	// Sorted so the same invalid arguments always produce the same message
	// (Constitution IV applies to what the model reads back, too).
	sort.Strings(found)
	if len(found) > maxReportedProblems {
		extra := len(found) - maxReportedProblems
		found = append(found[:maxReportedProblems:maxReportedProblems], fmt.Sprintf("(+%d more)", extra))
	}
	return strings.Join(found, "; ")
}
