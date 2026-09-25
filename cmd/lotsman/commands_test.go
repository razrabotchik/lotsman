package main_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every command docs/spec.md §4.11 lists exists, unless the specification says
// on the line itself that it does not yet.
//
// The list is read out of the document rather than copied here, on the same
// terms as the configuration example: a test that carries its own copy of the
// thing it checks can drift from it silently.
//
// One entry is deferred in the specification's own words --
// `auth login|status|logout` is annotated "появляется вместе с auth-code flow",
// which is M4b -- and a comment on the line is how the document says so. A
// command with no such note is a promise.
func TestEveryDocumentedCommandExists(t *testing.T) {
	documented := documentedCommands(t)
	if len(documented) == 0 {
		t.Fatal("docs/spec.md §4.11 no longer lists any commands")
	}

	for _, command := range documented {
		t.Run(command, func(t *testing.T) {
			// `help` is the fallback for anything unrecognised, so an unknown
			// command exits 2 with the usage text. A command that exists
			// either succeeds or complains about its own arguments.
			_, stderr, code := runCLI(t, command)
			if code == 2 && strings.Contains(stderr, "unknown command") {
				t.Errorf("docs/spec.md §4.11 lists %q, and the binary does not have it", command)
			}
		})
	}
}

// documentedCommands reads §4.11's block, skipping any line the specification
// annotates as not yet present.
func documentedCommands(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "spec.md")
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	section := regexp.MustCompile(`(?s)### 4\.11.*?\x60\x60\x60text\n(.*?)\x60\x60\x60`).
		FindSubmatch(document)
	if section == nil {
		t.Fatal("docs/spec.md §4.11 no longer contains a command block")
	}

	var commands []string
	for _, line := range strings.Split(string(section[1]), "\n") {
		if strings.Contains(line, "#") {
			// The specification's own way of saying "not yet".
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "lotsman" {
			continue
		}
		// `config check|export` is one entry for two commands; the first word
		// is the one the dispatcher sees.
		commands = append(commands, fields[1])
	}
	sort.Strings(commands)
	return commands
}
