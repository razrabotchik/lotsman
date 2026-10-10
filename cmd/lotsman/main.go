// Command lotsman serves an OpenAPI specification as MCP tools.
//
// This is the process and nothing else: signals in, an exit code out. The
// command line itself is internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/razrabotchik/lotsman/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop() // not deferred: os.Exit below would skip it
	os.Exit(code)
}
