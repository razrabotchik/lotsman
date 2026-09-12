package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// operations implements `lotsman operations SPEC [--rejected]` (T008):
// load, parse and print the enumerated operations as a table. This is the
// raw IR view; the aggregated capability report (`inspect`) lands in T021/T022.
func operations(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("operations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rejectedOnly := fs.Bool("rejected", false, "show only rejected operations")
	supportedOnly := fs.Bool("supported", false, "show only supported operations")
	allowMutations := fs.Bool("allow-mutations", false, "evaluate as if mutations were enabled at serve time")
	configPath := fs.String("config", "", "configuration file (auth profiles, execution settings)")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if spec == "" {
		fmt.Fprintf(stderr, "lotsman: operations requires SPEC\n\n%s", usage)
		return exitUsage
	}
	if *rejectedOnly && *supportedOnly {
		fmt.Fprintln(stderr, "lotsman: operations: --supported and --rejected are mutually exclusive")
		return exitUsage
	}

	// A one-shot CLI command has no long-running session to tune verbosity
	// for; only libopenapi's own error/warning logs and spec diagnostics
	// land here.
	logger := slog.New(redact.NewHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	doc, err := parseSpec(ctx, spec, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitError
	}

	// EXECUTABLE answers the operator's actual question -- would this run? --
	// so it accounts for policy as well as capability, under the same default
	// (read-only) the server uses.
	runtime, err := resolveConfig(*configPath, fs, *allowMutations, false, "")
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitUsage
	}
	policyConfig := policy.Config{AllowMutations: runtime.AllowMutations}
	profiles := auth.NewProfiles(runtime.AuthProfiles)

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tOPERATION ID\tEFFECT\tSUPPORT\tEXECUTABLE\tREASONS")
	for i := range doc.Operations {
		op := &doc.Operations[i]
		for _, warning := range op.Effect.Warnings {
			// A suspicious verb is a warning an operator must see, not a
			// silent demotion (spec 4.7).
			fmt.Fprintf(stderr, "lotsman: warning: %s\n", warning)
		}
		if *rejectedOnly && op.Support.Level != domain.SupportRejected {
			continue
		}
		if *supportedOnly && op.Support.Level != domain.SupportSupported {
			continue
		}
		reasons := append(append([]domain.ReasonCode(nil), op.Support.Reasons...), op.ExecutionBlockers...)
		verdict := policyConfig.Evaluate(op.Effect)
		if !verdict.Allowed {
			reasons = append(reasons, verdict.Reason)
		}
		credentials := auth.Select(op.Security, profiles)
		if !credentials.Bound() {
			reasons = append(reasons, credentials.Reason)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\t%s\n",
			op.Method, op.PathTemplate, orDash(op.SourceOperationID), op.Effect.Effect,
			op.Support.Level, op.Executable() && verdict.Allowed && credentials.Bound(), joinReasons(reasons))
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitError
	}
	return exitOK
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinReasons(reasons []domain.ReasonCode) string {
	if len(reasons) == 0 {
		return "-"
	}
	strs := make([]string, len(reasons))
	for i, r := range reasons {
		strs[i] = string(r)
	}
	return strings.Join(strs, ",")
}
