package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The process around the command line: arguments in, the exit code out. The
// test binary runs itself as lotsman, so this is main() as it ships, and
// nothing more -- what the commands do is tested in internal/cli and test/e2e.
func TestMain(m *testing.M) {
	if os.Getenv("LOTSMAN_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestMainPassesArgumentsAndReturnsTheExitCode(t *testing.T) {
	run := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "LOTSMAN_RUN_MAIN=1")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit):
			return string(out), exit.ExitCode()
		case err != nil:
			t.Fatalf("run: %v", err)
		}
		return string(out), 0
	}

	if out, code := run("version"); code != 0 || !strings.Contains(out, "lotsman") {
		t.Errorf("lotsman version: exit %d, output %q", code, out)
	}
	if _, code := run("no-such-command"); code != 2 {
		t.Errorf("an unknown command exited %d, want the usage code 2", code)
	}
}
