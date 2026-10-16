package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/httpserver"
	"github.com/razrabotchik/lotsman/internal/inbound"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/openapi"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/reload"
	"github.com/razrabotchik/lotsman/internal/specsource"
)

func serve(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	level := fs.String("log-level", "", "debug|info|warn|error (default info, env LOTSMAN_LOG_LEVEL)")
	baseURL := fs.String("base-url", "", "override every tool's server (FR-30)")
	lax := fs.Bool("lax", false, "serve the supported subset when individual operations are rejected")
	allowMutations := fs.Bool("allow-mutations", false, "allow non-read operations to execute (FR-41)")
	readOnly := fs.Bool("read-only", false, "state the default explicitly: only read operations execute")
	configPath := fs.String("config", "", "configuration file (auth profiles, execution settings)")
	allowPrivate := fs.Bool("allow-private-network", false,
		"permit an allowed origin whose hostname resolves into a private or link-local range")
	mode := fs.String("mode", "", "catalog mode: tools|search|auto (default auto)")
	approval := fs.String("approval", "",
		"ask before a mutating call: always|client-capability|never (default always, FR-44)")
	transport := fs.String("transport", "",
		"stdio|http (default stdio; http serves the stateless Streamable HTTP profile, FR-69)")
	listen := fs.String("listen", "", "bind address for --transport=http (default "+config.DefaultListen+", FR-70)")
	drainTimeout := fs.String("drain-timeout", "",
		"how long shutdown waits for calls in flight (default 10s, FR-75)")
	publicBind := fs.Bool("allow-unauthenticated-public-bind", false,
		"permit a non-loopback bind with nothing authenticating the endpoint (FR-70)")
	watch := fs.Bool("watch", false,
		"serve --transport=http: rebuild and republish the catalog when the document changes (FR-72)")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if *allowMutations && *readOnly {
		fmt.Fprintln(stderr, "lotsman: serve: --read-only and --allow-mutations contradict each other")
		return exitUsage
	}

	// The configuration is resolved before the logger exists, because the
	// file may be where the log level was stated (docs/spec.md 5.1). Errors
	// until then go to stderr directly, which is where they went anyway.
	runtime, err := resolveConfig(*configPath, fs, &flagValues{
		allowMutations:  *allowMutations,
		readOnly:        *readOnly,
		baseURL:         *baseURL,
		approval:        *approval,
		transport:       *transport,
		listen:          *listen,
		drainTimeout:    *drainTimeout,
		allowPublicBind: *publicBind,
		logLevel:        *level,
		mode:            *mode,
		lax:             *lax,
	})
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}
	runtime.AllowPrivateNetworks = runtime.AllowPrivateNetworks || *allowPrivate

	logger, err := newLogger(stderr, runtime.Server.LogLevel)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitUsage
	}

	// The configuration may name the document, and the command line wins
	// (FR-62). One file describing a deployment is the point of the field.
	spec = pick(spec, runtime.Source)

	// An HTTP endpoint with no catalog is a socket that answers tools/list
	// with nothing, which is indistinguishable from a broken deployment. Over
	// stdio the same state is a developer convenience with one user.
	if runtime.Server.Transport == config.TransportHTTP && spec == "" {
		fmt.Fprintln(stderr, "lotsman: serve --transport=http needs a specification to serve")
		return exitUsage
	}
	if *watch && runtime.Server.Transport != config.TransportHTTP {
		// Reload replaces the server a transport looks up per request, and a
		// stdio session resolves one for its whole life. Saying so is better
		// than accepting a flag that would quietly do nothing.
		fmt.Fprintln(stderr, "lotsman: --watch applies to --transport=http; "+
			"a stdio session serves one client from one process")
		return exitUsage
	}
	catalogMode, err := parseMode(runtime.Mode)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}
	egressPolicy, err := egress.PolicyFromBaseURL(runtime.BaseURL,
		egress.BudgetFor(runtime.Timeout), runtime.AllowPrivateNetworks)
	egressPolicy.AllowedOrigins = append(egressPolicy.AllowedOrigins, runtime.AllowedOrigins...)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}

	watched := &watchSet{}
	opts := mcpserver.Options{
		Logger: logger, BaseURL: runtime.BaseURL, Egress: egressPolicy,
		Approval:         runtime.InteractiveApproval,
		MaxResponseBytes: runtime.MaxResponseBytes,
		// One limit for the process, shared by every server a reload builds.
		Calls: mcpserver.NewCallLimit(runtime.MaxConcurrentCalls),
		// Through the logger, so an event inherits redaction and the stdout
		// ban instead of re-deriving both (FR-61, FR-68).
		Audit: audit.Log(logger),
	}
	if spec != "" {
		cat, err := loadCatalog(ctx, spec, logger, runtime, catalogMode, watched)
		if err != nil {
			logger.Error("load spec failed", "class", string(errs.ClassOf(err)), "error", err)
			return exitCode(err)
		}
		reportMode(logger, cat)
		opts.Catalog = cat
	}

	source := func(ctx context.Context) (*catalog.Catalog, error) {
		return loadCatalog(ctx, spec, logger, runtime, catalogMode, watched)
	}
	if err := runTransport(ctx, runtime, &opts, source, reloadTriggers(spec, *watch, watched)); err != nil {
		logger.Error("serve failed", "class", string(errs.ClassOf(err)), "error", err)
		return exitCode(err)
	}
	return exitOK
}

// runTransport serves the catalog over the configured transport.
//
// Both branches are handed the same `mcpserver.Options`, and the HTTP one is
// handed the server those options build. There is deliberately nowhere to
// write a behaviour that applies to one transport and not the other: a
// refusal that holds over stdio and not over HTTP would be a bug in the
// server, not a property of the protocol.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func runTransport(ctx context.Context, runtime config.Runtime, opts *mcpserver.Options,
	source reload.Source, triggers reload.Triggers) error {
	if runtime.Server.Transport != config.TransportHTTP {
		return mcpserver.ServeStdio(ctx, opts)
	}
	// The guard is built before the socket exists, so a `tokenRef` pointing
	// at an unset variable is a startup failure rather than an endpoint that
	// refuses every caller while looking healthy.
	guard, err := inbound.New(&runtime.Server.InboundAuth, opts.Logger, nil)
	if err != nil {
		return err
	}
	// Counters exist only here: §7.4 puts metrics in the HTTP profile, and a
	// stdio process has nobody to scrape it. They are fed from the same
	// events the audit log gets, so a metric and a log line cannot disagree
	// about how many calls were refused.
	counters := audit.NewCounters()
	audited := *opts
	audited.Audit = audit.Multi{opts.Audit, counters}

	// Every published catalog goes through the same builder the startup one
	// did, with the same options: a reload that could produce a differently
	// configured server would be a second way to decide policy.
	holder := reload.New(audited.Catalog, source, func(cat *catalog.Catalog) *mcp.Server {
		published := audited
		published.Catalog = cat
		return mcpserver.New(&published)
	}, audited.Logger)
	reload.Start(ctx, holder, triggers)

	metrics := counters.Handler(func() audit.Snapshot {
		current := holder.Catalog()
		snapshot := audit.Snapshot{Tools: len(current.Tools), Digest: current.Digest}
		if opts.Calls != nil {
			snapshot.CallsInFlight, snapshot.CallLimit = opts.Calls.InFlight(), opts.Calls.Max()
		}
		return snapshot
	})

	return httpserver.Serve(ctx, &httpserver.Options{
		Server:                         holder.Server,
		Metrics:                        metrics,
		Logger:                         opts.Logger,
		Listen:                         runtime.Server.Listen,
		AllowedOrigins:                 runtime.Server.AllowedOrigins,
		AllowedHosts:                   runtime.Server.AllowedHosts,
		DrainTimeout:                   runtime.Server.DrainTimeout,
		Guard:                          guard,
		TrustedProxies:                 runtime.Server.InboundAuth.TrustedProxies,
		AllowUnauthenticatedPublicBind: runtime.Server.AllowUnauthenticatedPublicBind,
	})
}

// reloadTriggers is what may republish the catalog.
//
// SIGHUP is always listened for: it costs nothing, has no surface, and is
// what a sidecar or a config-map reloader already sends. Watching is opt-in
// because it is a poll, and it needs a file to poll -- a document read from
// stdin was consumed at startup and there is nothing left to watch.
func reloadTriggers(spec string, watch bool, watched *watchSet) reload.Triggers {
	triggers := reload.Triggers{Signal: true}
	if watch && spec != "" && spec != "-" {
		triggers.Watch = spec
		// And every document the specification read. For an exploded spec the
		// root is an index nobody edits, so watching it alone noticed nothing an
		// operator actually does.
		triggers.Documents = watched.documents
	}
	return triggers
}

// watchSet remembers which documents the last successful load actually read, so
// `--watch` polls the specification rather than only its index (FR-13b).
//
// It is written by the loader and read by the watcher's ticker, hence the
// atomic: the two are on different goroutines by construction, and a reload
// replaces the whole set at once rather than editing it in place.
type watchSet struct{ paths atomic.Pointer[[]string] }

// record stores the absolute path of every document a load read.
func (w *watchSet) record(root string, references []domain.RefDocument) {
	paths := make([]string, 0, len(references))
	for i := range references {
		if root == "" {
			continue // stdin: nothing on disk to watch
		}
		paths = append(paths, filepath.Join(root, filepath.FromSlash(references[i].Path)))
	}
	w.paths.Store(&paths)
}

// documents is what the watcher asks for on each interval.
func (w *watchSet) documents() []string {
	if current := w.paths.Load(); current != nil {
		return *current
	}
	return nil
}

// loadCatalog runs pipeline stages 0-4 (specsource, openapi, catalog) for
// `serve SPEC`.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func loadCatalog(ctx context.Context, spec string, logger *slog.Logger, runtime config.Runtime, mode catalog.Mode, watched *watchSet) (*catalog.Catalog, error) {
	doc, err := parseSpec(ctx, spec, runtime, logger)
	if err != nil {
		return nil, err
	}
	if watched != nil {
		// Recorded before the checks below: a document that referenced others
		// and then failed validation is still the one an operator is editing,
		// and the next edit is what should be noticed.
		watched.record(doc.rootPath, doc.References)
	}
	opts, err := catalogOptions(runtime, mode, doc.Operations)
	if err != nil {
		return nil, err
	}
	if doc.HasErrors() {
		return nil, errs.Errorf(errs.ClassSpecInvalid, "invalid spec: document contains error diagnostics")
	}
	if !runtime.Lax {
		for i := range doc.Operations {
			if op := &doc.Operations[i]; op.Support.Level != domain.SupportSupported {
				return nil, errs.Errorf(errs.ClassUnsupported, "unsupported operation %s: %s (use --lax to serve the supported subset)", op.Key, joinReasons(op.Support.Reasons))
			}
		}
	}
	cat := catalog.Build(doc.digest, doc.Operations, opts)
	if err := checkCorrectedSchemas(&cat); err != nil {
		return nil, err
	}
	if len(cat.Tools) == 0 {
		return nil, errs.Errorf(errs.ClassUnsupported, "spec contains zero supported operations")
	}
	return &cat, nil
}

// maxLoggedDiagnostics bounds how many spec diagnostics reach the log.
const maxLoggedDiagnostics = 10

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

// catalogOptions turns the resolved configuration into catalog options,
// checking the operator's overlay against the document first.
//
// The check is separate from Build and fatal on purpose: an override that
// matches nothing is not a smaller catalog, it is a control the operator
// believes is in force. `lotsman validate` and `serve` should fail on the
// same typo.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func catalogOptions(runtime config.Runtime, mode catalog.Mode, operations []domain.Operation) (catalog.Options, error) {
	if err := catalog.ValidateOverrides(operations, runtime.Overrides); err != nil {
		return catalog.Options{}, err
	}
	return catalog.Options{
		Mode:   mode,
		Policy: policyConfig(runtime),
		Auth:   auth.NewProfiles(runtime.AuthProfiles),
		// Zero means the catalog package's documented default. The number is
		// decided there and read here; two places with an opinion about a
		// budget is how they come to disagree.
		MaxSerializedBytes:      runtime.MaxSerializedBytes,
		DescriptionBytesPerTool: runtime.DescriptionBytesPerTool,
		IncludeTags:             runtime.IncludeTags,
		Overrides:               runtime.Overrides,
	}, nil
}

// policyConfig is the execution policy the runtime configuration describes.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
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

// specDoc pairs the parsed IR with the digest of the bytes it came from,
// since openapi.Document itself carries no provenance.
type specDoc struct {
	*openapi.Document
	digest string
	// rootPath is the directory references were confined to, so a caller that
	// watches the specification knows where its documents live.
	rootPath string
	// overlays are the OpenAPI Overlay documents applied before parsing, with
	// their digests: they change what was parsed as surely as an edit would.
	overlays []domain.RefDocument
}

// manifestDigest identifies the root document and every document it read
// (FR-13b). For a single-file specification it still differs from the root
// digest, and that is deliberate: the two answer different questions, and one
// value that sometimes means "the root" and sometimes "everything" would be
// worse than two that always mean what they say.
func (d *specDoc) manifestDigest() string {
	return domain.ManifestDigest(d.digest, d.References)
}

// parseSpec runs pipeline stages 0-1 (specsource, openapi): shared by
// `serve SPEC`, which reduces the result to a Catalog, and `operations
// SPEC`, which prints every operation including the rejected ones a
// Catalog would silently drop.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func parseSpec(ctx context.Context, spec string, runtime config.Runtime, logger *slog.Logger) (*specDoc, error) {
	src, err := specsource.Load(ctx, spec, specsource.Options{})
	if err != nil {
		return nil, err
	}
	// RootPath travels with the bytes: the $ref stage must know which
	// directory the document is confined to, and stdin (empty root) is
	// confined to nothing at all.
	//
	// A configured root replaces the document's own directory. That widens
	// what the document may read, which is why only the operator can say it
	// -- the same shape as `--base-url` for egress. Inside the stated root a
	// reference is still confined; outside it is still refused.
	rootPath := src.RootPath
	if runtime.Root != "" {
		rootPath = runtime.Root
	}
	specBytes := src.Bytes
	var overlays []domain.RefDocument
	if len(runtime.Overlays) > 0 {
		docs, manifest, readErr := readOverlays(runtime.Overlays)
		if readErr != nil {
			return nil, readErr
		}
		if specBytes, err = openapi.ApplyOverlays(specBytes, docs); err != nil {
			return nil, err
		}
		overlays = manifest
	}
	doc, err := openapi.Parse(ctx, specBytes, openapi.Options{
		Logger: logger, RootPath: rootPath, Corrections: correctionsFrom(runtime),
	})
	if err != nil {
		return nil, err
	}
	if err := correctionError(doc.CorrectionProblems); err != nil {
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
	return &specDoc{Document: doc, digest: src.Digest, rootPath: rootPath, overlays: overlays}, nil
}

// readOverlays reads the configured overlay files and their digests. The
// digest is reported beside the document's, because an overlay changes what
// was parsed as surely as an edit to the document would.
func readOverlays(paths []string) ([]openapi.OverlayDocument, []domain.RefDocument, error) {
	docs := make([]openapi.OverlayDocument, 0, len(paths))
	manifest := make([]domain.RefDocument, 0, len(paths))
	for _, path := range paths {
		// #nosec G304 -- operator-named in the configuration.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays: cannot read %s", path)
		}
		sum := sha256.Sum256(data)
		docs = append(docs, openapi.OverlayDocument{Path: path, Bytes: data})
		manifest = append(manifest, domain.RefDocument{
			Path: filepath.Base(path), Digest: "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	return docs, manifest, nil
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
