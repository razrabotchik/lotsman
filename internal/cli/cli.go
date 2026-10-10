package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// Exit codes. The error class of every failure is already carried by
// internal/errs and logged; T034 maps those classes onto the distinct codes
// in contracts/cli.md (FR-77). Until that contract lands, only these three
// are used.
const (
	exitOK          = 0
	exitError       = 1 // runtime: I/O, internal, upstream transport
	exitUsage       = 2 // invalid usage or configuration
	exitSpecInvalid = 3 // the document cannot be trusted or parsed
	exitUnsupported = 4 // valid, but not translatable or not permitted
	exitAuth        = 5 // a credential could not be resolved or applied
)

// exitCode maps an error class onto the documented exit codes (FR-77,
// contracts/cli.md).
//
// The mapping lives here, at the boundary, rather than on the class itself:
// the same classification drives MCP error text and reports, and a package
// that knows about process exit codes would be a package that cannot be used
// by anything but a CLI.
func exitCode(err error) int {
	switch errs.ClassOf(err) {
	case errs.ClassUsage:
		return exitUsage
	case errs.ClassSpecInvalid:
		return exitSpecInvalid
	case errs.ClassUnsupported, errs.ClassPolicy:
		// "Valid but refused" from lotsman and from the operator's policy are
		// the same thing to a CI script: the spec is fine, the call is not
		// going to happen.
		return exitUnsupported
	case errs.ClassAuth:
		return exitAuth
	default: // internal, upstream
		return exitError
	}
}

const usage = `lotsman — security-first OpenAPI → MCP runtime. Lotsman doesn't guess.

Usage:
  lotsman serve SPEC       Serve an OpenAPI document as MCP tools (stdio, or --transport=http)
  lotsman inspect SPEC     What lotsman makes of a document, and why (--json for CI)
  lotsman validate SPEC    Is this document usable? The exit code is the answer
  lotsman operations SPEC  One line per operation: effect, support, executable
  lotsman explain-call OP  What a call would do — without making it
  lotsman config check F   Is this configuration usable? The exit code is the answer
  lotsman config export F  The effective configuration, after every precedence layer
  lotsman version          Build, MCP SDK and protocol identity
  lotsman help             This message

Five minutes:
  lotsman inspect ./openapi.yaml                      # look before you serve
  lotsman serve ./openapi.yaml --base-url https://api.example.com
  claude mcp add my-api -- lotsman serve /abs/openapi.yaml --base-url https://api.example.com

That is a read-only agent against one origin, with no credentials. The rest is opt-in:
  --config FILE            auth profiles (secrets are env:/file: references, never values)
  --allow-mutations        let write/destructive/unknown operations execute

Large APIs: one tool per operation stops working above a catalog a model can hold (65 Kubernetes
operations weigh 2.2 MB of tool definitions). --mode=auto, the default, switches to five
meta-tools -- search, describe, then call -- when the measured catalog does not fit; the same
4.7 KB whether the API has 65 operations or 631. Pin --mode=tools to get the full list anyway.

Flags:
  --base-url URL           the origin you authorize; a URL from the document is not authorization
  --config FILE            serve, inspect, operations, explain-call: profiles and execution settings
  --lax                    serve the supported subset instead of refusing the whole document
  --read-only              state the default explicitly: only read operations execute
  --allow-mutations        serve, inspect, operations: permit non-read effects
  --allow-private-network  serve: allow an origin that resolves into a private range
  --transport stdio|http   serve: stdio (default) or the stateless Streamable HTTP profile
  --listen HOST:PORT       serve --transport=http: bind address (default 127.0.0.1:8080)
  --drain-timeout D        serve --transport=http: how long shutdown waits for calls in flight
  --watch                  serve --transport=http: republish when the document or anything it
                           references changes (also SIGHUP)
  --json                   inspect, version: machine-readable output
  --fail-on-rejected       inspect: exit 4 when any operation is rejected
  --supported|--rejected   operations: filter by translation support
  --args FILE              explain-call: JSON file of grouped arguments
  --spec SPEC              explain-call: the document the operation comes from
  --quiet                  validate: print nothing, the exit code is the answer
  --log-level LEVEL        debug|info|warn|error (default info, env LOTSMAN_LOG_LEVEL)

Exit codes: 0 ok, 1 runtime, 2 usage/config, 3 unusable document, 4 unsupported or not
permitted, 5 credential could not be resolved.
`

// Run executes one lotsman command line -- args without the program name --
// and returns the process exit code. ctx ends the command: cmd/lotsman
// cancels it on SIGINT and SIGTERM, which is how `serve` drains.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	case "inspect":
		return inspect(ctx, rest, stdout, stderr)
	case "validate":
		return validate(ctx, rest, stdout, stderr)
	case "explain-call":
		return explainCall(ctx, rest, stdout, stderr)
	case "config":
		return configCommand(args[1:], stdout, stderr)
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

func version(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "lotsman: version: unexpected argument %q\n", fs.Arg(0))
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
		return nil, errs.Errorf(errs.ClassUsage, "invalid --log-level %q: want debug|info|warn|error", level)
	}
	// Redaction wraps the outermost handler: whatever any package logs, and
	// however it got there, the bytes leaving the process pass through it
	// (FR-61).
	return slog.New(redact.NewHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl}))), nil
}
