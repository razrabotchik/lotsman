package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exportCanary = "CANARY-config-export-6b2a-DO-NOT-LEAK"

// writeConfig puts a configuration file next to a document it names.
func writeConfigBeside(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	document, err := os.ReadFile(miniSpecPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), document, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// `config check` answers "would this be accepted", and strict decoding makes
// that a sharper question than it sounds: a typo in a field that gates
// something open is caught here instead of later.
func TestConfigCheck(t *testing.T) {
	good := writeConfigBeside(t, `apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
execution:
  allowMutations: false
`)
	stdout, stderr, code := runCLI(t, "config", "check", good)
	if code != 0 {
		t.Fatalf("a usable configuration was refused (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "usable") {
		t.Errorf("stdout = %q", stdout)
	}

	// A contradiction, which is the interesting kind of bad configuration:
	// both fields are spelled correctly and they disagree.
	bad := writeConfigBeside(t, `apiVersion: lotsman.dev/v1alpha1
execution:
  defaultPolicy: read-only
  allowMutations: true
`)
	_, stderr, code = runCLI(t, "config", "check", bad)
	if code == 0 {
		t.Error("a contradictory configuration was accepted")
	}
	if !strings.Contains(stderr, "contradict") {
		t.Errorf("the refusal does not name the problem: %s", stderr)
	}

	// And --quiet means the exit code is the whole answer.
	stdout, stderr, code = runCLI(t, "config", "check", bad, "--quiet")
	if code == 0 || stdout != "" || stderr != "" {
		t.Errorf("--quiet printed something: %q / %q (exit %d)", stdout, stderr, code)
	}
}

// `config export` answers the question an operator cannot work out for
// themselves: after defaults, the file, the environment and the flags, which
// values are in force.
//
// The property that makes it worth printing is that reading it back produces
// the same thing, so the test does exactly that.
func TestConfigExportCanBeReadBack(t *testing.T) {
	t.Setenv("LOTSMAN_EXPORT_TOKEN", exportCanary)
	path := writeConfigBeside(t, `apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
  strict: false
catalog:
  mode: search
  includeTags: [pets]
execution:
  allowMutations: true
  timeout: 7s
  maxResponseBytes: 2048
authProfiles:
  example:
    scheme: bearer
    tokenRef: env:LOTSMAN_EXPORT_TOKEN
`)

	exported, stderr, code := runCLI(t, "config", "export", path)
	if code != 0 {
		t.Fatalf("export failed (%d): %s", code, stderr)
	}

	// The reference is there; the value it points at is not. Not because it was
	// stripped -- the configuration only ever holds the reference -- which is
	// worth asserting rather than assuming.
	if !strings.Contains(exported, "env:LOTSMAN_EXPORT_TOKEN") {
		t.Errorf("the export lost the secret reference:\n%s", exported)
	}
	if strings.Contains(exported, exportCanary) {
		t.Errorf("the export resolved a secret:\n%s", exported)
	}

	// Read it back: it has to be a configuration lotsman accepts, and one that
	// says the same things.
	roundTrip := filepath.Join(t.TempDir(), "exported.yaml")
	if err := os.WriteFile(roundTrip, []byte(exported), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = runCLI(t, "config", "check", roundTrip); code != 0 {
		t.Fatalf("the exported configuration does not parse (%d): %s\n%s", code, stderr, exported)
	}

	again, stderr, code := runCLI(t, "config", "export", roundTrip)
	if code != 0 {
		t.Fatalf("re-export failed (%d): %s", code, stderr)
	}
	if strip(again) != strip(exported) {
		t.Errorf("exporting twice produced different documents:\n%s\n---\n%s", exported, again)
	}
}

// Both subcommands want exactly one file, and anything else is a usage error
// rather than a guess.
func TestConfigNeedsASubcommandAndAFile(t *testing.T) {
	for _, args := range [][]string{
		{"config"},
		{"config", "explain"},
		{"config", "check"},
		{"config", "export"},
	} {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%v: exit code = %d, want 2 (usage)", args, code)
		}
	}
}

// strip drops the header comments, which name the file they came from.
func strip(document string) string {
	var kept []string
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
