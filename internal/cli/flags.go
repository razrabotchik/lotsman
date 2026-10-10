package cli

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// pick is the flag when it was given, the file otherwise (FR-62). The flag's
// zero value is the empty string precisely so that "not given" is a state
// rather than a guess.
func pick(fromFlag, fromFile string) string {
	if fromFlag != "" {
		return fromFlag
	}
	return fromFile
}

// parseMode validates the requested catalog mode.
func parseMode(requested string) (catalog.Mode, error) {
	switch catalog.Mode(requested) {
	case "":
		// Nobody said anything, in the file or on the command line.
		return catalog.ModeAuto, nil
	case catalog.ModeTools, catalog.ModeSearch, catalog.ModeAuto:
		return catalog.Mode(requested), nil
	default:
		return "", errs.Errorf(errs.ClassUsage, "--mode %q is not tools, search or auto", requested)
	}
}

// resolveConfig applies the documented precedence: defaults < file <
// environment < flags (FR-62). A flag that was not given must not override the
// file with its zero value, which is why the overrides carry pointers.
func resolveConfig(path string, fs *flag.FlagSet, given *flagValues) (config.Runtime, error) {
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
		effective := given.allowMutations && !given.readOnly
		overrides.AllowMutations = &effective
	}
	if wasSet(fs, "base-url") {
		overrides.BaseURL = &given.baseURL
	}
	if wasSet(fs, "approval") {
		mode := config.Approval(given.approval)
		if !mode.Valid() {
			return config.Runtime{}, errs.Errorf(errs.ClassUsage,
				"--approval %q is not always, client-capability or never", given.approval)
		}
		overrides.Approval = &mode
	}
	if err := serverOverrides(fs, given, &overrides); err != nil {
		return config.Runtime{}, err
	}

	environment := config.Environment{
		BaseURL:  os.Getenv("LOTSMAN_BASE_URL"),
		LogLevel: os.Getenv("LOTSMAN_LOG_LEVEL"),
	}
	return config.Resolve(file, environment, overrides), nil
}

// flagValues is the command-line layer of FR-62's precedence, collected in one
// place so that adding a setting does not add a positional argument nobody can
// read at the call site.
type flagValues struct {
	allowMutations  bool
	readOnly        bool
	baseURL         string
	approval        string
	transport       string
	listen          string
	drainTimeout    string
	allowPublicBind bool
	logLevel        string
	mode            string
	lax             bool
}

// serverOverrides folds the transport flags into the override layer. Each is
// applied only when it was actually given, so a flag left alone cannot
// silently overwrite a configuration file that stated something.
func serverOverrides(fs *flag.FlagSet, given *flagValues, overrides *config.Overrides) error {
	if wasSet(fs, "transport") {
		transport := config.Transport(given.transport)
		if !transport.Valid() {
			return errs.Errorf(errs.ClassUsage, "--transport %q is not stdio or http", given.transport)
		}
		overrides.Transport = &transport
	}
	if wasSet(fs, "listen") {
		overrides.Listen = &given.listen
	}
	if wasSet(fs, "drain-timeout") {
		drain, err := time.ParseDuration(given.drainTimeout)
		if err != nil {
			return errs.Errorf(errs.ClassUsage,
				"--drain-timeout %q is not a duration (for example \"10s\")", given.drainTimeout)
		}
		if drain < 0 {
			return errs.Errorf(errs.ClassUsage, "--drain-timeout %q is negative", given.drainTimeout)
		}
		overrides.DrainTimeout = &drain
	}
	if wasSet(fs, "allow-unauthenticated-public-bind") {
		overrides.AllowUnauthenticatedPublicBind = &given.allowPublicBind
	}
	if wasSet(fs, "log-level") {
		overrides.LogLevel = &given.logLevel
	}
	if wasSet(fs, "mode") {
		overrides.Mode = &given.mode
	}
	if wasSet(fs, "lax") {
		overrides.Lax = &given.lax
	}
	return nil
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
