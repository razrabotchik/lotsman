package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README counts the dependencies, and the count is a claim about the
// project rather than a decoration: "four direct dependencies" is the sentence
// that tells a reader what they are taking on. It was written when there were
// four, and features 005 and 006 added two more -- a JWT library and an OAuth2
// client -- so the number went quietly wrong.
//
// The count is read out of go.mod here rather than copied, for the same reason
// the command list is read out of the specification: a test carrying its own
// copy of the thing it checks can drift from it as silently as the prose did.
func TestTheReadmeCountsTheDependenciesCorrectly(t *testing.T) {
	count := directDependencies(t)
	if count == 0 {
		t.Fatal("go.mod lists no direct dependencies, which cannot be right")
	}

	readme := readRepoFile(t, "README.md")
	want := fmt.Sprintf("%s direct dependencies", spelled(count))
	if !strings.Contains(strings.ToLower(readme), want) {
		t.Errorf("go.mod has %d direct dependencies and the README does not say %q; "+
			"it says: %q", count, want, dependencySentence(readme))
	}
}

// Every make target the README tells a reader to run exists. A command in a
// README is an instruction, and one that fails is worse than one that is
// missing: the reader assumes they broke it.
func TestEveryDocumentedMakeTargetExists(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	readme := readRepoFile(t, "README.md")

	for _, line := range strings.Split(readme, "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "$ "))
		if len(fields) < 2 || fields[0] != "make" || strings.HasPrefix(fields[1], "-") {
			continue
		}
		target := fields[1]
		if !strings.Contains(makefile, "\n"+target+":") {
			t.Errorf("the README says `make %s`, and the Makefile has no such target", target)
		}
	}
}

// directDependencies counts the requires that are not marked indirect.
func directDependencies(t *testing.T) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(readRepoFile(t, "go.mod"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.Contains(line, "// indirect") {
			continue
		}
		if !strings.HasPrefix(line, "require") && strings.Count(line, " ") == 1 &&
			strings.Contains(line, "/") && strings.Contains(line, " v") {
			count++
		}
	}
	return count
}

// dependencySentence finds whatever the README currently claims, so a failure
// shows the wrong number rather than only the right one.
func dependencySentence(readme string) string {
	for _, line := range strings.Split(readme, "\n") {
		if strings.Contains(line, "direct dependencies") {
			return strings.TrimSpace(line)
		}
	}
	return "nothing about direct dependencies"
}

// spelled is the README's own register: it writes numbers as words at this size.
func spelled(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven",
		"eight", "nine", "ten", "eleven", "twelve"}
	if n < len(words) {
		return words[n]
	}
	return fmt.Sprint(n)
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// Every flag the README shows in its command block is a flag the binary
// defines. A README is where an operator copies a command line from, and a flag
// that does not exist fails with the usage text -- which reads as the reader's
// mistake, not the document's.
func TestEveryDocumentedFlagExists(t *testing.T) {
	defined := definedFlags(t)
	if len(defined) == 0 {
		t.Fatal("no flags found in the source, so this test would pass vacuously")
	}

	block := commandBlock(t, readRepoFile(t, "README.md"))
	if block == "" {
		t.Fatal("the README no longer has a command block")
	}
	seen := 0
	for _, token := range strings.Fields(block) {
		token = strings.Trim(token, "[]|()")
		if !strings.HasPrefix(token, "--") {
			continue
		}
		name := strings.TrimPrefix(token, "--")
		if i := strings.IndexAny(name, "= "); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			continue
		}
		seen++
		if !defined[name] {
			t.Errorf("the README documents --%s, and no command defines it", name)
		}
	}
	if seen == 0 {
		t.Error("no flags were read out of the README, so this test proved nothing")
	}
}

// commandBlock returns the fenced block under the README's Commands heading.
func commandBlock(t *testing.T, readme string) string {
	t.Helper()
	heading := strings.Index(readme, "## Commands")
	if heading < 0 {
		return ""
	}
	rest := readme[heading:]
	start := strings.Index(rest, "```text")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("```text"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// definedFlags collects the flag names the commands register.
func definedFlags(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`fs\.(?:Bool|String|Int|Int64|Duration|Var|Func)(?:Var)?\(\s*(?:&[A-Za-z0-9_.]+,\s*)?"([a-z0-9-]+)"`)
	out := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			out[match[1]] = true
		}
	}
	return out
}
