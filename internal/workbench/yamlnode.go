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
	if len(bytes.TrimSpace(data)) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "apiVersion", scalar(config.APIVersion))
		if spec != "" {
			specNode := &yaml.Node{Kind: yaml.MappingNode}
			setKey(specNode, "source", scalar(spec))
			setKey(root, "spec", specNode)
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errNotMapping
	}
	return &doc, nil
}

func render(doc *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

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
