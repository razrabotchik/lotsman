package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// The CLI is tested through the real binary, in a subprocess, because that is
// the only way to test what an operator actually runs: exit codes, stdout that
// must stay clean for stdio MCP, and the signal handling of `serve`.
//
// Two consequences, both handled here rather than in every test.
//
// One: the binary is built once per test binary. It used to be built into each
// test's own temporary directory, so a suite with fifty CLI tests ran fifty
// builds -- cheap with a warm cache, and still the largest fixed cost in the
// package.
//
// Two: a subprocess is invisible to `go test -coverprofile`, so `make cover`
// reported cmd/lotsman at 0% and the total at three quarters of the statements
// while most of what it called was exercised. When LOTSMAN_CLI_COVERDIR is set,
// the binary is built with coverage instrumentation and every subprocess writes
// its counters there, which `make cover` converts and merges. Unset, nothing
// changes.
var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

func TestMain(m *testing.M) {
	// Counters are written by the instrumented child, and it reads GOCOVERDIR
	// from its environment. Setting it here means every subprocess inherits it,
	// whether the caller passes an environment of its own or not.
	if dir := coverDir(); dir != "" {
		if err := os.Setenv("GOCOVERDIR", dir); err != nil {
			fmt.Fprintf(os.Stderr, "set GOCOVERDIR: %v\n", err)
			os.Exit(1)
		}
	}

	code := m.Run()
	if binaryPath != "" {
		_ = os.RemoveAll(filepath.Dir(binaryPath))
	}
	os.Exit(code)
}

// coverDir is where an instrumented subprocess writes its counters, empty when
// coverage was not asked for.
func coverDir() string { return os.Getenv("LOTSMAN_CLI_COVERDIR") }

// buildBinary compiles the CLI once per test binary and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skipping the subprocess e2e test")
	}

	binaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lotsman-bin")
		if err != nil {
			binaryErr = err
			return
		}
		bin := filepath.Join(dir, "lotsman")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		args := []string{"build", "-o", bin}
		if coverDir() != "" {
			// The whole module, not just main: the point is to see the packages
			// the CLI drives that their own unit tests do not reach.
			//
			// The pattern is absolute because this build runs in the test's own
			// directory, where `./...` means this one package -- which is how
			// the first version of this instrumented everything except what it
			// was after. The mode matches the in-process profile so the two can
			// be merged.
			args = append(args, "-cover", "-covermode=atomic",
				"-coverpkg=github.com/razrabotchik/lotsman/...")
		}
		args = append(args, ".")
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			binaryErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		binaryPath = bin
	})
	if binaryErr != nil {
		t.Fatal(binaryErr)
	}
	return binaryPath
}
