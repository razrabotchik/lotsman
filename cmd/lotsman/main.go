// Command lotsman serves an OpenAPI specification as MCP tools.
//
// This file is wiring only: flag parsing, logger construction, signal handling
// and dependency assembly by hand (Constitution VII — no DI framework).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
)

// Exit codes. Distinct codes per error class arrive with T034 (FR-77);
// until then only these three are used.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `lotsman — security-first OpenAPI → MCP runtime.

Usage:
  lotsman serve [SPEC]     Serve MCP over stdio (stdout is protocol-only)
  lotsman operations SPEC  List parsed operations as a table
  lotsman version          Print build, MCP SDK and protocol identity
  lotsman help             Print this message

Flags:
  --log-level LEVEL      debug|info|warn|error (default info, env LOTSMAN_LOG_LEVEL)
  --json                 version: machine-readable output
  --rejected             operations: show only rejected operations
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop() // not deferred: os.Exit below would skip it
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return serve(ctx, rest, stderr)
	case "operations":
		return operations(ctx, rest, stdout, stderr)
	case "version":
		return version(rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "lotsman: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

func serve(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	level := fs.String("log-level", envOr("LOTSMAN_LOG_LEVEL", "info"), "debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	logger, err := newLogger(stderr, *level)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitUsage
	}

	// The spec path is accepted now so that registrations and `make run SPEC=...`
	// do not change shape later; loading lands in T005–T011.
	if spec := fs.Arg(0); spec != "" {
		logger.Warn("spec loading is not wired yet; serving the ping tool only", "spec", spec)
	}

	if err := mcpserver.ServeStdio(ctx, mcpserver.Options{Logger: logger}); err != nil {
		logger.Error("serve failed", "error", err)
		return exitError
	}
	return exitOK
}

func version(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	info := buildinfo.Get()
	if *asJSON {
		enc := newJSONEncoder(stdout)
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "lotsman: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fmt.Fprintln(stdout, info)
	return exitOK
}

// newLogger builds the stderr logger. Nothing in the process may log to stdout:
// on stdio transport stdout carries MCP frames only.
func newLogger(stderr io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q: want debug|info|warn|error", level)
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl})), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitFlags separates "-"-prefixed flags from positional arguments so a
// flag.FlagSet can parse them regardless of order. It only supports boolean
// flags (no "--flag value" pairs): every -prefixed token is independent, so
// this cannot tell a value-taking flag's value from the next positional
// argument.
func splitFlags(args []string) (flagArgs, posArgs []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			posArgs = append(posArgs, a)
		}
	}
	return flagArgs, posArgs
}
