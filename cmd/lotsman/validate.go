package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// validate implements `lotsman validate SPEC` (T035).
//
// It answers one question with an exit code: would `serve` accept this
// document? `inspect` explains a specification; `validate` gates a pipeline,
// which is why its output is short and its exit codes are the contract:
// 3 for a document that cannot be used, 4 for one that is valid but has
// nothing lotsman would publish.
func validate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("quiet", false, "print nothing; the exit code is the answer")
	configPath := fs.String("config", "", "configuration file; its overlay is checked against the document too")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if spec == "" {
		fmt.Fprintf(stderr, "lotsman: validate requires SPEC\n\n%s", usage)
		return exitUsage
	}

	logger := slog.New(redact.NewHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	doc, err := parseSpec(ctx, spec, logger)
	if err != nil {
		if !*quiet {
			fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		}
		return exitCode(err)
	}

	// A configuration is part of the answer to "would serve accept this?":
	// an override that matches nothing fails the same way here as it does
	// there, and a tag filter can leave a valid document with nothing to
	// publish.
	overlay := catalog.Overlaid{Operations: doc.Operations}
	if *configPath != "" {
		runtime, err := resolveConfig(*configPath, fs, &flagValues{})
		if err == nil {
			var opts catalog.Options
			opts, err = catalogOptions(runtime, catalog.ModeTools, doc.Operations)
			if err == nil {
				overlay = catalog.Overlay(doc.Operations, opts)
			}
		}
		if err != nil {
			if !*quiet {
				fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
			}
			return exitCode(err)
		}
	}

	var documentErrors, rejected, supported, excluded int
	for _, diagnostic := range doc.Diagnostics {
		if diagnostic.Severity == domain.SeverityError {
			documentErrors++
		}
	}
	for i := range overlay.Operations {
		op := &overlay.Operations[i]
		switch op.Support.Level {
		case domain.SupportSupported:
			if _, gone := overlay.Excluded[op.Key]; gone {
				excluded++
				continue
			}
			supported++
		case domain.SupportRejected:
			rejected++
		}
	}

	switch {
	case documentErrors > 0:
		if !*quiet {
			fmt.Fprintf(stderr, "lotsman: %s is not usable: %d document-level error(s); run `lotsman inspect` for detail\n",
				spec, documentErrors)
		}
		return exitSpecInvalid
	case supported == 0:
		if !*quiet {
			fmt.Fprintf(stderr, "lotsman: %s has no operation lotsman can publish (%d rejected%s)\n",
				spec, rejected, excludedSuffix(excluded))
		}
		return exitUnsupported
	default:
		if !*quiet {
			fmt.Fprintf(stdout, "%s: %d operation(s) publishable, %d rejected%s\n",
				spec, supported, rejected, excludedSuffix(excluded))
		}
		return exitOK
	}
}

// excludedSuffix reports the operator's own exclusions separately, because
// "lotsman refused this" and "you excluded this" are different facts.
func excludedSuffix(excluded int) string {
	if excluded == 0 {
		return ""
	}
	return fmt.Sprintf(", %d excluded by configuration", excluded)
}
