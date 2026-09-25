package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// The same rule for flags as for settings: a message that tells someone to pass
// `--allow-mutations` has to name a flag that exists, or the advice sends them
// to a usage error and teaches them to distrust the next hint.
//
// Flags are named from every package -- a refusal in internal/policy tells an
// operator about `--allow-mutations` -- so the search is the whole module while
// the definitions live here.
func TestEveryFlagNamedInAMessageExists(t *testing.T) {
	defined := definedFlags(t)
	// `--help` is the shell's way of asking, handled before any flag set, and
	// the transport-less commands take it too.
	defined["help"] = true

	mentioned := flagsMentionedInSource(t)
	if len(mentioned) < 10 {
		t.Fatalf("only %d flags were found in messages, so this test proves nothing", len(mentioned))
	}
	for _, ref := range mentioned {
		if !defined[ref.flag] {
			t.Errorf("%s names --%s, and no command defines it", ref.where, ref.flag)
		}
	}
}

type flagRef struct {
	flag  string
	where string
}

var flagPattern = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

func flagsMentionedInSource(t *testing.T) []flagRef {
	t.Helper()
	root := filepath.Join("..", "..")
	var found []flagRef
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "bin", "testdata", "coverage-cli":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, literal := range quotedGoStrings(line) {
				// A flag being *defined* or parsed is spelled without dashes;
				// only prose mentions them, which is what this is about.
				for _, match := range flagPattern.FindAllStringSubmatch(literal, -1) {
					found = append(found, flagRef{
						flag:  match[1],
						where: fmt.Sprintf("%s:%d", filepath.ToSlash(path), i+1),
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return found
}

// quotedGoStrings returns the double-quoted literals on a line.
func quotedGoStrings(line string) []string {
	var out []string
	for {
		start := strings.Index(line, `"`)
		if start < 0 {
			return out
		}
		rest := line[start+1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		line = rest[end+1:]
	}
}

// `--help` quotes a measurement, and a measurement in prose is a claim that
// rots: this one said 5.5 KB after the published list had become 5 297 bytes,
// and it had been corrected in three documents and not here. So the number is
// checked against a live session rather than against whoever last remembered.
//
// The tolerance is for rounding to one decimal, not for drift: a change that
// moves the real payload by more than a few percent fails here and has to be
// stated, which is the whole point of quoting a number at all.
func TestTheHelpTextQuotesTheRealSearchModeSize(t *testing.T) {
	help, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("help exited %d", code)
	}
	claim := regexp.MustCompile(`(\d+\.\d+) KB whether the API has`).FindStringSubmatch(help)
	if claim == nil {
		t.Fatal("the help text no longer quotes a search-mode size; update or remove this test")
	}
	claimed, err := strconv.ParseFloat(claim[1], 64)
	if err != nil {
		t.Fatalf("parse %q: %v", claim[1], err)
	}

	// Measured on the wire, over a sessionless POST, because that is what the
	// number means: what a client receives. Marshalling the SDK's own result
	// struct instead adds fields the wire never carried -- `resultType` is
	// internal to the client -- and overstates the payload by about 2%, which
	// is how a measurement can be both careful and wrong.
	endpoint, _ := serveHTTP(t, miniSpecPath(t), "--lax", "--mode=search",
		"--base-url", "https://api.example.com")
	frame := sessionless(t, endpoint, "tools/list", "1")
	result, ok := frame["result"]
	if !ok {
		t.Fatalf("tools/list returned no result: %+v", frame)
	}
	onTheWire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	measured := float64(len(onTheWire)) / 1000
	if delta := measured - claimed; delta > 0.1 || delta < -0.1 {
		t.Errorf("the help text claims %.1f KB and a client receives %.1f KB (%d bytes); "+
			"if the change was intended, say the new number in --help, README.md and docs/corpus.md",
			claimed, measured, len(onTheWire))
	}
}
