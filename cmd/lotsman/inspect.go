package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// maxListedIssues bounds the human report's issue list.
const maxListedIssues = 10

// inspectDocument is the `inspect --json` contract (contracts/cli.md,
// schemaVersion 1). It is spelled out here, in the order the contract
// documents, because this shape is a promise to CI consumers: fields may be
// added within a version, never repurposed or removed.
type inspectDocument struct {
	SchemaVersion  string                     `json:"schemaVersion"`
	Lotsman        lotsmanIdentity            `json:"lotsman"`
	Spec           specIdentity               `json:"spec"`
	Totals         catalog.Totals             `json:"totals"`
	ByReason       map[domain.ReasonCode]int  `json:"byReason,omitempty"`
	Catalog        catalog.Estimate           `json:"catalog"`
	Security       catalog.SecuritySummary    `json:"security"`
	DocumentIssues []documentIssue            `json:"documentIssues,omitempty"`
	Operations     []catalog.OperationVerdict `json:"operations"`
}

type lotsmanIdentity struct {
	Version     string   `json:"version"`
	MCPProtocol []string `json:"mcpProtocol"`
}

type specIdentity struct {
	Source  string `json:"source"`
	Digest  string `json:"digest"`
	OpenAPI string `json:"openapi,omitempty"`
}

// documentIssue is a diagnostic that belongs to the document rather than to
// any one operation. `serve` refuses to start on these; `inspect` reports
// them, because explaining a document lotsman will not serve is precisely
// what it is for.
type documentIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Pointer  string `json:"pointer,omitempty"`
	Message  string `json:"message"`
}

// inspect implements `lotsman inspect SPEC [--json] [--allow-mutations]
// [--fail-on-rejected]` (T022, FR-10/12/12a).
//
// It exits 0 even when operations are rejected: the report is the product,
// not a test that passed. `--fail-on-rejected` is the CI helper for teams who
// want a red build instead.
func inspect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable report (schemaVersion 1)")
	allowMutations := fs.Bool("allow-mutations", false, "report as if mutations were enabled at serve time")
	configPath := fs.String("config", "", "configuration file (auth profiles, execution settings)")
	failOnRejected := fs.Bool("fail-on-rejected", false, "exit non-zero when any operation is rejected (CI helper)")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if spec == "" {
		fmt.Fprintf(stderr, "lotsman: inspect requires SPEC\n\n%s", usage)
		return exitUsage
	}

	logger := slog.New(redact.NewHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	doc, err := parseSpec(ctx, spec, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}

	runtime, err := resolveConfig(*configPath, fs, *allowMutations, false, "")
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}
	cat := catalog.Build(doc.digest, doc.Operations, catalog.Options{
		Policy: policy.Config{AllowMutations: runtime.AllowMutations},
		Auth:   auth.NewProfiles(runtime.AuthProfiles),
	})

	info := buildinfo.Get()
	report := inspectDocument{
		SchemaVersion:  cat.Report.SchemaVersion,
		Lotsman:        lotsmanIdentity{Version: info.Version, MCPProtocol: []string{info.MCPProtocolVersion}},
		Spec:           specIdentity{Source: spec, Digest: doc.digest, OpenAPI: doc.Version},
		Totals:         cat.Report.Totals,
		ByReason:       cat.Report.ByReason,
		Catalog:        cat.Report.Estimate,
		Security:       cat.Report.Security,
		DocumentIssues: documentIssues(doc.Diagnostics),
		Operations:     cat.Report.Operations,
	}

	if *asJSON {
		if err := newJSONEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintf(stderr, "lotsman: %v\n", err)
			return exitError
		}
	} else {
		writeHumanReport(stdout, &report)
	}

	if *failOnRejected && report.Totals.Rejected > 0 {
		fmt.Fprintf(stderr, "lotsman: %d operation(s) rejected\n", report.Totals.Rejected)
		return exitUnsupported
	}
	return exitOK
}

func documentIssues(diagnostics []domain.Diagnostic) []documentIssue {
	var out []documentIssue
	for _, d := range diagnostics {
		out = append(out, documentIssue{
			Severity: string(d.Severity),
			Code:     string(d.Code),
			Pointer:  d.Pointer,
			Message:  d.Message,
		})
	}
	return out
}

// writeHumanReport prints the report for a person: the numbers first, then
// what to do about them. Reasons are sorted by count so the biggest source of
// lost operations is the first line a reader sees (FR-12a).
func writeHumanReport(w io.Writer, report *inspectDocument) {
	fmt.Fprintf(w, "spec:     %s\n", report.Spec.Source)
	fmt.Fprintf(w, "digest:   %s\n", report.Spec.Digest)
	if report.Spec.OpenAPI != "" {
		fmt.Fprintf(w, "openapi:  %s\n", report.Spec.OpenAPI)
	}
	fmt.Fprintf(w, "lotsman:  %s (MCP %s)\n\n", report.Lotsman.Version, strings.Join(report.Lotsman.MCPProtocol, ", "))

	t := report.Totals
	fmt.Fprintf(w, "operations: %d found, %d supported, %d partial, %d rejected\n",
		t.Found, t.Supported, t.Partial, t.Rejected)
	fmt.Fprintf(w, "tools:      %d published, %d executable, %d blocked by policy, %d not implemented yet\n",
		t.Published, t.Executable, t.PolicyBlocked, t.CapabilityBlocked)
	fmt.Fprintf(w, "effects:    %s\n", formatEffects(t.ByEffect))
	fmt.Fprintf(w, "catalog:    mode=%s, %d tools, ~%d bytes, %s\n",
		report.Catalog.Mode, report.Catalog.ToolCount, report.Catalog.SerializedBytes, report.Catalog.Digest)
	fmt.Fprintf(w, "security:   remote refs %s, redirects %s, unknown mutations %s\n\n",
		enabledWord(report.Security.RemoteRefs), enabledWord(report.Security.Redirects),
		blockedWord(report.Security.UnknownMutationsBlocked))

	if len(report.DocumentIssues) > 0 {
		fmt.Fprintf(w, "document issues (these stop `serve` in every mode): %d\n", len(report.DocumentIssues))
		// A document whose every reference is unresolvable produces one issue
		// per reference; a reader needs the first few and the count, not all
		// of them. The --json form carries the complete list.
		for i, issue := range report.DocumentIssues {
			if i == maxListedIssues {
				fmt.Fprintf(w, "  ... and %d more (use --json for the full list)\n", len(report.DocumentIssues)-i)
				break
			}
			fmt.Fprintf(w, "  %-7s %s\n", issue.Severity, issue.Message)
		}
		fmt.Fprintln(w)
	}

	if len(report.ByReason) > 0 {
		fmt.Fprintln(w, "reasons:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, reason := range sortedReasons(report.ByReason) {
			fmt.Fprintf(tw, "  %s\t%d\n", reason.code, reason.count)
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	warnings := effectWarnings(report.Operations)
	if len(warnings) > 0 {
		fmt.Fprintln(w, "effect warnings:")
		for _, warning := range warnings {
			fmt.Fprintf(w, "  %s\n", warning)
		}
		fmt.Fprintln(w)
	}

	if t.Rejected > 0 {
		fmt.Fprintln(w, "rejected operations:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for i := range report.Operations {
			op := &report.Operations[i]
			if op.Support != domain.SupportRejected {
				continue
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", op.Method, op.Path, formatReasons(op.Reasons))
		}
		_ = tw.Flush()
	}
}

type reasonCount struct {
	code  domain.ReasonCode
	count int
}

// sortedReasons orders by count descending, then by code, so the output is
// both useful and deterministic.
func sortedReasons(byReason map[domain.ReasonCode]int) []reasonCount {
	out := make([]reasonCount, 0, len(byReason))
	for code, count := range byReason {
		out = append(out, reasonCount{code: code, count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].code < out[j].code
	})
	return out
}

func formatEffects(byEffect map[domain.Effect]int) string {
	if len(byEffect) == 0 {
		return "none"
	}
	var parts []string
	for _, effect := range []domain.Effect{
		domain.EffectRead, domain.EffectWrite, domain.EffectDestructive, domain.EffectUnknown,
	} {
		if count := byEffect[effect]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, effect))
		}
	}
	return strings.Join(parts, ", ")
}

func effectWarnings(operations []catalog.OperationVerdict) []string {
	var out []string
	for i := range operations {
		out = append(out, operations[i].Effect.Warnings...)
	}
	return out
}

func formatReasons(reasons []catalog.Reason) string {
	if len(reasons) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason.Detail != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", reason.Code, reason.Detail))
			continue
		}
		parts = append(parts, string(reason.Code))
	}
	return strings.Join(parts, "; ")
}

func enabledWord(enabled bool) string {
	if enabled {
		return "ENABLED"
	}
	return "off"
}

func blockedWord(blocked bool) string {
	if blocked {
		return "blocked"
	}
	return "ALLOWED"
}
