package workbench

import (
	"bytes"
	"reflect"
	"strings"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/config"
)

// document parses a configuration into a node tree, or starts a new one.
// Comments live on the nodes, so everything the workbench does not touch is
// written back as the person wrote it.
func document(data []byte, spec string) (*yaml.Node, error) {
	// Line endings are render's concern: the tree is always read as LF, so
	// comments carry no stray CR.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 && len(doc.Content) == 0 {
		// Empty, or only comments (the parser drops those): a new
		// configuration that keeps what the person wrote at the top.
		root := &yaml.Node{Kind: yaml.MappingNode, HeadComment: strings.TrimSpace(string(data))}
		setKey(root, "apiVersion", scalar(config.APIVersion))
		if spec != "" {
			specNode := &yaml.Node{Kind: yaml.MappingNode}
			setKey(specNode, "source", scalar(spec))
			setKey(root, "spec", specNode)
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errNotMapping
	}
	return &doc, nil
}

// render writes doc back. The file is the person's: it keeps the
// indentation they use, never folds a long line, and every line the edit did
// not touch is written back byte for byte (see splice).
func render(doc *yaml.Node, source []byte) ([]byte, error) {
	// A file written with CRLF line endings is worked on as LF and written
	// back with CRLF throughout, so new lines do not mix endings with old.
	if crlf(source) {
		out, err := render(doc, bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")))
		if err != nil {
			return nil, err
		}
		return bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n")), nil
	}
	style := styleOf(source)
	out, err := dump(doc, style)
	if err != nil || len(bytes.TrimSpace(source)) == 0 {
		return out, err
	}
	orig, err := document(source, "")
	if err != nil {
		return out, nil //nolint:nilerr // the source does not parse: nothing to keep, the rendering stands.
	}
	base, err := dump(orig, style)
	if err != nil {
		return out, nil //nolint:nilerr // as above.
	}
	spliced := splice(source, base, out)
	// The spliced file must say exactly what the rendering says; if mixing
	// the person's lines with the encoder's ever changed the meaning (an
	// indentation read wrong), the rendering is written instead.
	if !sameYAML(spliced, out) {
		return out, nil
	}
	return spliced, nil
}

// crlf says whether the file ends its lines with CRLF (its first line does).
func crlf(source []byte) bool {
	i := bytes.IndexByte(source, '\n')
	return i > 0 && source[i-1] == '\r'
}

func sameYAML(x, y []byte) bool {
	var a, b any
	if yaml.Unmarshal(x, &a) != nil || yaml.Unmarshal(y, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// layout is how a file indents.
type layout struct {
	indent  int
	compact bool // "- " at the column of its parent key
}

// styleOf reads the indentation off the source: the first indented mapping
// key gives the width, the first sequence item says whether "- " sits at its
// parent's column. A new file gets two spaces, items indented.
func styleOf(source []byte) layout {
	style := layout{indent: 2}
	lines := strings.Split(string(source), "\n")
	foundIndent, foundSeq := false, false
	prevCol, prevContent, prevIsKey := -1, -1, false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		col := len(line) - len(trimmed)
		item := strings.HasPrefix(trimmed, "- ") || trimmed == "-"
		content := col // the column of what the line says, past any "- "
		if item {
			content = col + 1 + len(trimmed[1:]) - len(strings.TrimLeft(trimmed[1:], " "))
		}
		// A key nested under a key line: the step between them is the
		// indentation.
		if !foundIndent && !item && prevIsKey && col > prevContent {
			if step := col - prevContent; step >= 2 && step <= 9 {
				style.indent = step
			}
			foundIndent = true
		}
		if !foundSeq && item && prevIsKey {
			style.compact = col == prevCol
			foundSeq = true
		}
		prevCol, prevContent = col, content
		prevIsKey = strings.HasSuffix(strings.TrimRight(trimmed, " "), ":")
		if foundIndent && foundSeq {
			break
		}
	}
	return style
}

func dump(doc *yaml.Node, style layout) ([]byte, error) {
	var out bytes.Buffer
	d, err := yaml.NewDumper(&out, yaml.WithV3Defaults(), yaml.WithIndent(style.indent),
		yaml.WithCompactSeqIndent(style.compact), yaml.WithLineWidth(-1))
	if err != nil {
		return nil, err
	}
	if err := d.Dump(doc); err != nil {
		return nil, err
	}
	if err := d.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// splice keeps the person's lines. base is the source rendered unchanged,
// out the source rendered with the edits; the lines out shares with base are
// taken from the source as written (spacing, quotes, flow style, blank
// lines), and only the lines the edit changed come from out. When base does
// not line up with the source line for line (a multi-line flow value the
// encoder joined, say), out is returned as it is.
func splice(source, base, out []byte) []byte {
	src := splitLines(string(source))
	baseLines := splitLines(string(base))
	var kept []int // indices of src lines that are not blank
	for i, line := range src {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, i)
		}
	}
	if len(kept) != len(baseLines) {
		return out
	}
	for i, b := range baseLines {
		// A blank rendered line (inside a block scalar) has no source line
		// to pair with: blank source lines are not counted.
		x, y := strings.TrimSpace(src[kept[i]]), strings.TrimSpace(b)
		if x == "" || y == "" {
			return out
		}
		if x[0] != y[0] && !isQuote(x[0]) && !isQuote(y[0]) {
			return out
		}
	}
	ops := lcsOps(baseLines, splitLines(string(out)))
	if ops == nil {
		return source
	}
	var b strings.Builder
	blanksBefore := func(i int) { // the blank source lines before base line i
		from := 0
		if i > 0 {
			from = kept[i-1] + 1
		}
		for _, line := range src[from:kept[i]] {
			b.WriteString(line + "\n")
		}
	}
	for _, op := range ops {
		switch op.kind {
		case ' ':
			blanksBefore(op.aIdx - 1)
			b.WriteString(src[kept[op.aIdx-1]] + "\n")
		case '-':
			blanksBefore(op.aIdx - 1)
		default:
			b.WriteString(op.line + "\n")
		}
	}
	if len(kept) > 0 {
		for _, line := range src[kept[len(kept)-1]+1:] {
			b.WriteString(line + "\n")
		}
	}
	return []byte(b.String())
}

func isQuote(c byte) bool { return c == '"' || c == '\'' }

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func lookup(m *yaml.Node, key string) (index int, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i, m.Content[i+1]
		}
	}
	return -1, nil
}

// setKey replaces a key's value in place, keeping the key node and its
// comments, or appends the pair.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	if i, _ := lookup(m, key); i >= 0 {
		value.HeadComment, value.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
		m.Content[i+1] = value
		return
	}
	m.Content = append(m.Content, scalar(key), value)
	m.Content[len(m.Content)-2].Tag = ""
}

func deleteKey(m *yaml.Node, key string) {
	if i, _ := lookup(m, key); i >= 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}

// sequence returns the root's sequence under key, creating it if asked.
func sequence(root *yaml.Node, key string, create bool) *yaml.Node {
	if _, seq := lookup(root, key); seq != nil && seq.Kind == yaml.SequenceNode {
		return seq
	}
	if !create {
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	setKey(root, key, seq)
	return seq
}

// encode turns a value into a node using the config package's yaml tags.
func encode(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}

// mergeInto makes item say what desired says, key by key: unchanged keys keep
// their nodes (and comments), changed ones are replaced, absent ones removed.
func mergeInto(item, desired *yaml.Node) {
	keep := map[string]bool{}
	for i := 0; i+1 < len(desired.Content); i += 2 {
		key := desired.Content[i].Value
		keep[key] = true
		if _, current := lookup(item, key); current != nil && sameNode(current, desired.Content[i+1]) {
			continue
		}
		setKey(item, key, desired.Content[i+1])
	}
	for i := 0; i+1 < len(item.Content); {
		if !keep[item.Content[i].Value] {
			item.Content = append(item.Content[:i], item.Content[i+2:]...)
			continue
		}
		i += 2
	}
}

// sameNode says whether two nodes hold the same data, whatever their style:
// a flow mapping or a quoted scalar the person wrote is kept when the value
// it says has not changed.
func sameNode(a, b *yaml.Node) bool {
	var x, y any
	if a.Decode(&x) != nil || b.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func matchOf(item *yaml.Node) config.Match {
	var m config.Match
	if _, node := lookup(item, "match"); node != nil {
		_ = node.Decode(&m)
	}
	return m
}

func sameMatch(a, b config.Match) bool {
	return a.OperationID == b.OperationID && strings.EqualFold(a.Method, b.Method) && a.Path == b.Path
}
