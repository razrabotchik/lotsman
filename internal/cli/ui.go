package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/ui"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

// uiCommand implements `lotsman ui [SPEC] [--config FILE] [--root DIR]
// [--port N] [--no-open]` (feature 008, spec §4).
func uiCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "configuration file to edit (default ./lotsman.yaml, created on first save)")
	root := fs.String("root", "", "directory $ref resolution is confined to")
	port := fs.Int("port", 0, "loopback port (default: a free one)")
	noOpen := fs.Bool("no-open", false, "print the address instead of opening a browser")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	if spec == "-" {
		fmt.Fprintln(stderr, "lotsman: ui: the workbench re-reads the document on every change; pass a file or URL, not - (standard input)")
		return exitUsage
	}
	path := *configPath
	if path == "" {
		path = "lotsman.yaml"
	}
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) && spec == "" {
		fmt.Fprintf(stderr, "lotsman: ui: %s does not exist; pass SPEC to start a new configuration\n", path)
		return exitUsage
	}
	// A new configuration names the document relative to itself, so it means
	// the same thing from any directory.
	sourceForNew := spec
	if spec != "" {
		if abs, absErr := filepath.Abs(spec); absErr == nil {
			if rel, relErr := filepath.Rel(filepath.Dir(path), abs); relErr == nil {
				sourceForNew = rel
			}
		}
	}
	wb, err := workbench.New(ctx, path, sourceForNew, newPipeline(pipelineInputs{configPath: path, spec: spec, root: *root}))
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}
	srv, err := ui.Listen(ctx, wb, *port)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: ui: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "lotsman ui: editing %s\nopen %s\n(ctrl-c to stop)\n", path, srv.URL())
	if !*noOpen {
		cleanup := ui.Open(ctx, srv.URL())
		defer cleanup()
	}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "lotsman: ui: %v\n", err)
		return exitError
	}
	return exitOK
}
