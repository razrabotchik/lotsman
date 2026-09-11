package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/openapi"
	"github.com/razrabotchik/lotsman/internal/specsource"
)

// operations implements `lotsman operations SPEC [--rejected]` (T008):
// load, parse and print the enumerated operations as a table. This is the
// raw IR view; the aggregated capability report (`inspect`) lands in T021/T022.
func operations(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("operations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rejectedOnly := fs.Bool("rejected", false, "show only rejected operations")
	// flag.Parse stops at the first non-flag argument, but the documented
	// usage (quickstart.md) puts SPEC before --rejected. Split flags from
	// the positional SPEC first so either order works.
	flagArgs, posArgs := splitFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}

	var spec string
	if len(posArgs) > 0 {
		spec = posArgs[0]
	}
	if spec == "" {
		fmt.Fprintf(stderr, "lotsman: operations requires SPEC\n\n%s", usage)
		return exitUsage
	}

	// A one-shot CLI command has no long-running session to tune verbosity
	// for; only libopenapi's own error/warning logs land here.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	src, err := specsource.Load(ctx, spec, specsource.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitError
	}

	doc, err := openapi.Parse(src.Bytes, "", logger)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %v\n", err)
		return exitError
	}
	for _, d := range doc.Diagnostics {
		fmt.Fprintf(stderr, "lotsman: %s: %s\n", d.Severity, d.Message)
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tOPERATION ID\tSUPPORT\tREASONS")
	for _, op := range doc.Operations {
		if *rejectedOnly && op.Support.Level != domain.SupportRejected {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			op.Method, op.PathTemplate, orDash(op.SourceOperationID), op.Support.Level, joinReasons(op.Support.Reasons))
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
