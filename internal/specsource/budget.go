package specsource

import (
	"errors"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// Structural budget defaults (pipeline.md stage 0). A byte limit alone does
// not bound a YAML document: anchors and aliases make expansion multiplicative,
// so two kilobytes can describe gigabytes ("billion laughs"). What has to be
// bounded is the *expanded* node count, before any consumer walks the tree.
//
// The numbers are chosen so that every real spec in the M0 corpus passes with
// room to spare -- a 10 MiB document cannot physically contain more than a few
// hundred thousand nodes -- while a bomb fails within milliseconds.
const (
	DefaultMaxNodes         = 1_000_000 // structural nodes, before alias expansion
	DefaultMaxAliases       = 10_000    // alias occurrences; real specs use a handful
	DefaultMaxExpandedNodes = 5_000_000 // nodes after alias expansion: the bomb budget
	DefaultMaxDepth         = 200       // nesting depth; also bounds our own recursion
)

// ErrMalformed is returned when the source is not valid YAML (JSON is a YAML
// subset, so this covers both). Stage 0 does not interpret the document, but
// it must be able to walk its node tree to bound it.
var ErrMalformed = errors.New("specsource: document is not valid YAML or JSON")

// ErrBudgetExceeded is returned when the document exceeds a structural budget.
var ErrBudgetExceeded = errors.New("specsource: document exceeds a structural budget")

// checkBudget parses data into a node tree and enforces the structural
// budgets. Decoding into a *yaml.Node deliberately does not expand aliases --
// they stay as alias nodes pointing at their anchor -- which is what makes it
// safe to measure the expansion cost without paying it.
func checkBudget(data []byte, opts Options) error {
	if len(data) == 0 {
		return nil // an empty document has nothing to expand; stage 1 rejects it
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return errs.Errorf(errs.ClassSpecInvalid, "%w: %v", ErrMalformed, err)
	}

	counts := counter{
		maxNodes:   opts.maxNodes(),
		maxAliases: opts.maxAliases(),
		maxDepth:   opts.maxDepth(),
	}
	if err := counts.walk(&root, 1); err != nil {
		return err
	}

	limit := opts.maxExpandedNodes()
	cost, err := expandedCost(&root, make(map[*yaml.Node]int64), make(map[*yaml.Node]bool), limit)
	if err != nil {
		return err
	}
	if cost > limit {
		return errs.Errorf(errs.ClassSpecInvalid,
			"%w: alias expansion reaches more than %d nodes (%d aliases over %d nodes)",
			ErrBudgetExceeded, limit, counts.aliases, counts.nodes)
	}
	return nil
}

// counter walks the tree structurally: it counts nodes as authored, never
// following an alias, so the walk is linear in document size.
type counter struct {
	nodes, aliases       int64
	maxNodes, maxAliases int64
	maxDepth             int
}

func (c *counter) walk(n *yaml.Node, depth int) error {
	if n == nil {
		return nil
	}
	if depth > c.maxDepth {
		return errs.Errorf(errs.ClassSpecInvalid,
			"%w: nesting deeper than %d levels", ErrBudgetExceeded, c.maxDepth)
	}
	c.nodes++
	if c.nodes > c.maxNodes {
		return errs.Errorf(errs.ClassSpecInvalid,
			"%w: more than %d nodes", ErrBudgetExceeded, c.maxNodes)
	}
	if n.Kind == yaml.AliasNode {
		c.aliases++
		if c.aliases > c.maxAliases {
			return errs.Errorf(errs.ClassSpecInvalid,
				"%w: more than %d aliases", ErrBudgetExceeded, c.maxAliases)
		}
		return nil // the anchor is counted where it is defined
	}
	for _, child := range n.Content {
		if err := c.walk(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// expandedCost returns how many nodes the subtree would occupy once every
// alias is expanded, saturating at limit+1 so a bomb cannot overflow the
// counter it is being measured against.
//
// memo makes this linear in the number of distinct nodes rather than
// exponential in alias depth -- the whole point of measuring instead of
// expanding. visiting guards against a self-referential anchor, which a
// hostile document can contain even though valid YAML should not.
func expandedCost(n *yaml.Node, memo map[*yaml.Node]int64, visiting map[*yaml.Node]bool, limit int64) (int64, error) {
	if n == nil {
		return 0, nil
	}
	if cost, ok := memo[n]; ok {
		return cost, nil
	}
	if visiting[n] {
		return 0, errs.Errorf(errs.ClassSpecInvalid,
			"%w: anchor %q refers to itself", ErrBudgetExceeded, n.Anchor)
	}
	visiting[n] = true
	defer delete(visiting, n)

	total := int64(1)
	if n.Kind == yaml.AliasNode {
		cost, err := expandedCost(n.Alias, memo, visiting, limit)
		if err != nil {
			return 0, err
		}
		total = saturatingAdd(total, cost, limit)
	}
	for _, child := range n.Content {
		if total > limit {
			break
		}
		cost, err := expandedCost(child, memo, visiting, limit)
		if err != nil {
			return 0, err
		}
		total = saturatingAdd(total, cost, limit)
	}

	memo[n] = total
	return total, nil
}

// saturatingAdd stops at limit+1: past the budget the exact size is both
// unrepresentable and irrelevant.
func saturatingAdd(a, b, limit int64) int64 {
	sum := a + b
	if sum < a || sum > limit {
		return limit + 1
	}
	return sum
}
