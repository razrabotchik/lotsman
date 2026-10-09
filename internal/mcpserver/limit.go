package mcpserver

import "github.com/razrabotchik/lotsman/internal/errs"

// DefaultMaxConcurrentCalls is execution.maxConcurrentCalls when the
// configuration does not say.
const DefaultMaxConcurrentCalls = 64

// CallLimit bounds the tool calls in flight at once.
//
// It is one value for the process, not one per server: a reload builds a new
// server for the new catalog while calls on the old one finish, and a limit
// that reset with every reload would be a limit a reload could double.
//
// A call over the limit is refused, not queued. A queue is a second budget --
// how long may a call wait -- and a caller that is told "try again" decides
// that better than a runtime holding its request open on its behalf.
type CallLimit struct {
	max   int
	slots chan struct{}
}

// NewCallLimit returns a limit of max calls; zero or less means the default.
func NewCallLimit(maxCalls int) *CallLimit {
	if maxCalls <= 0 {
		maxCalls = DefaultMaxConcurrentCalls
	}
	return &CallLimit{max: maxCalls, slots: make(chan struct{}, maxCalls)}
}

// acquire takes a slot, or refuses when none is free. The returned release
// must be called exactly once.
func (l *CallLimit) acquire() (release func(), err error) {
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	default:
		return nil, errs.Errorf(errs.ClassPolicy,
			"lotsman: %d calls are already in flight, which is the limit "+
				"(execution.maxConcurrentCalls); nothing was sent -- try again shortly", l.max)
	}
}

// InFlight is how many calls hold a slot now.
func (l *CallLimit) InFlight() int { return len(l.slots) }

// Max is the limit.
func (l *CallLimit) Max() int { return l.max }
