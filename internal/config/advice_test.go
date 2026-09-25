package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A refusal that names a setting is advice, and advice that names a setting
// which does not exist is worse than none: the operator edits their file, gets
// the same refusal, and learns to distrust the next hint too.
//
// Renaming a field is a one-line change; the sentence telling someone to set it
// lives in another package and will not be recompiled into agreement. So the
// sentences are checked against the schema rather than against a reviewer's
// memory.
func TestEverySettingNamedInAMessageExists(t *testing.T) {
	known := map[string]bool{}
	collectYAMLPaths(reflect.TypeOf(File{}), "", known, 0)
	if len(known) < 40 {
		t.Fatalf("the schema walk found only %d paths, so this test proves nothing", len(known))
	}

	mentioned := settingsMentionedInSource(t)
	if len(mentioned) < 15 {
		t.Fatalf("only %d settings were found in messages, so this test proves nothing", len(mentioned))
	}

	for _, ref := range mentioned {
		if !known[ref.path] {
			t.Errorf("%s names %q, and the configuration has no such setting",
				ref.where, ref.path)
		}
	}
}

type settingRef struct {
	path  string
	where string
}

// sections are the top-level configuration keys. A dotted word starting with
// one of these, inside a string literal, is a setting being named.
var sections = []string{"spec", "catalog", "execution", "server", "authProfiles", "operationOverrides"}

// settingPattern finds a dotted path inside a double-quoted Go string.
var settingPattern = regexp.MustCompile(`(?:` + strings.Join(sections, "|") + `)\.[a-zA-Z][a-zA-Z0-9.]*`)

// settingsMentionedInSource scans the non-test Go sources for settings named in
// string literals.
func settingsMentionedInSource(t *testing.T) []settingRef {
	t.Helper()
	root := filepath.Join("..", "..")
	var found []settingRef
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
		source := path
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, literal := range quotedStrings(line) {
				for _, candidate := range settingPattern.FindAllString(literal, -1) {
					if setting := configPath(candidate); setting != "" {
						found = append(found, settingRef{
							path:  setting,
							where: fmt.Sprintf("%s:%d", filepath.ToSlash(source), i+1),
						})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	return found
}

// configPath normalizes a candidate, or returns empty when it is not a setting:
// a Go identifier (`catalog.Options`), a file name (`spec.md`), or a hostname
// (`spec.example.com`) all look like dotted paths and are not settings.
func configPath(candidate string) string {
	candidate = strings.TrimRight(candidate, ".")
	segments := strings.Split(candidate, ".")
	for _, segment := range segments[1:] {
		if segment == "" || segment[0] >= 'A' && segment[0] <= 'Z' {
			return "" // exported Go identifier
		}
	}
	switch last := segments[len(segments)-1]; last {
	case "md", "json", "yaml", "yml", "go", "com", "dev", "example", "golden":
		return ""
	}
	return candidate
}

// quotedStrings returns the double-quoted literals on a line. It is deliberately
// simple: a missed literal weakens the test, and the count assertion above is
// what keeps that from going unnoticed.
func quotedStrings(line string) []string {
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

// collectYAMLPaths walks the configuration types, recording every dotted path a
// document may legitimately contain.
func collectYAMLPaths(t reflect.Type, prefix string, out map[string]bool, depth int) {
	if depth > 6 {
		return
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		field := t.Field(i)
		tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		out[path] = true
		collectYAMLPaths(field.Type, path, out, depth+1)
	}
}
