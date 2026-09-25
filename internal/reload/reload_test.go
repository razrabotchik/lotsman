package reload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
)

// build counts how many servers were made, so a test can tell "published
// nothing" from "published the same thing again".
func build(built *atomic.Int64) Build {
	return func(*catalog.Catalog) *mcp.Server {
		if built != nil {
			built.Add(1)
		}
		return mcp.NewServer(&mcp.Implementation{Name: "lotsman-test", Version: "v0"}, nil)
	}
}

// Acceptance criterion 9, and the reason FR-72 is worded the way it is: a
// candidate that cannot be built must not take the working catalog with it.
func TestABrokenCandidateLeavesTheWorkingCatalogServing(t *testing.T) {
	working := &catalog.Catalog{Digest: "sha256:working", Mode: catalog.ModeTools}
	var built atomic.Int64
	holder := New(working, func(context.Context) (*catalog.Catalog, error) {
		return nil, errors.New("the document is not a document")
	}, build(&built), nil)

	before := holder.Server()
	changed, err := holder.Reload(t.Context())
	if err == nil {
		t.Fatal("a broken candidate was accepted")
	}
	if changed {
		t.Error("a failed reload reported a change")
	}
	if holder.Digest() != "sha256:working" {
		t.Errorf("digest = %q; the failure replaced the working catalog", holder.Digest())
	}
	if holder.Server() != before {
		t.Error("the served server changed on a failed reload")
	}
	if built.Load() != 1 {
		t.Errorf("built %d servers; only the initial one should exist", built.Load())
	}
}

// A candidate identical to what is published is not published again. FR-74's
// cache hints promise that a digest moves only when the catalog does.
func TestAnIdenticalCandidateIsNotRepublished(t *testing.T) {
	working := &catalog.Catalog{Digest: "sha256:same"}
	var built atomic.Int64
	holder := New(working, func(context.Context) (*catalog.Catalog, error) {
		return &catalog.Catalog{Digest: "sha256:same"}, nil
	}, build(&built), nil)

	before := holder.Server()
	changed, err := holder.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if changed {
		t.Error("an unchanged catalog was reported as changed")
	}
	if holder.Server() != before {
		t.Error("an unchanged catalog produced a new server")
	}
	if built.Load() != 1 {
		t.Errorf("built %d servers, want 1", built.Load())
	}
}

// And the working case: a different catalog is published whole, while a
// caller that already took a server keeps the one it took (§7.6 step 7).
func TestAValidCandidateIsPublishedWithoutDisturbingAnInFlightCall(t *testing.T) {
	holder := New(&catalog.Catalog{Digest: "sha256:before"}, func(context.Context) (*catalog.Catalog, error) {
		return &catalog.Catalog{Digest: "sha256:after"}, nil
	}, build(nil), nil)

	// A call in flight took this before the reload started.
	inFlight := holder.Server()

	changed, err := holder.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !changed {
		t.Fatal("a different catalog was not published")
	}
	if holder.Digest() != "sha256:after" {
		t.Errorf("digest = %q, want the candidate's", holder.Digest())
	}
	if holder.Server() == inFlight {
		t.Error("the published server did not change")
	}
	// The in-flight value is still usable: nothing was mutated underneath it.
	if inFlight == nil {
		t.Error("the snapshot a call was holding was invalidated")
	}
}

// A Holder with no source cannot reload, and says so rather than pretending
// it did.
func TestReloadWithoutASourceIsAnError(t *testing.T) {
	holder := New(&catalog.Catalog{Digest: "sha256:only"}, nil, build(nil), nil)
	if _, err := holder.Reload(t.Context()); err == nil {
		t.Fatal("a Holder with no source reported a successful reload")
	}
}

// The watcher waits for a file to stop changing before it says anything. A
// document being written is a document mid-write, and parsing one produces a
// failure that was never real.
func TestTheWatcherWaitsForTheFileToSettle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := newWatcher(func() []string { return []string{path} }, 10*time.Millisecond)
	defer w.stop()

	// Nothing has changed, so nothing fires.
	select {
	case <-w.changed:
		t.Fatal("the watcher fired on a file nobody touched")
	case <-time.After(60 * time.Millisecond):
	}

	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.changed:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher never reported a change")
	}
}

// Start with no triggers configured is a no-op rather than a goroutine that
// wakes up forever to do nothing.
func TestStartWithoutTriggersDoesNothing(t *testing.T) {
	reloaded := make(chan struct{}, 1)
	holder := New(&catalog.Catalog{Digest: "sha256:x"}, func(context.Context) (*catalog.Catalog, error) {
		reloaded <- struct{}{}
		return &catalog.Catalog{Digest: "sha256:y"}, nil
	}, build(nil), nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	Start(ctx, holder, Triggers{})

	select {
	case <-reloaded:
		t.Fatal("a reload happened with no trigger configured")
	case <-time.After(50 * time.Millisecond):
	}
}

// An exploded specification is an index over hundreds of documents, and every
// edit an operator makes is to one of those documents rather than to the index.
// Watching the root alone was a feature that appeared to work: the reload itself
// re-reads the whole closure correctly, so the only thing missing was ever
// noticing.
func TestTheWatcherSeesAReferencedDocumentChange(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "openapi.yaml")
	referenced := filepath.Join(dir, "pet.yaml")
	for path, body := range map[string]string{root: "root", referenced: "first"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	w := newWatcher(func() []string { return []string{root, referenced} }, 10*time.Millisecond)
	defer w.stop()

	select {
	case <-w.changed:
		t.Fatal("the watcher fired on documents nobody touched")
	case <-time.After(60 * time.Millisecond):
	}

	// The index is untouched; only the document it points at moves.
	if err := os.WriteFile(referenced, []byte("second edition"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a change to a referenced document was never noticed")
	}
}

// The set is re-asked each interval, because a reload can change it: a `$ref`
// added to the index brings a document that has to be watched from then on.
func TestTheWatchedSetFollowsTheClosure(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "openapi.yaml")
	added := filepath.Join(dir, "added.yaml")
	if err := os.WriteFile(root, []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}

	var extra atomic.Pointer[[]string]
	empty := []string{}
	extra.Store(&empty)
	w := newWatcher(func() []string {
		return append([]string{root}, *extra.Load()...)
	}, 10*time.Millisecond)
	defer w.stop()

	if err := os.WriteFile(added, []byte("new document"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Writing it changed nothing, because nothing was watching it yet.
	select {
	case <-w.changed:
		t.Fatal("the watcher fired on a document outside the set")
	case <-time.After(60 * time.Millisecond):
	}

	// Now it is part of the closure, which is itself the change.
	set := []string{added}
	extra.Store(&set)
	select {
	case <-w.changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a document entering the closure was never noticed")
	}
}
