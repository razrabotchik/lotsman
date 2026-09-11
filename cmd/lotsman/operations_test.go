package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func miniSpecPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "mini", "basic.yaml"))
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}
	return abs
}

// TestOperationsCommand exercises T008 end to end against the shared fixture
// spec: five operations, one deliberately rejected (missing path parameter).
func TestOperationsCommand(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "operations", miniSpecPath(t)).Output()
	if err != nil {
		t.Fatalf("lotsman operations: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 6 { // header + 5 operations
		t.Fatalf("got %d lines, want 6 (header + 5 ops):\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "METHOD") {
		t.Errorf("header = %q, want it to start with METHOD", lines[0])
	}
	if !strings.Contains(string(out), "getOrder") || !strings.Contains(string(out), "rejected") {
		t.Errorf("output missing the rejected getOrder row:\n%s", out)
	}
	if !strings.Contains(string(out), "listPets") {
		t.Errorf("output missing a supported row (listPets):\n%s", out)
	}
}

// TestOperationsRejectedFlag checks the --rejected filter in both argument
// orders: the documented one (quickstart.md: SPEC before the flag) and the
// conventional one (flag before SPEC).
func TestOperationsRejectedFlag(t *testing.T) {
	bin := buildBinary(t)
	spec := miniSpecPath(t)

	for _, args := range [][]string{
		{"operations", spec, "--rejected"},
		{"operations", "--rejected", spec},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := exec.Command(bin, args...).Output()
			if err != nil {
				t.Fatalf("lotsman %v: %v\n%s", args, err, out)
			}
			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			if len(lines) != 2 { // header + the one rejected operation
				t.Fatalf("got %d lines, want 2 (header + 1 rejected op):\n%s", len(lines), out)
			}
			if !strings.Contains(lines[1], "getOrder") {
				t.Errorf("row = %q, want the getOrder operation", lines[1])
			}
		})
	}
}

// TestOperationsMissingSpec guards the exit-code contract (FR-77).
func TestOperationsMissingSpec(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "operations")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errorAs(err, &exitErr) {
		t.Fatalf("missing SPEC: err = %v, want a non-zero exit", err)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", got)
	}
	if !strings.Contains(stderr.String(), "requires SPEC") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

// TestOperationsMissingFile guards the runtime-error exit code, distinct
// from the usage error above.
func TestOperationsMissingFile(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "operations", "/no/such/spec.yaml")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errorAs(err, &exitErr) {
		t.Fatalf("missing file: err = %v, want a non-zero exit", err)
	}
	if got := exitErr.ExitCode(); got != 1 {
		t.Errorf("exit code = %d, want 1 (runtime error)", got)
	}
}
