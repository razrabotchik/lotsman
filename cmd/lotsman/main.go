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
	"github.com/razrabotchik/lotsman/internal/egress"
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
  lotsman serve SPEC       Serve an OpenAPI document as MCP tools over stdio
  lotsman inspect SPEC     What lotsman makes of a document, and why (--json for CI)
  lotsman validate SPEC    Is this document usable? The exit code is the answer
  lotsman operations SPEC  One line per operation: effect, support, executable
  lotsman explain-call OP  What a call would do — without making it
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
5.5 KB whether the API has 65 operations or 631. Pin --mode=tools to get the full list anyway.

Flags:
  --base-url URL           the origin you authorize; a URL from the document is not authorization
  --config FILE            serve, inspect, operations, explain-call: profiles and execution settings
  --lax                    serve the supported subset instead of refusing the whole document
  --read-only              state the default explicitly: only read operations execute
  --allow-mutations        serve, inspect, operations: permit non-read effects
  --allow-private-network  serve: allow an origin that resolves into a private range
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
	case "validate":
		return validate(ctx, rest, stdout, stderr)
	case "explain-call":
		return explainCall(ctx, rest, stdout, stderr)
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
	allowPrivate := fs.Bool("allow-private-network", false,
		"permit an allowed origin whose hostname resolves into a private or link-local range")
	mode := fs.String("mode", string(catalog.ModeAuto), "catalog mode: tools|search|auto")
	approval := fs.String("approval", "",
		"ask before a mutating call: always|client-capability|never (default always, FR-44)")
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

	runtime, err := resolveConfig(*configPath, fs, *allowMutations, *readOnly, *baseURL, *approval)
	runtime.AllowPrivateNetworks = runtime.AllowPrivateNetworks || *allowPrivate
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}
	catalogMode, err := parseMode(*mode)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}
	egressPolicy, err := egress.PolicyFromBaseURL(runtime.BaseURL, egress.Budget{}, runtime.AllowPrivateNetworks)
	egressPolicy.AllowedOrigins = append(egressPolicy.AllowedOrigins, runtime.AllowedOrigins...)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}

	opts := mcpserver.Options{
		Logger: logger, BaseURL: runtime.BaseURL, Egress: egressPolicy,
		Approval: runtime.InteractiveApproval,
	}
	if spec != "" {
		cat, err := loadCatalog(ctx, spec, logger, *lax, runtime, catalogMode)
		if err != nil {
			logger.Error("load spec failed", "class", string(errs.ClassOf(err)), "error", err)
			return exitCode(err)
		}
		reportMode(logger, cat)
		opts.Catalog = cat
	}

	if err := mcpserver.ServeStdio(ctx, &opts); err != nil {
		logger.Error("serve failed", "class", string(errs.ClassOf(err)), "error", err)
		return exitCode(err)
	}
	return exitOK
}

// loadCatalog runs pipeline stages 0-4 (specsource, openapi, catalog) for
// `serve SPEC`.
func loadCatalog(ctx context.Context, spec string, logger *slog.Logger, lax bool, runtime config.Runtime, mode catalog.Mode) (*catalog.Catalog, error) {
	doc, err := parseSpec(ctx, spec, logger)
	if err != nil {
		return nil, err
	}
	opts, err := catalogOptions(runtime, mode, doc.Operations)
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

// parseMode validates the requested catalog mode.
func parseMode(requested string) (catalog.Mode, error) {
	switch catalog.Mode(requested) {
	case catalog.ModeTools, catalog.ModeSearch, catalog.ModeAuto:
		return catalog.Mode(requested), nil
	default:
		return "", errs.Errorf(errs.ClassUsage, "--mode %q is not tools, search or auto", requested)
	}
}

// reportMode says out loud which mode is in force and why. A catalog served as
// tools while the measurement asks for search is a choice an operator may make
// deliberately -- but not one lotsman should make quietly.
func reportMode(logger *slog.Logger, cat *catalog.Catalog) {
	estimate := cat.Report.Estimate
	attrs := []any{
		"mode", string(cat.Mode),
		"tools", estimate.ToolCount,
		"serialized_bytes", estimate.SerializedBytes,
		"budget_bytes", estimate.ThresholdBytes,
	}
	if !estimate.OverBudget {
		logger.Info("catalog mode selected", attrs...)
		return
	}
	if cat.Mode == catalog.ModeSearch {
		logger.Info("catalog exceeds the context budget; serving search mode", attrs...)
		return
	}
	logger.Warn("catalog exceeds the context budget and tools mode was requested anyway; "+
		"a model may not fit this many tool definitions", attrs...)
}

// resolveConfig applies the documented precedence: defaults < file <
// environment < flags (FR-62). A flag that was not given must not override the
// file with its zero value, which is why the overrides carry pointers.
func resolveConfig(path string, fs *flag.FlagSet, allowMutations, readOnly bool, baseURL, approval string) (config.Runtime, error) {
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
	if wasSet(fs, "approval") {
		mode := config.Approval(approval)
		if !mode.Valid() {
			return config.Runtime{}, errs.Errorf(errs.ClassUsage,
				"--approval %q is not always, client-capability or never", approval)
		}
		overrides.Approval = &mode
	}

	environment := config.Environment{BaseURL: os.Getenv("LOTSMAN_BASE_URL")}
	return config.Resolve(file, environment, overrides), nil
}

// catalogOptions turns the resolved configuration into catalog options,
// checking the operator's overlay against the document first.
//
// The check is separate from Build and fatal on purpose: an override that
// matches nothing is not a smaller catalog, it is a control the operator
// believes is in force. `lotsman validate` and `serve` should fail on the
// same typo.
func catalogOptions(runtime config.Runtime, mode catalog.Mode, operations []domain.Operation) (catalog.Options, error) {
	if err := catalog.ValidateOverrides(operations, runtime.Overrides); err != nil {
		return catalog.Options{}, err
	}
	return catalog.Options{
		Mode:        mode,
		Policy:      policyConfig(runtime),
		Auth:        auth.NewProfiles(runtime.AuthProfiles),
		IncludeTags: runtime.IncludeTags,
		Overrides:   runtime.Overrides,
	}, nil
}

// policyConfig is the execution policy the runtime configuration describes.
func policyConfig(runtime config.Runtime) policy.Config {
	return policy.Config{
		AllowMutations: runtime.AllowMutations,
		Allow:          policyRules(runtime.AllowRules),
		Deny:           policyRules(runtime.DenyRules),
	}
}

// policyRules translates the configured rules into the gate's own vocabulary.
// The gate does not read configuration: it is called from three places, and a
// policy that could only be built one way would be a policy with one caller
// and three interpretations.
func policyRules(rules []config.Rule) []policy.Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]policy.Rule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, policy.Rule{
			Namespace:    rule.Namespace,
			OperationKey: domain.OperationKey(rule.OperationKey),
			Tag:          rule.Tag,
			Effect:       domain.Effect(rule.Effect),
		})
	}
	return out
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
