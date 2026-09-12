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

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/openapi"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/redact"
	"github.com/razrabotchik/lotsman/internal/specsource"
)

// Exit codes. The error class of every failure is already carried by
// internal/errs and logged; T034 maps those classes onto the distinct codes
// in contracts/cli.md (FR-77). Until that contract lands, only these three
// are used.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
	// exitUnsupported is the documented code for "valid but unsupported under
	// the selected policy". `inspect --fail-on-rejected` is the first user;
	// T034 maps the remaining error classes onto the rest of the table.
	exitUnsupported = 4
)

const usage = `lotsman — security-first OpenAPI → MCP runtime.

Usage:
  lotsman serve [SPEC]     Serve MCP over stdio (stdout is protocol-only)
  lotsman inspect SPEC     Capability report (add --json for CI)
  lotsman operations SPEC  List parsed operations as a table
  lotsman version          Print build, MCP SDK and protocol identity
  lotsman help             Print this message

Flags:
  --log-level LEVEL      debug|info|warn|error (default info, env LOTSMAN_LOG_LEVEL)
  --json                 version, inspect: machine-readable output
  --fail-on-rejected     inspect: exit 4 when any operation is rejected
  --supported|--rejected operations: filter by translation support
  --allow-mutations      operations: evaluate as if mutations were enabled
  --base-url URL         serve: override every tool's server (FR-30)
  --lax                  serve supported subset; strict mode is the default
  --read-only            serve: only read operations execute (the default)
  --allow-mutations      serve: let write/destructive/unknown operations execute
  --config FILE          serve, inspect, operations: auth profiles and execution settings
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
	case "inspect":
		return inspect(ctx, rest, stdout, stderr)
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
	baseURL := fs.String("base-url", "", "override every tool's server (FR-30)")
	lax := fs.Bool("lax", false, "serve the supported subset when individual operations are rejected")
	allowMutations := fs.Bool("allow-mutations", false, "allow non-read operations to execute (FR-41)")
	readOnly := fs.Bool("read-only", false, "state the default explicitly: only read operations execute")
	configPath := fs.String("config", "", "configuration file (auth profiles, execution settings)")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if *allowMutations && *readOnly {
		fmt.Fprintln(stderr, "lotsman: serve: --read-only and --allow-mutations contradict each other")
		return exitUsage
	}

	logger, err := newLogger(stderr, *level)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitUsage
	}

	runtime, err := resolveConfig(*configPath, fs, *allowMutations, *readOnly, *baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}
	policyConfig := policy.Config{AllowMutations: runtime.AllowMutations}
	profiles := auth.NewProfiles(runtime.AuthProfiles)

	opts := mcpserver.Options{Logger: logger, BaseURL: runtime.BaseURL}
	if spec != "" {
		cat, err := loadCatalog(ctx, spec, logger, *lax, catalog.Options{Policy: policyConfig, Auth: profiles})
		if err != nil {
			logger.Error("load spec failed", "class", string(errs.ClassOf(err)), "error", err)
			return exitError
		}
		opts.Catalog = cat
	}

	if err := mcpserver.ServeStdio(ctx, opts); err != nil {
		logger.Error("serve failed", "error", err)
		return exitError
	}
	return exitOK
}

// loadCatalog runs pipeline stages 0-4 (specsource, openapi, catalog) for
// `serve SPEC`.
func loadCatalog(ctx context.Context, spec string, logger *slog.Logger, lax bool, opts catalog.Options) (*catalog.Catalog, error) {
	doc, err := parseSpec(ctx, spec, logger)
	if err != nil {
		return nil, err
	}
	if doc.HasErrors() {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "invalid spec: document contains error diagnostics")
	}
	if !lax {
		for i := range doc.Operations {
			if op := &doc.Operations[i]; op.Support.Level != domain.SupportSupported {
				return nil, errs.Errorf(errs.ClassUnsupported, "unsupported operation %s: %s (use --lax to serve the supported subset)", op.Key, joinReasons(op.Support.Reasons))
			}
		}
	}
	cat := catalog.Build(doc.digest, doc.Operations, opts)
	if len(cat.Tools) == 0 {
		return nil, errs.Errorf(errs.ClassUnsupported, "spec contains zero supported operations")
	}
	return &cat, nil
}

// maxLoggedDiagnostics bounds how many spec diagnostics reach the log.
const maxLoggedDiagnostics = 10

// resolveConfig applies the documented precedence: defaults < file <
// environment < flags (FR-62). A flag that was not given must not override the
// file with its zero value, which is why the overrides carry pointers.
func resolveConfig(path string, fs *flag.FlagSet, allowMutations, readOnly bool, baseURL string) (config.Runtime, error) {
	var file *config.File
	if path != "" {
		loaded, err := config.Load(path)
		if err != nil {
			return config.Runtime{}, err
		}
		file = loaded
	}

	overrides := config.Overrides{}
	if wasSet(fs, "allow-mutations") || wasSet(fs, "read-only") {
		effective := allowMutations && !readOnly
		overrides.AllowMutations = &effective
	}
	if wasSet(fs, "base-url") {
		overrides.BaseURL = &baseURL
	}

	environment := config.Environment{BaseURL: os.Getenv("LOTSMAN_BASE_URL")}
	return config.Resolve(file, environment, overrides), nil
}

// wasSet reports whether a flag was given on the command line, which is what
// separates "set to false" from "not mentioned".
func wasSet(fs *flag.FlagSet, name string) bool {
	var set bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// specDoc pairs the parsed IR with the digest of the bytes it came from,
// since openapi.Document itself carries no provenance.
type specDoc struct {
	*openapi.Document
	digest string
}

// parseSpec runs pipeline stages 0-1 (specsource, openapi): shared by
// `serve SPEC`, which reduces the result to a Catalog, and `operations
// SPEC`, which prints every operation including the rejected ones a
// Catalog would silently drop.
func parseSpec(ctx context.Context, spec string, logger *slog.Logger) (*specDoc, error) {
	src, err := specsource.Load(ctx, spec, specsource.Options{})
	if err != nil {
		return nil, err
	}
	// RootPath travels with the bytes: the $ref stage must know which
	// directory the document is confined to, and stdin (empty root) is
	// confined to nothing at all.
	doc, err := openapi.Parse(ctx, src.Bytes, openapi.Options{Logger: logger, RootPath: src.RootPath})
	if err != nil {
		return nil, err
	}
	// One exploded specification can produce hundreds of identical-looking
	// diagnostics. The log carries the first few with their provenance; the
	// full list is what `inspect` is for.
	for i, d := range doc.Diagnostics {
		if i == maxLoggedDiagnostics {
			logger.LogAttrs(ctx, slog.LevelError, "more spec diagnostics suppressed",
				slog.Int("remaining", len(doc.Diagnostics)-i),
				slog.String("hint", "run `lotsman inspect SPEC --json` for the full list"))
			break
		}
		logger.LogAttrs(ctx, diagnosticLevel(d.Severity), "spec diagnostic",
			slog.String("severity", string(d.Severity)),
			slog.String("code", string(d.Code)),
			slog.String("pointer", d.Pointer),
			slog.String("message", d.Message))
	}
	return &specDoc{Document: doc, digest: src.Digest}, nil
}

// diagnosticLevel maps a domain.Diagnostic's severity to the matching slog
// level so filtering by --log-level behaves as an operator expects.
func diagnosticLevel(s domain.Severity) slog.Level {
	switch s {
	case domain.SeverityWarning:
		return slog.LevelWarn
	case domain.SeverityInfo:
		return slog.LevelInfo
	default:
		return slog.LevelError
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseWithTrailingSpec parses args against fs and returns the single
// positional SPEC argument, if any, tolerating flags on either side of it.
//
// flag.FlagSet.Parse alone stops at the first non-flag token, so a flag
// after SPEC -- the exact order quickstart.md documents ("operations
// SPEC --rejected") -- would otherwise land in fs.Args() unparsed instead
// of being applied. Parsing runs twice: once up to SPEC, once past it.
func parseWithTrailingSpec(fs *flag.FlagSet, args []string) (spec string, err error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil
	}
	if err := fs.Parse(rest[1:]); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return rest[0], nil
}
