package workbench

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()
	before := "a\nb\nc\nd\ne\nf\ng\nh\n"
	after := "a\nb\nc\nX\ne\nf\ng\nh\nY\n"
	d := UnifiedDiff("lotsman.yaml", []byte(before), []byte(after))
	for _, want := range []string{"--- lotsman.yaml (on disk)", "+++ lotsman.yaml (draft)", "-d", "+X", "+Y", "@@ -1,"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if UnifiedDiff("x", []byte(before), []byte(before)) != "" {
		t.Error("identical texts produced a diff")
	}
	if !strings.Contains(UnifiedDiff("x", nil, []byte("new\n")), "+new") {
		t.Error("diff from empty")
	}
}

// Two changes far apart are two hunks, each with its own header and only
// three lines of context around it.
func TestUnifiedDiffSplitsDistantChanges(t *testing.T) {
	t.Parallel()
	var before, after strings.Builder
	for i := range 30 {
		line := string(rune('a'+i%26)) + "\n"
		before.WriteString(line)
		switch i {
		case 2:
			after.WriteString("FIRST\n")
		case 25:
			after.WriteString("SECOND\n")
		default:
			after.WriteString(line)
		}
	}
	d := UnifiedDiff("f", []byte(before.String()), []byte(after.String()))
	if n := strings.Count(d, "@@ -"); n != 2 {
		t.Fatalf("hunks = %d, want 2:\n%s", n, d)
	}
	if strings.Contains(d, " k\n") {
		t.Fatalf("a line far from both changes was shown:\n%s", d)
	}
}

// A replaced line reads as the old one removed, then the new one added.
func TestUnifiedDiffRemovesBeforeAdding(t *testing.T) {
	t.Parallel()
	d := UnifiedDiff("f", []byte("a\nb\nc\n"), []byte("a\nX\nY\nc\n"))
	if !strings.Contains(d, "@@ -1,3 +1,4 @@\n a\n-b\n+X\n+Y\n c\n") {
		t.Fatalf("diff:\n%s", d)
	}
}
