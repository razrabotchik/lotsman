package config

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// The catalog block of docs/spec.md §5.1, now that it is read.
func TestCatalogSectionParses(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
catalog:
  mode: search
  maxSerializedBytes: 60000
  descriptionBytesPerTool: 400
  includeTags: [issues, projects]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if file.Catalog.Mode != "search" {
		t.Errorf("mode = %q", file.Catalog.Mode)
	}
	if file.Catalog.MaxSerializedBytes != 60000 || file.Catalog.DescriptionBytesPerTool != 400 {
		t.Errorf("budgets = %d / %d", file.Catalog.MaxSerializedBytes, file.Catalog.DescriptionBytesPerTool)
	}

	runtime := Resolve(file, Environment{}, Overrides{})
	if runtime.Mode != "search" || runtime.MaxSerializedBytes != 60000 || runtime.DescriptionBytesPerTool != 400 {
		t.Errorf("runtime = %+v", runtime)
	}
}

// A budget may be omitted but not negative. Zero is the shape of an omitted
// field; a negative number is a mistake, and reading it as "no limit" would
// turn a bound that exists to protect a model's context into nothing.
func TestANegativeBudgetIsRefused(t *testing.T) {
	for _, field := range []string{"maxSerializedBytes", "descriptionBytesPerTool"} {
		t.Run(field, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\ncatalog:\n  " + field + ": -1\n"))
			if err == nil {
				t.Fatal("a negative budget was accepted")
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}
}

func TestAnUnknownCatalogModeIsRefused(t *testing.T) {
	_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\ncatalog:\n  mode: hybrid\n"))
	if err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	if !strings.Contains(err.Error(), "tools, search or auto") {
		t.Errorf("the refusal does not say what is allowed: %v", err)
	}
}

// FR-62: the flag wins, and a flag nobody passed does not overwrite the file.
func TestTheModeFlagWinsOverTheFile(t *testing.T) {
	file := &File{Catalog: Catalog{Mode: "search"}}

	if got := Resolve(file, Environment{}, Overrides{}).Mode; got != "search" {
		t.Errorf("with no flag, mode = %q, want the file's", got)
	}
	tools := "tools"
	if got := Resolve(file, Environment{}, Overrides{Mode: &tools}).Mode; got != "tools" {
		t.Errorf("with a flag, mode = %q, want the flag's", got)
	}
}
