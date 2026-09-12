package main_test

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// approving is the client options of a user who says yes.
//
// Every mutation needs an answer now that `interactiveApproval` defaults to
// `always` (FR-44), and a client that cannot be asked is refused before the
// network -- which is what TestApprovalFailsClosedWithoutClientCapability
// exists to prove. The tests that are about something else say yes and get on
// with it.
func approving() *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	}
}
