package config

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/response"
)

// The execution budgets of docs/spec.md §5.1, at values that are not the
// defaults. A test at the default proves only that the default still works.
func TestExecutionBudgetsAreRead(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
execution:
  timeout: 5s
  maxResponseBytes: 8192
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	runtime := Resolve(file, Environment{}, Overrides{})
	if runtime.Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", runtime.Timeout)
	}
	if runtime.MaxResponseBytes != 8192 {
		t.Errorf("maxResponseBytes = %d, want 8192", runtime.MaxResponseBytes)
	}
}

// A cap may be lowered and not raised. It exists to bound memory this process
// has to find, and an operator raising it is asking for a promise the runtime
// does not make -- so the refusal says that rather than accepting a number
// nothing will honour.
func TestTheResponseCapMayOnlyBeLowered(t *testing.T) {
	if _, err := Parse(executionCap(1024)); err != nil {
		t.Fatalf("a lower cap was refused: %v", err)
	}
	if _, err := Parse(executionCap(response.MaxBodyBytes)); err != nil {
		t.Fatalf("the ceiling itself was refused: %v", err)
	}

	_, err := Parse(executionCap(response.MaxBodyBytes + 1))
	if err == nil {
		t.Fatal("a cap above the ceiling was accepted")
	}
	if errs.ClassOf(err) != errs.ClassUsage {
		t.Errorf("class = %s, want usage", errs.ClassOf(err))
	}
	if !strings.Contains(err.Error(), "lowered and not raised") {
		t.Errorf("the refusal does not explain the rule: %v", err)
	}
}

func TestANegativeResponseCapIsRefused(t *testing.T) {
	_, err := Parse(executionCap(-1))
	if err == nil {
		t.Fatal("a negative cap was accepted")
	}
	if !strings.Contains(err.Error(), "not negative") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// A timeout has to be a duration, and a call needs some time to happen in.
func TestTheTimeoutIsValidated(t *testing.T) {
	for _, value := range []string{"soon", "0s", "-5s"} {
		t.Run(value, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\nexecution:\n  timeout: \"" +
				value + "\"\n"))
			if err == nil {
				t.Fatal("the timeout was accepted")
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
		})
	}
}

func executionCap(bytes int) []byte {
	return []byte("apiVersion: lotsman.dev/v1alpha1\nexecution:\n  maxResponseBytes: " +
		strconv.Itoa(bytes) + "\n")
}
