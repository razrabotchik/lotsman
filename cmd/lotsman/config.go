package main

import (
	"flag"
	"fmt"
	"io"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// configCommand is `lotsman config check|export` (docs/spec.md §4.11).
//
// Both are read-only and neither resolves a secret. `check` answers "would this
// file be accepted", which strict decoding makes a sharper question than it
// sounds: an unknown field is an error, so a typo in a setting that gates
// something open is caught here rather than discovered later.
//
// `export` answers the one an operator cannot work out for themselves: after
// defaults, the file, the environment and the flags (FR-62), which values are
// actually in force.
func configCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "lotsman: config needs a subcommand: check or export\n\n%s", usage)
		return exitUsage
	}
	switch args[0] {
	case "check":
		return configCheck(args[1:], stdout, stderr)
	case "export":
		return configExport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lotsman: config %q is not check or export\n\n%s", args[0], usage)
		return exitUsage
	}
}

// configCheck loads a configuration and says whether it is usable. The exit
// code is the answer.
func configCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("quiet", false, "print nothing; the exit code is the answer")
	path, err := configPathArgument(fs, args, stderr)
	if err != nil {
		return exitUsage
	}

	if _, err := config.Load(path); err != nil {
		if !*quiet {
			fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		}
		return exitCode(err)
	}
	if !*quiet {
		fmt.Fprintf(stdout, "%s is usable\n", path)
	}
	return exitOK
}

// configExport prints the effective configuration as a document that can be
// read back.
func configExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path, err := configPathArgument(fs, args, stderr)
	if err != nil {
		return exitUsage
	}

	// The same resolution `serve` performs, with no flag layer: the flags of
	// *this* command are not the flags of a server, and pretending otherwise
	// would export a configuration nobody is running.
	runtime, err := resolveConfig(path, fs, &flagValues{})
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}

	exported := runtime.Export()
	document, err := yaml.Marshal(&exported)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] config export: %v\n", errs.ClassInternal, err)
		return exitError
	}
	// The header says what this is and what it is not, because an exported
	// document is the kind of thing that gets pasted somewhere and read months
	// later.
	//
	// An omitted field means the deriving package's documented default rather
	// than "unset": the catalog owns its budgets and this package does not
	// duplicate them, so the export states what the configuration says and not
	// what every constant is. Reading the document back produces the same
	// runtime, which is the property that makes it worth printing.
	fmt.Fprintf(stdout, "# lotsman config export: the effective configuration of %s\n", path)
	fmt.Fprintf(stdout, "# Precedence is already applied: defaults < file < environment < flags.\n")
	fmt.Fprintf(stdout, "# An omitted field is at its documented default. No secret was resolved:\n")
	fmt.Fprintf(stdout, "# a reference is all this configuration ever holds.\n")
	_, _ = stdout.Write(document)
	return exitOK
}

// configPathArgument takes the one positional argument both subcommands need.
//
// It goes through parseWithTrailingSpec for the same reason every other command
// does: Go's flag package stops at the first non-flag argument, so
// `config check FILE --quiet` would otherwise read `--quiet` as a second file.
func configPathArgument(fs *flag.FlagSet, args []string, stderr io.Writer) (string, error) {
	path, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: %s: %v\n", fs.Name(), err)
		return "", err
	}
	if path == "" {
		fmt.Fprintf(stderr, "lotsman: %s needs a configuration file\n\n%s", fs.Name(), usage)
		return "", errs.Errorf(errs.ClassUsage, "%s: one file expected", fs.Name())
	}
	return path, nil
}
