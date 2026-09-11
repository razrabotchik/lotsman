package mcpserver

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// OptionsForTest returns Options with a frozen clock, so that handler output is
// byte-stable in tests (Constitution IV).
func OptionsForTest(now time.Time) Options {
	return Options{now: func() time.Time { return now }}
}

// SDKLoggerForTest exposes the SDK log wrapper.
func SDKLoggerForTest(base *slog.Logger) *slog.Logger { return sdkLogger(base) }

// CleanDisconnectErrForTest returns the error the SDK reports when a client
// closes stdin with a request in flight.
func CleanDisconnectErrForTest() error {
	return fmt.Errorf("%w: %w", &jsonrpc.Error{Code: codeServerClosing, Message: "server is closing"}, io.EOF)
}
