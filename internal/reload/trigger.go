package reload

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// DefaultWatchInterval is how often a watched file is examined.
//
// Polling rather than a filesystem notification API, which would be a
// dependency (Constitution VII) for a feature whose whole job is to notice a
// file every few seconds. The cost is one stat per interval; the benefit is
// that it behaves the same on every platform and through every kind of mount,
// including the container volumes this is actually for.
const DefaultWatchInterval = 2 * time.Second

// Triggers is what may cause a reload.
type Triggers struct {
	// Signal reloads on SIGHUP. It is free, has no surface, and is what a
	// sidecar or a config-map reloader already sends.
	Signal bool
	// Watch is a path to poll; empty disables watching.
	//
	// For an exploded specification the root document is an index, and every
	// edit an operator makes is to one of the documents it points at -- so
	// watching this path alone is a feature that appears to work and silently
	// does not. Documents supplies the rest.
	Watch string
	// Documents returns every other path to poll, re-asked on each interval
	// because a reload can change the set: a `$ref` added to the index brings a
	// new document, and it has to be watched from then on.
	//
	// Nil means "the root only". The cost is one stat per path per interval:
	// 2,910 documents -- the largest specification in the corpus -- measure at
	// about 3 ms, so completeness here is cheaper than the surprise of missing
	// an edit.
	Documents func() []string
	// Interval overrides DefaultWatchInterval.
	Interval time.Duration
}

// paths is everything to poll: the root, then whatever Documents names now.
func (t Triggers) paths() []string {
	if t.Watch == "" {
		return nil
	}
	paths := []string{t.Watch}
	if t.Documents != nil {
		paths = append(paths, t.Documents()...)
	}
	return paths
}

func (t Triggers) interval() time.Duration {
	if t.Interval > 0 {
		return t.Interval
	}
	return DefaultWatchInterval
}

// Start runs the configured triggers until the context is cancelled.
//
// It returns immediately; reloads happen on their own goroutine, so a
// candidate that takes seconds to parse never blocks a call being served in
// the meantime. Errors are the Holder's to report -- a failed reload is not a
// reason to stop watching, since the next edit may be the fix.
func Start(ctx context.Context, h *Holder, triggers Triggers) {
	if !triggers.Signal && triggers.Watch == "" {
		return
	}
	hangup := make(chan os.Signal, 1)
	if triggers.Signal {
		signal.Notify(hangup, syscall.SIGHUP)
	}
	go func() {
		defer signal.Stop(hangup)
		watcher := newWatcher(triggers.paths, triggers.interval())
		defer watcher.stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hangup:
				_, _ = h.Reload(ctx)
			case <-watcher.changed:
				_, _ = h.Reload(ctx)
			}
		}
	}()
}

// watcher reports that a file has changed and then stopped changing.
type watcher struct {
	changed chan struct{}
	ticker  *time.Ticker
	done    chan struct{}
}

// newWatcher polls whatever paths returns. A nil or empty set yields a watcher
// that never fires, so the caller's select needs no special case.
func newWatcher(paths func() []string, interval time.Duration) *watcher {
	w := &watcher{changed: make(chan struct{}, 1), done: make(chan struct{})}
	if paths == nil || len(paths()) == 0 {
		return w
	}
	w.ticker = time.NewTicker(interval)
	go w.poll(paths)
	return w
}

// poll fires once a file has changed and then held still for a full interval.
//
// The wait is the point. A document being written is a document mid-write,
// and reloading on the first sign of movement means parsing half a file and
// reporting a failure that was never real. Settling first costs one interval
// and removes the entire class.
func (w *watcher) poll(paths func() []string) {
	last, ok := fingerprintAll(paths())
	settling := false
	for {
		select {
		case <-w.done:
			return
		case <-w.ticker.C:
		}
		current, exists := fingerprintAll(paths())
		switch {
		case !exists:
			// A document that is gone is not a change to reload; it is usually
			// the middle of an atomic replace. Wait for it to come back.
			settling = false
		case !ok || current != last:
			last, ok = current, true
			settling = true
		case settling:
			settling = false
			select {
			case w.changed <- struct{}{}:
			default: // a reload is already pending; one is enough
			}
		}
	}
}

func (w *watcher) stop() {
	close(w.done)
	if w.ticker != nil {
		w.ticker.Stop()
	}
}

// state is what a poll compares. Size and modification time miss an edit that
// changes neither, which is a trade this makes knowingly: the alternative is
// reading and hashing the file every interval, and the operator who needs
// that has SIGHUP.
type state struct {
	size    int64
	modTime time.Time
}

func fingerprint(path string) (state, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return state{}, false
	}
	return state{size: info.Size(), modTime: info.ModTime()}, true
}

// fingerprintAll folds the set into one comparable value, reported absent if
// any document is missing -- which, mid-replace, is what the settle wait is for.
//
// The fold is order-dependent on purpose: `paths` is derived from a sorted
// manifest, so a reordering means the set changed, and a set that changed is a
// change.
func fingerprintAll(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	var folded strings.Builder
	for _, path := range paths {
		one, ok := fingerprint(path)
		if !ok {
			return "", false
		}
		fmt.Fprintf(&folded, "%s\x00%d\x00%d\n", path, one.size, one.modTime.UnixNano())
	}
	return folded.String(), true
}
