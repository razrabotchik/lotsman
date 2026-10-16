package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/config"
)

var (
	// ErrChanged: the file on disk is not the one the draft was based on.
	ErrChanged = errors.New("the configuration changed on disk since it was loaded; reload to see it")
	// ErrUnacknowledged: a widening change was not acknowledged.
	ErrUnacknowledged = errors.New("every widening change needs its own acknowledgement")
	// ErrRejected: the entry makes the configuration invalid or adds problems.
	ErrRejected   = errors.New("the change was not taken")
	errNotMapping = errors.New("the configuration is not a YAML mapping")
)

// Entry is one pending change: the whole desired operationOverrides entry
// for one operation, componentOverrides entry for one component, or the
// overlays list.
type Entry struct {
	ID           string                    `json:"id"`
	Kind         string                    `json:"kind"` // operation | component | overlays
	OperationKey string                    `json:"operationKey,omitempty"`
	Match        *config.Match             `json:"match,omitempty"`
	Schema       string                    `json:"schema,omitempty"`
	Operation    *config.OperationOverride `json:"operation,omitempty"`
	Component    *config.ComponentOverride `json:"component,omitempty"`
	Overlays     []string                  `json:"overlays,omitempty"`
	Remove       bool                      `json:"remove,omitempty"`
}

// State is everything the frontend draws.
type State struct {
	ConfigPath string     `json:"configPath"`
	Spec       string     `json:"spec,omitempty"`
	Base       *Snapshot  `json:"-"`
	Draft      *Snapshot  `json:"draft"`
	Comparison Comparison `json:"comparison"`
	Entries    []Entry    `json:"entries"`
	// Problems are the draft's problems the configuration on disk does not
	// have.
	Problems []Problem `json:"problems,omitempty"`
}

// Workbench is a draft of one configuration file.
type Workbench struct {
	mu       sync.Mutex
	path     string
	spec     string
	pipe     Pipeline
	disk     []byte
	diskHash string
	entries  map[string]Entry
	order    []string
	base     *Snapshot
}

// New loads the configuration at path (which may not exist yet) and
// evaluates it.
func New(ctx context.Context, path, spec string, pipe Pipeline) (*Workbench, error) {
	w := &Workbench{path: path, spec: spec, pipe: pipe, entries: map[string]Entry{}}
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workbench) readDisk(ctx context.Context) error {
	data, err := os.ReadFile(w.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	w.disk, w.diskHash = data, hashOf(data)
	base, err := w.pipe.Evaluate(ctx, data)
	if err != nil {
		return err
	}
	w.base = base
	return nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// EntryID is the identity of a pending entry: one per operation match, one
// per component, one for the overlays list.
//
//nolint:gocritic // hugeParam: an entry is a value the caller hands over; the id is derived from it, never written back.
func EntryID(e Entry) string {
	switch e.Kind {
	case "operation":
		m := e.Match
		if m == nil && e.Operation != nil {
			m = &e.Operation.Match
		}
		if m == nil {
			return "operation:"
		}
		if m.OperationID != "" {
			return "operation:" + m.OperationID
		}
		return "operation:" + m.Method + " " + m.Path
	case "component":
		if e.Schema == "" && e.Component != nil {
			return "component:" + e.Component.Match.Schema
		}
		return "component:" + e.Schema
	default:
		return "overlays"
	}
}

// draftBytes renders the disk configuration with every pending entry applied.
func (w *Workbench) draftBytes(entries map[string]Entry, order []string) ([]byte, error) {
	doc, err := document(w.disk, w.spec)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	for _, id := range order {
		e := entries[id]
		if err := applyEntry(root, &e); err != nil {
			return nil, err
		}
	}
	if len(order) == 0 && len(w.disk) > 0 {
		return w.disk, nil
	}
	return render(doc)
}

func applyEntry(root *yaml.Node, e *Entry) error {
	switch e.Kind {
	case "operation":
		var match config.Match
		if e.Match != nil {
			match = *e.Match
		} else if e.Operation != nil {
			match = e.Operation.Match
		}
		if !e.Remove && e.Operation == nil {
			return errors.New("an operation entry needs the operation's override")
		}
		seq := sequence(root, "operationOverrides", !e.Remove)
		err := upsert(seq, func(item *yaml.Node) bool { return sameMatch(matchOf(item), match) }, e.Remove, e.Operation)
		dropEmpty(root, "operationOverrides", seq)
		return err
	case "component":
		name := e.Schema
		if name == "" && e.Component != nil {
			name = e.Component.Match.Schema
		}
		if !e.Remove && e.Component == nil {
			return errors.New("a component entry needs the component's override")
		}
		seq := sequence(root, "componentOverrides", !e.Remove)
		err := upsert(seq, func(item *yaml.Node) bool {
			var c config.ComponentOverride
			_ = item.Decode(&c)
			return c.Match.Schema == name
		}, e.Remove, e.Component)
		dropEmpty(root, "componentOverrides", seq)
		return err
	case "overlays":
		_, specNode := lookup(root, "spec")
		if specNode == nil {
			specNode = &yaml.Node{Kind: yaml.MappingNode}
			setKey(root, "spec", specNode)
		}
		if len(e.Overlays) == 0 {
			deleteKey(specNode, "overlays")
			return nil
		}
		node, err := encode(e.Overlays)
		if err != nil {
			return err
		}
		setKey(specNode, "overlays", node)
		return nil
	}
	return fmt.Errorf("unknown entry kind %q", e.Kind)
}

// dropEmpty removes a sequence a removal left empty, so the file does not keep
// an `operationOverrides: []` nobody wrote.
func dropEmpty(root *yaml.Node, key string, seq *yaml.Node) {
	if seq != nil && len(seq.Content) == 0 {
		deleteKey(root, key)
	}
}

func upsert(seq *yaml.Node, is func(*yaml.Node) bool, remove bool, desired any) error {
	if seq == nil {
		return nil
	}
	for i, item := range seq.Content {
		if !is(item) {
			continue
		}
		if remove {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			return nil
		}
		node, err := encode(desired)
		if err != nil {
			return err
		}
		mergeInto(item, node)
		return nil
	}
	if remove {
		return nil
	}
	node, err := encode(desired)
	if err != nil {
		return err
	}
	seq.Content = append(seq.Content, node)
	return nil
}

// State evaluates the current draft.
func (w *Workbench) State(ctx context.Context) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state(ctx, w.entries, w.order)
}

func (w *Workbench) state(ctx context.Context, entries map[string]Entry, order []string) (*State, error) {
	data, err := w.draftBytes(entries, order)
	if err != nil {
		return nil, err
	}
	draft, err := w.pipe.Evaluate(ctx, data)
	if err != nil {
		return nil, err
	}
	cmp := Compare(w.base, draft)
	SortWidening(cmp.Widening)
	st := &State{
		ConfigPath: w.path, Spec: w.spec, Base: w.base, Draft: draft, Comparison: cmp,
		Problems: newProblems(w.base.Problems, draft.Problems),
	}
	for _, id := range order {
		st.Entries = append(st.Entries, entries[id])
	}
	return st, nil
}

// Apply adds or replaces one pending entry. An entry that makes the
// configuration invalid, or adds a problem the draft did not have, is not
// taken: the draft stays as it was (spec §5).
//
//nolint:gocritic // hugeParam: the workbench keeps its own copy of the entry; a pointer would let the caller edit a pending change behind the lock.
func (w *Workbench) Apply(ctx context.Context, e Entry) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.ID = EntryID(e)
	entries, order := w.with(&e)
	before, err := w.state(ctx, w.entries, w.order)
	if err != nil {
		return nil, err
	}
	after, err := w.state(ctx, entries, order)
	if err != nil {
		return nil, err
	}
	if after.Draft.ConfigError != "" && before.Draft.ConfigError == "" {
		return after, fmt.Errorf("%w: %s", ErrRejected, after.Draft.ConfigError)
	}
	if added := newProblems(before.Draft.Problems, after.Draft.Problems); len(added) > 0 {
		return after, fmt.Errorf("%w: %s", ErrRejected, added[0].Message)
	}
	w.entries, w.order = entries, order
	return after, nil
}

func (w *Workbench) with(e *Entry) (entries map[string]Entry, order []string) {
	entries = make(map[string]Entry, len(w.entries)+1)
	for k, v := range w.entries {
		entries[k] = v
	}
	order = append([]string(nil), w.order...)
	if _, exists := entries[e.ID]; !exists {
		order = append(order, e.ID)
	}
	entries[e.ID] = *e
	return entries, order
}

func newProblems(before, after []Problem) []Problem {
	seen := map[string]bool{}
	for _, p := range before {
		seen[p.Message] = true
	}
	var out []Problem
	for _, p := range after {
		if !seen[p.Message] {
			out = append(out, p)
		}
	}
	return out
}

// Revert drops one pending entry.
func (w *Workbench) Revert(ctx context.Context, id string) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.entries[id]; ok {
		delete(w.entries, id)
		for i, existing := range w.order {
			if existing == id {
				w.order = append(w.order[:i], w.order[i+1:]...)
				break
			}
		}
	}
	return w.state(ctx, w.entries, w.order)
}

// Diff is the draft against the file on disk.
func (w *Workbench) Diff() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, err := w.draftBytes(w.entries, w.order)
	if err != nil {
		return "", err
	}
	return UnifiedDiff(filepath.Base(w.path), w.disk, data), nil
}

// Save writes the draft if the file on disk is still the one it is based on
// and every widening change is acknowledged.
func (w *Workbench) Save(ctx context.Context, acknowledged []string) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	current, err := os.ReadFile(w.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if hashOf(current) != w.diskHash {
		return nil, ErrChanged
	}
	st, err := w.state(ctx, w.entries, w.order)
	if err != nil {
		return nil, err
	}
	if st.Draft.ConfigError != "" {
		return st, fmt.Errorf("%w: %s", ErrRejected, st.Draft.ConfigError)
	}
	ack := map[string]bool{}
	for _, id := range acknowledged {
		ack[id] = true
	}
	for _, wd := range st.Comparison.Widening {
		if !ack[wd.ID] {
			return st, fmt.Errorf("%w: %s", ErrUnacknowledged, wd.Message)
		}
	}
	data, err := w.draftBytes(w.entries, w.order)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(w.path, data); err != nil {
		return nil, err
	}
	w.entries, w.order = map[string]Entry{}, nil
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w.state(ctx, w.entries, w.order)
}

// Reload re-reads the file on disk and re-applies the pending entries.
func (w *Workbench) Reload(ctx context.Context) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w.state(ctx, w.entries, w.order)
}

// Explain renders a dry run under the draft.
func (w *Workbench) Explain(ctx context.Context, key string, arguments []byte) (string, error) {
	w.mu.Lock()
	data, err := w.draftBytes(w.entries, w.order)
	w.mu.Unlock()
	if err != nil {
		return "", err
	}
	return w.pipe.Explain(ctx, data, key, arguments)
}

// writeAtomic replaces path with data: a temporary file in the same
// directory, synced, then renamed over it, keeping the existing mode.
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SortedEntryIDs is for tests and stable output.
func SortedEntryIDs(st *State) []string {
	ids := make([]string, 0, len(st.Entries))
	for _, e := range st.Entries {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
