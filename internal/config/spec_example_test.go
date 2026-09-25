package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// The example configuration in the frozen specification (docs/spec.md §5.1)
// parses, and produces the runtime it describes.
//
// It did not, for a long time. Strict decoding refused eight fields, and
// lotsman implemented most of what it refused at exactly the documented
// values -- the catalog budget of 120 000 bytes, the per-tool description
// ceiling of 1 200, the response cap of 524 288, the 30s timeout -- with each
// constant carrying a comment saying it came from this example. The values
// were taken from the specification; the ability to set them was not.
//
// The example is read out of the document rather than copied into this file,
// so the test cannot drift from the specification it is about. A field added
// to §5.1 fails here until the field is real.
func TestTheSpecificationsOwnExampleParses(t *testing.T) {
	example := specExample(t)

	file, err := Parse(example)
	if err != nil {
		t.Fatalf("the specification's own example does not parse: %v", err)
	}

	// And it means what it says. Each of these is a value from §5.1 that used
	// to be a constant nobody could reach.
	runtime := Resolve(file, Environment{}, Overrides{})
	for _, check := range []struct {
		field string
		got   any
		want  any
	}{
		{"catalog.mode", runtime.Mode, "auto"},
		{"catalog.maxSerializedBytes", runtime.MaxSerializedBytes, 120000},
		{"catalog.descriptionBytesPerTool", runtime.DescriptionBytesPerTool, 1200},
		{"execution.timeout", runtime.Timeout, 30 * time.Second},
		{"execution.maxResponseBytes", runtime.MaxResponseBytes, 524288},
		{"execution.allowMutations", runtime.AllowMutations, false},
		{"execution.interactiveApproval", runtime.InteractiveApproval, ApprovalAlways},
		{"server.transport", runtime.Server.Transport, TransportStdio},
		{"server.logLevel", runtime.Server.LogLevel, "info"},
		{"spec.strict", runtime.Lax, false},
	} {
		if check.got != check.want {
			t.Errorf("%s resolved to %v, want %v", check.field, check.got, check.want)
		}
	}

	// The parts that are lists rather than scalars.
	if len(runtime.IncludeTags) != 2 {
		t.Errorf("catalog.includeTags = %v, want two tags", runtime.IncludeTags)
	}
	if len(runtime.AllowedOrigins) != 1 {
		t.Errorf("execution.allowedOrigins = %v, want one origin", runtime.AllowedOrigins)
	}
	if len(runtime.Overrides) != 2 {
		t.Errorf("operationOverrides = %d, want two", len(runtime.Overrides))
	}
	if _, ok := runtime.AuthProfiles["example"]; !ok {
		t.Errorf("authProfiles = %v, want the example profile", runtime.AuthProfiles)
	}
}

// specExample reads the YAML block out of docs/spec.md §5.1, so that the test
// tracks the specification rather than a copy of it that can drift too.
func specExample(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "spec.md")
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	section := regexp.MustCompile(`(?s)### 5\.1.*?\x60\x60\x60yaml\n(.*?)\x60\x60\x60`).
		FindSubmatch(document)
	if section == nil {
		t.Fatal("docs/spec.md §5.1 no longer contains a YAML example")
	}
	return section[1]
}
