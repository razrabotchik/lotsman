package main_test

import (
	"bytes"
	"errors"
	"sync"
)

// errorAs keeps the e2e assertions readable.
func errorAs(err error, target any) bool { return errors.As(err, target) }

// syncBuffer collects a subprocess's stderr. os/exec copies into it from its
// own goroutine while the test reads, so the buffer needs its own lock.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
