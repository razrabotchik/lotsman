package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The example configuration in the frozen specification (docs/spec.md §5.1)
// does not parse, and this test says exactly which of its fields are refused.
//
// That is a drift worth pinning rather than discovering: an operator's first
// move is to copy the example, and strict decoding then rejects it with a list
// of eight unknown fields. Several of those fields describe settings lotsman
// *does* implement, at exactly the documented values -- the catalog budget is
// 120 000 bytes, the per-tool description ceiling is 1 200, the response cap is
// 524 288, the timeout budget is 30s, and every one of those constants carries
// a comment saying it came from this example. The values were taken from the
// specification; the ability to set them was not.
//
// The list below is therefore a to-do in test form. Implementing any of these
// fields makes this test fail, which is the point: the gap should shrink
// visibly, and nobody should have to rediscover it. It has already shrunk once
// -- the three `catalog` fields came off it in step 1 of feature 007 -- and the
// remaining names carry the task that will take them off.
func TestTheSpecificationsOwnExampleDoesNotParseYet(t *testing.T) {
	example := specExample(t)

	_, err := Parse(example)
	if err == nil {
		t.Fatal("the example parses now — remove this test and assert it parses instead")
	}

	// Every field the parser refuses, and nothing else.
	want := []string{
		// The document to serve is named on the command line today (T609).
		"spec",
		// Implemented as `execution.allowMutations: false`, said twice (T608).
		"defaultPolicy",
		// Implemented as an unconditional refusal; only `deny` is possible (T607).
		"redirects",
	}
	if got := refusedFields(err.Error()); !equalStrings(got, want) {
		t.Errorf("the parser refuses %v; this test expects %v.\n"+
			"If a field was implemented, take it off the list. If one was added to the "+
			"specification, put it on.", got, want)
	}
}

// specExample reads the YAML block out of docs/spec.md §5.1, so that the test
// tracks the specification rather than a copy of it that can drift too.
func specExample(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "spec.md")
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	section := regexp.MustCompile(`(?s)### 5\.1.*?\x60\x60\x60yaml\n(.*?)\x60\x60\x60`).
		FindSubmatch(document)
	if section == nil {
		t.Fatal("docs/spec.md §5.1 no longer contains a YAML example")
	}
	return section[1]
}

// refusedFields pulls the field names out of the decoder's complaint.
var unknownField = regexp.MustCompile(`field (\S+) not found`)

func refusedFields(message string) []string {
	var fields []string
	for _, match := range unknownField.FindAllStringSubmatch(message, -1) {
		fields = append(fields, match[1])
	}
	sort.Strings(fields)
	return fields
}

func equalStrings(got, want []string) bool {
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	return strings.Join(got, ",") == strings.Join(sorted, ",")
}
