package reload

import (
	"context"
	"os"
	"os/signal"
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
	Watch string
	// Interval overrides DefaultWatchInterval.
	Interval time.Duration
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
		watcher := newWatcher(triggers.Watch, triggers.interval())
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

// newWatcher polls path. An empty path yields a watcher that never fires, so
// the caller's select needs no special case.
func newWatcher(path string, interval time.Duration) *watcher {
	w := &watcher{changed: make(chan struct{}, 1), done: make(chan struct{})}
	if path == "" {
		return w
	}
	w.ticker = time.NewTicker(interval)
	go w.poll(path)
	return w
}

// poll fires once a file has changed and then held still for a full interval.
//
// The wait is the point. A document being written is a document mid-write,
// and reloading on the first sign of movement means parsing half a file and
// reporting a failure that was never real. Settling first costs one interval
// and removes the entire class.
func (w *watcher) poll(path string) {
	last, ok := fingerprint(path)
	settling := false
	for {
		select {
		case <-w.done:
			return
		case <-w.ticker.C:
		}
		current, exists := fingerprint(path)
		switch {
		case !exists:
			// A file that is gone is not a change to reload; it is usually
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
