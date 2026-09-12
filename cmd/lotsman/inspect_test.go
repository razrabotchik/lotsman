package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI runs the built binary and returns stdout, stderr and the exit code.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(buildBinary(t), args...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errorAs(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

// inspectReport is the consumer's view of the contract: a CI tool decodes the
// fields it knows and ignores the rest.
type inspectReport struct {
	SchemaVersion string `json:"schemaVersion"`
	Lotsman       struct {
		Version     string   `json:"version"`
		MCPProtocol []string `json:"mcpProtocol"`
	} `json:"lotsman"`
	Spec struct {
		Source  string `json:"source"`
		Digest  string `json:"digest"`
		OpenAPI string `json:"openapi"`
	} `json:"spec"`
	Totals struct {
		Found             int `json:"found"`
		Supported         int `json:"supported"`
		Partial           int `json:"partial"`
		Rejected          int `json:"rejected"`
		Published         int `json:"published"`
		Executable        int `json:"executable"`
		CapabilityBlocked int `json:"capabilityBlocked"`
		PolicyBlocked     int `json:"policyBlocked"`
	} `json:"totals"`
	ByReason map[string]int `json:"byReason"`
	Catalog  struct {
		Mode            string `json:"mode"`
		ToolCount       int    `json:"toolCount"`
		SerializedBytes int    `json:"serializedBytesEstimate"`
		Digest          string `json:"digest"`
	} `json:"catalog"`
	Security struct {
		RemoteRefs              bool `json:"remoteRefs"`
		Redirects               bool `json:"redirects"`
		UnknownMutationsBlocked bool `json:"unknownMutationsBlocked"`
	} `json:"security"`
	DocumentIssues []struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"documentIssues"`
	Operations []struct {
		Key        string `json:"key"`
		ToolName   string `json:"toolName"`
		Support    string `json:"support"`
		Published  bool   `json:"published"`
		Executable bool   `json:"executable"`
		Effect     struct {
			Effect     string   `json:"effect"`
			Source     string   `json:"source"`
			Confidence string   `json:"confidence"`
			Warnings   []string `json:"warnings"`
		} `json:"effect"`
		Reasons []struct {
			Code    string `json:"code"`
			Detail  string `json:"detail"`
			Pointer string `json:"pointer"`
		} `json:"reasons"`
	} `json:"operations"`
}

func decodeReport(t *testing.T, stdout string) inspectReport {
	t.Helper()
	var report inspectReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	return report
}

func TestInspectJSONContract(t *testing.T) {
	stdout, stderr, code := runCLI(t, "inspect", miniSpecPath(t), "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a rejected operation is a finding, not a failure)\n%s", code, stderr)
	}
	report := decodeReport(t, stdout)

	if report.SchemaVersion != "1" {
		t.Errorf("schemaVersion = %q, want 1", report.SchemaVersion)
	}
	if report.Lotsman.Version == "" || len(report.Lotsman.MCPProtocol) == 0 {
		t.Errorf("lotsman identity = %+v", report.Lotsman)
	}
	if !strings.HasPrefix(report.Spec.Digest, "sha256:") || report.Spec.OpenAPI != "3.0.3" {
		t.Errorf("spec identity = %+v", report.Spec)
	}
	if report.Totals.Found != 5 || report.Totals.Supported != 4 || report.Totals.Rejected != 1 {
		t.Errorf("totals = %+v", report.Totals)
	}
	if report.Totals.Published != 4 || report.Totals.Executable != 2 || report.Totals.PolicyBlocked != 2 {
		t.Errorf("publication totals = %+v", report.Totals)
	}
	if report.Catalog.Mode != "tools" || report.Catalog.ToolCount != 4 ||
		report.Catalog.SerializedBytes == 0 || !strings.HasPrefix(report.Catalog.Digest, "sha256:") {
		t.Errorf("catalog estimate = %+v", report.Catalog)
	}
	if report.Security.RemoteRefs || report.Security.Redirects || !report.Security.UnknownMutationsBlocked {
		t.Errorf("security summary = %+v, want the fail-closed posture", report.Security)
	}
	if len(report.Operations) != 5 {
		t.Fatalf("operations = %d, want every enumerated operation, published or not", len(report.Operations))
	}

	// Sorted by key, as the contract guarantees.
	for i := 1; i < len(report.Operations); i++ {
		if report.Operations[i-1].Key >= report.Operations[i].Key {
			t.Errorf("operations not sorted by key: %q before %q",
				report.Operations[i-1].Key, report.Operations[i].Key)
		}
	}

	byKey := map[string]int{}
	for i, op := range report.Operations {
		byKey[op.Key] = i
	}
	rejected := report.Operations[byKey["default:GET:/orders/{orderId}"]]
	if rejected.Support != "rejected" || rejected.Published || rejected.Executable {
		t.Errorf("rejected operation = %+v", rejected)
	}
	if len(rejected.Reasons) == 0 || rejected.Reasons[0].Code != "path_parameter_mismatch" ||
		rejected.Reasons[0].Pointer == "" {
		t.Errorf("rejected reasons = %+v, want a code and a pointer", rejected.Reasons)
	}
	post := report.Operations[byKey["default:POST:/pets"]]
	if !post.Published || post.Executable {
		t.Errorf("POST = %+v, want published but not executable under the default policy", post)
	}
	if post.Effect.Effect != "unknown" || post.Effect.Source != "http_method" || post.Effect.Confidence != "inferred" {
		t.Errorf("POST effect = %+v", post.Effect)
	}
}

// The report describes the policy it was asked about, so a reader can preview
// what enabling mutations would change before enabling it.
func TestInspectReportsUnderTheRequestedPolicy(t *testing.T) {
	stdout, _, code := runCLI(t, "inspect", miniSpecPath(t), "--json", "--allow-mutations")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	report := decodeReport(t, stdout)

	if report.Totals.PolicyBlocked != 0 {
		t.Errorf("policyBlocked = %d, want 0 with mutations enabled", report.Totals.PolicyBlocked)
	}
	if report.Totals.Executable != 4 {
		t.Errorf("executable = %d, want every supported operation", report.Totals.Executable)
	}
	if report.Security.UnknownMutationsBlocked {
		t.Error("security summary still claims unknown mutations are blocked")
	}
}

// A document lotsman refuses to serve is exactly the document inspect exists
// to explain, so document-level errors are reported, not fatal.
func TestInspectExplainsADocumentServeWouldRefuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external-ref.yaml")
	spec := []byte(`openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - $ref: './common.yaml#/Limit'
      responses: { "200": { description: ok } }
`)
	if err := os.WriteFile(path, spec, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "inspect", path, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	report := decodeReport(t, stdout)
	if len(report.DocumentIssues) == 0 {
		t.Fatalf("no document issues reported for a document with an unresolvable reference:\n%s", stdout)
	}

	// And `serve` still refuses it, which is the contrast the report explains.
	_, serveErr, serveCode := runCLI(t, "serve", path, "--lax")
	if serveCode == 0 {
		t.Error("serve accepted a document with error diagnostics")
	}
	if !strings.Contains(serveErr, "error diagnostics") {
		t.Errorf("serve stderr = %q, want it to name the document errors", serveErr)
	}
}

func TestInspectFailOnRejectedIsTheCIHelper(t *testing.T) {
	_, stderr, code := runCLI(t, "inspect", miniSpecPath(t), "--fail-on-rejected")
	if code != 4 {
		t.Errorf("exit = %d, want 4 (valid spec, unsupported under the selected policy)", code)
	}
	if !strings.Contains(stderr, "rejected") {
		t.Errorf("stderr = %q, want it to say what failed", stderr)
	}
}

func TestInspectHumanOutput(t *testing.T) {
	stdout, _, code := runCLI(t, "inspect", miniSpecPath(t))
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{
		"operations: 5 found",
		"tools:",
		"blocked by policy",
		"security:",
		"unknown mutations blocked",
		"rejected operations:",
		"path_parameter_mismatch",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human report missing %q:\n%s", want, stdout)
		}
	}
}

func TestInspectRequiresSpec(t *testing.T) {
	if _, _, code := runCLI(t, "inspect"); code != 2 {
		t.Errorf("exit = %d, want 2 for a missing SPEC", code)
	}
}

// Two runs of the same spec produce the same report, byte for byte
// (Constitution IV, and the reason a report can be diffed in CI at all).
func TestInspectIsDeterministic(t *testing.T) {
	first, _, _ := runCLI(t, "inspect", miniSpecPath(t), "--json")
	second, _, _ := runCLI(t, "inspect", miniSpecPath(t), "--json")
	if first != second {
		t.Error("two inspect runs produced different reports")
	}
}

// T101/T102: the mode is reported with the measurement behind it, and an
// operator who asks for a mode that does not exist is told so.
func TestInspectReportsTheCatalogBudget(t *testing.T) {
	stdout, _, code := runCLI(t, "inspect", miniSpecPath(t), "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var report struct {
		Catalog struct {
			Mode            string `json:"mode"`
			Recommended     string `json:"recommendedMode"`
			SerializedBytes int    `json:"serializedBytesEstimate"`
			ThresholdBytes  int    `json:"thresholdBytes"`
			OverBudget      bool   `json:"overBudget"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Catalog.Mode != "tools" || report.Catalog.Recommended != "tools" {
		t.Errorf("modes = %+v", report.Catalog)
	}
	if report.Catalog.ThresholdBytes == 0 || report.Catalog.OverBudget {
		t.Errorf("budget = %+v, want a threshold and a fitting catalog", report.Catalog)
	}
}

// Search mode exists now; an unknown mode is still a usage error.
func TestInspectAcceptsEveryKnownMode(t *testing.T) {
	for _, mode := range []string{"tools", "search", "auto"} {
		if _, stderr, code := runCLI(t, "inspect", miniSpecPath(t), "--mode="+mode); code != 0 {
			t.Errorf("--mode=%s exited %d: %s", mode, code, stderr)
		}
	}
	if _, _, code := runCLI(t, "inspect", miniSpecPath(t), "--mode=nonsense"); code != 2 {
		t.Errorf("exit = %d for a bad mode, want 2", code)
	}
}

// A pinned mode is obeyed and reported, even when the measurement disagrees.
func TestInspectReportsAPinnedMode(t *testing.T) {
	stdout, _, code := runCLI(t, "inspect", miniSpecPath(t), "--mode=search", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var report struct {
		Catalog struct {
			Mode        string `json:"mode"`
			Recommended string `json:"recommendedMode"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Catalog.Mode != "search" {
		t.Errorf("mode = %q, want the request obeyed", report.Catalog.Mode)
	}
	if report.Catalog.Recommended != "tools" {
		t.Errorf("recommended = %q, want the measurement unchanged by the request", report.Catalog.Recommended)
	}
}
