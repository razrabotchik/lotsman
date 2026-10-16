package workbench

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a line diff of two texts with three lines of context,
// in the familiar unified format. It is for configuration files a person
// reads, so a quadratic LCS is fine and a dependency is not.
func UnifiedDiff(name string, before, after []byte) string {
	a, b := splitLines(string(before)), splitLines(string(after))
	ops := lcsOps(a, b)
	if len(ops) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (on disk)\n+++ %s (draft)\n", name, name)
	const context = 3
	for start := 0; start < len(ops); {
		// Find the next change.
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start == len(ops) {
			break
		}
		from := max(start-context, 0)
		end := start
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*context || run == len(ops) {
				end = min(end+context, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := hunkCoordinates(ops, from, end)
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, op := range ops[from:end] {
			fmt.Fprintf(&out, "%c%s\n", op.kind, op.line)
		}
		start = end
	}
	return out.String()
}

type diffOp struct {
	kind       byte // ' ', '-', '+'
	line       string
	aIdx, bIdx int // 1-based line numbers in a and b (0 when absent)
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	var ops []diffOp
	changed := false
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i + 1, j + 1})
			i++
			j++
		case i < n && (j == m || table[i+1][j] >= table[i][j+1]):
			// Removals first, so a replaced line reads "-old" then "+new".
			ops = append(ops, diffOp{'-', a[i], i + 1, 0})
			i++
			changed = true
		default:
			ops = append(ops, diffOp{'+', b[j], 0, j + 1})
			j++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return ops
}

func hunkCoordinates(ops []diffOp, from, end int) (aStart, bStart, aLen, bLen int) {
	for _, op := range ops[from:end] {
		if op.kind != '+' {
			if aStart == 0 {
				aStart = op.aIdx
			}
			aLen++
		}
		if op.kind != '-' {
			if bStart == 0 {
				bStart = op.bIdx
			}
			bLen++
		}
	}
	return aStart, bStart, aLen, bLen
}
