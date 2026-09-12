package catalog

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/policy"
)

func rejectedOperation(method, path string, reason domain.ReasonCode, detail, pointer string) domain.Operation {
	op := operationWithEffect(method, path, domain.EffectRead, nil)
	op.Support = domain.SupportStatus{Level: domain.SupportRejected, Reasons: []domain.ReasonCode{reason}}
	op.Diagnostics = []domain.Diagnostic{{
		Severity: domain.SeverityError, Code: reason, Message: detail, Pointer: pointer,
	}}
	return op
}

func reportFixture() []domain.Operation {
	blocked := operationWithEffect("GET", "/cookies", domain.EffectRead, nil)
	blocked.ExecutionBlockers = []domain.ReasonCode{domain.ReasonParametersNotImplemented}

	return []domain.Operation{
		operationWithEffect("GET", "/pets", domain.EffectRead, nil),
		operationWithEffect("POST", "/pets", domain.EffectUnknown, nil),
		operationWithEffect("DELETE", "/pets/{petId}", domain.EffectDestructive, nil),
		blocked,
		rejectedOperation("POST", "/uploads", domain.ReasonUnsupportedMediaType,
			"multipart/form-data", "#/paths/~1uploads/post"),
	}
}

// The three ways an operation can be unavailable are counted separately: a
// reader can act on each of them differently.
func TestReportSeparatesTranslationCapabilityAndPolicy(t *testing.T) {
	report := Build("sha256:spec", reportFixture(), Options{}).Report

	totals := report.Totals
	if totals.Found != 5 || totals.Supported != 4 || totals.Rejected != 1 {
		t.Errorf("translation totals = %+v", totals)
	}
	if totals.Published != 4 {
		t.Errorf("published = %d, want 4 (every supported operation)", totals.Published)
	}
	if totals.Executable != 1 {
		t.Errorf("executable = %d, want 1 (only the read with no blockers)", totals.Executable)
	}
	if totals.CapabilityBlocked != 1 {
		t.Errorf("capabilityBlocked = %d, want 1 (the cookie parameter)", totals.CapabilityBlocked)
	}
	if totals.PolicyBlocked != 2 {
		t.Errorf("policyBlocked = %d, want 2 (the POST and the DELETE)", totals.PolicyBlocked)
	}
	if got := totals.ByEffect[domain.EffectDestructive]; got != 1 {
		t.Errorf("byEffect[destructive] = %d, want 1", got)
	}
}

func TestReportCountsReasons(t *testing.T) {
	report := Build("sha256:spec", reportFixture(), Options{}).Report

	for code, want := range map[domain.ReasonCode]int{
		domain.ReasonUnsupportedMediaType:       1,
		domain.ReasonParametersNotImplemented:   1,
		domain.ReasonPolicyMutationBlocked:      1,
		domain.ReasonPolicyUnknownEffectBlocked: 1,
	} {
		if got := report.ByReason[code]; got != want {
			t.Errorf("byReason[%s] = %d, want %d", code, got, want)
		}
	}
}

// A rejected operation is the one a reader most needs to see, so it appears
// in the report with its pointer and message even though it is not a tool.
func TestReportKeepsRejectedOperationsWithProvenance(t *testing.T) {
	report := Build("sha256:spec", reportFixture(), Options{}).Report

	var found bool
	for _, verdict := range report.Operations {
		if verdict.Support != domain.SupportRejected {
			continue
		}
		found = true
		if verdict.Published || verdict.Executable {
			t.Errorf("a rejected operation is published or executable: %+v", verdict)
		}
		if verdict.ToolName != "" {
			t.Errorf("a rejected operation has a tool name: %+v", verdict)
		}
		if len(verdict.Reasons) != 1 {
			t.Fatalf("reasons = %+v, want exactly one", verdict.Reasons)
		}
		reason := verdict.Reasons[0]
		if reason.Code != domain.ReasonUnsupportedMediaType ||
			reason.Detail != "multipart/form-data" || reason.Pointer == "" {
			t.Errorf("reason = %+v, want code, detail and pointer", reason)
		}
	}
	if !found {
		t.Fatal("the rejected operation is missing from the report")
	}
}

func TestReportOperationsSortedByKey(t *testing.T) {
	report := Build("sha256:spec", reportFixture(), Options{}).Report
	for i := 1; i < len(report.Operations); i++ {
		if report.Operations[i-1].Key >= report.Operations[i].Key {
			t.Fatalf("operations are not sorted by key: %q before %q",
				report.Operations[i-1].Key, report.Operations[i].Key)
		}
	}
}

// T021's determinism requirement, stated directly: two runs, byte-identical.
func TestReportIsByteIdenticalAcrossRuns(t *testing.T) {
	ops := reportFixture()
	first, err := json.Marshal(Build("sha256:spec", ops, Options{}).Report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(Build("sha256:spec", ops, Options{}).Report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("report differs across runs:\n%s\n%s", first, second)
	}
}

func TestReportReflectsThePolicyItWasBuiltUnder(t *testing.T) {
	ops := reportFixture()
	readOnly := Build("sha256:spec", ops, Options{}).Report
	allowed := Build("sha256:spec", ops, Options{Policy: policy.Config{AllowMutations: true}}).Report

	if !readOnly.Security.UnknownMutationsBlocked {
		t.Error("the default policy must report unknown mutations as blocked")
	}
	if allowed.Security.UnknownMutationsBlocked {
		t.Error("with mutations enabled, unknown effects are no longer blocked")
	}
	if allowed.Totals.PolicyBlocked != 0 || allowed.Totals.Executable <= readOnly.Totals.Executable {
		t.Errorf("enabling mutations did not change the verdicts: %+v", allowed.Totals)
	}
}

// The catalog estimate is what decides whether a spec needs search mode, so
// it must measure the payload a client actually receives.
func TestReportEstimatesTheToolListPayload(t *testing.T) {
	report := Build("sha256:spec", reportFixture(), Options{}).Report

	if report.Estimate.ToolCount != 4 || report.Estimate.Mode != "tools" {
		t.Errorf("estimate = %+v", report.Estimate)
	}
	if report.Estimate.SerializedBytes < 100 {
		t.Errorf("serializedBytes = %d, want the real tool-list size", report.Estimate.SerializedBytes)
	}
	if report.Estimate.Digest == "" {
		t.Error("the estimate must carry the catalog digest it describes")
	}
}
