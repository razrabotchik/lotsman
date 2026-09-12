package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/redact"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
)

// explainCall implements `lotsman explain-call OPERATION --args FILE` (FR-31).
//
// It answers the question an operator asks before letting an agent near an
// API: *what exactly would this do?* Everything it prints is computed on the
// path a real call takes -- the same validator, the same serializer, the same
// policy and auth decisions -- and then it stops. Nothing is sent.
func explainCall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explain-call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	specPath := fs.String("spec", "", "the specification the operation comes from")
	argsPath := fs.String("args", "", "JSON file with the grouped arguments")
	configPath := fs.String("config", "", "configuration file (auth profiles, execution settings)")
	baseURL := fs.String("base-url", "", "override every tool's server (FR-30)")
	allowMutations := fs.Bool("allow-mutations", false, "explain as if mutations were enabled")
	// The operation comes first, and flag.Parse stops at the first
	// non-flag token -- the same trap that swallowed `operations SPEC
	// --rejected`. The positional is taken off the front before parsing.
	target, rest := splitLeadingArgument(args)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if target == "" || *specPath == "" || fs.NArg() != 0 {
		fmt.Fprintf(stderr, "lotsman: explain-call needs an operation and --spec\n\n%s", usage)
		return exitUsage
	}

	arguments, err := readArguments(*argsPath)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}

	runtime, err := resolveConfig(*configPath, fs, *allowMutations, false, *baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}

	logger := slog.New(redact.NewHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	doc, err := parseSpec(ctx, *specPath, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}

	cat := catalog.Build(doc.digest, doc.Operations, catalog.Options{
		Policy: policy.Config{AllowMutations: runtime.AllowMutations},
		Auth:   auth.NewProfiles(runtime.AuthProfiles),
	})
	tool := findTool(&cat, target)
	if tool == nil {
		fmt.Fprintf(stderr, "lotsman: no published operation matches %q (try `lotsman inspect %s`)\n", target, *specPath)
		return exitUnsupported
	}

	explain(stdout, tool, arguments, runtime)
	return exitOK
}

// splitLeadingArgument takes the positional argument off the front of the
// command line, if there is one.
func splitLeadingArgument(args []string) (leading string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// findTool accepts either the tool name a client sees or the operation key a
// report prints, because those are the two identifiers a reader has in hand.
func findTool(cat *catalog.Catalog, target string) *catalog.Tool {
	for i := range cat.Tools {
		if cat.Tools[i].Name == target || string(cat.Tools[i].OperationKey) == target {
			return &cat.Tools[i]
		}
	}
	return nil
}

func readArguments(path string) (requestbuild.Arguments, error) {
	if path == "" {
		return requestbuild.Arguments{}, nil
	}
	// #nosec G304 -- an operator-supplied CLI argument.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "explain-call: cannot read %s", path)
	}
	var arguments requestbuild.Arguments
	if err := json.Unmarshal(data, &arguments); err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "explain-call: %s is not a JSON object of argument groups", path)
	}
	return arguments, nil
}

// explain prints the decision trail. Every line is a fact lotsman would act
// on, in the order it would act on it.
func explain(w io.Writer, tool *catalog.Tool, arguments requestbuild.Arguments, runtime config.Runtime) {
	fmt.Fprintf(w, "operation   %s\n", tool.OperationKey)
	fmt.Fprintf(w, "tool        %s\n", tool.Name)
	fmt.Fprintf(w, "effect      %s (%s, %s)\n", tool.Effect.Effect, tool.Effect.Source, tool.Effect.Confidence)
	for _, warning := range tool.Effect.Warnings {
		fmt.Fprintf(w, "            warning: %s\n", warning)
	}

	fmt.Fprintf(w, "policy      %s\n", policyLine(tool, runtime))
	fmt.Fprintf(w, "request     %s\n", requestLine(tool, arguments, runtime))
	for _, line := range argumentLines(arguments) {
		fmt.Fprintf(w, "            %s\n", line)
	}
	fmt.Fprintf(w, "auth        %s\n", authLine(tool))
	fmt.Fprintf(w, "media       %s\n", mediaLine(tool))
	fmt.Fprintln(w, "NO network request was made.")
}

func policyLine(tool *catalog.Tool, runtime config.Runtime) string {
	if len(tool.PolicyBlockers) > 0 {
		return fmt.Sprintf("DENY (%s: %s)", joinReasons(tool.PolicyBlockers), tool.PolicyMessage)
	}
	if len(tool.ExecutionBlockers) > 0 {
		return fmt.Sprintf("DENY (%s)", joinReasons(tool.ExecutionBlockers))
	}
	mode := "read-only mode"
	if runtime.AllowMutations {
		mode = "mutations enabled"
	}
	return fmt.Sprintf("ALLOW (%s: %s permitted)", mode, tool.Effect.Effect)
}

// requestLine builds the request exactly as the runtime would, which is the
// only way the answer can be trusted: a re-implementation would be a
// description of a different call.
func requestLine(tool *catalog.Tool, arguments requestbuild.Arguments, runtime config.Runtime) string {
	op := requestbuild.Operation{
		Method:       tool.Method,
		PathTemplate: tool.PathTemplate,
		Servers:      tool.Servers,
		Parameters:   tool.Input.Parameters,
		Body:         tool.Input.Body,
	}
	req, err := requestbuild.Build(context.Background(), &op, arguments, requestbuild.Options{BaseURL: runtime.BaseURL})
	if err != nil {
		return fmt.Sprintf("%s %s — cannot be built: %v", tool.Method, tool.PathTemplate, redact.Error(err))
	}

	line := fmt.Sprintf("%s %s", req.Method, redact.String(req.URL.String()))
	if outbound, err := egress.PolicyFromBaseURL(runtime.BaseURL, egress.Budget{}, runtime.AllowPrivateNetworks); err == nil {
		outbound.AllowedOrigins = append(outbound.AllowedOrigins, runtime.AllowedOrigins...)
		if denied := outbound.CheckTarget(req.URL); denied != nil {
			line += "\n            egress: DENY — " + denied.Error()
		} else {
			line += "\n            egress: ALLOW"
		}
	}
	return line
}

func argumentLines(arguments requestbuild.Arguments) []string {
	var lines []string
	for _, group := range domain.GroupOrder {
		value, ok := arguments[group]
		if !ok {
			continue
		}
		fields, isObject := value.(map[string]any)
		if !isObject {
			lines = append(lines, fmt.Sprintf("[%s] %s", group, redact.String(fmt.Sprint(value))))
			continue
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		var parts []string
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s=%v", name, redact.String(fmt.Sprint(fields[name]))))
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", group, strings.Join(parts, "  ")))
	}
	return lines
}

// authLine names the profile and the reference behind it -- never a value.
// The reference is the useful part: it tells an operator which environment
// variable has to be set on the machine that will run this.
func authLine(tool *catalog.Tool) string {
	if len(tool.AuthBinding.Credentials) == 0 {
		return "-"
	}
	var parts []string
	for i := range tool.AuthBinding.Credentials {
		credential := &tool.AuthBinding.Credentials[i]
		parts = append(parts, fmt.Sprintf("profile %q → %s", credential.Name, describeCredential(credential)))
	}
	return strings.Join(parts, "; ")
}

func describeCredential(credential *auth.Credential) string {
	switch credential.Profile.Scheme {
	case config.SchemeBearer:
		return fmt.Sprintf("header Authorization: Bearer <%s>", credential.Profile.TokenRef)
	case config.SchemeBasic:
		return fmt.Sprintf("header Authorization: Basic <%s:%s>",
			credential.Profile.UsernameRef, credential.Profile.PasswordRef)
	case config.SchemeAPIKey:
		name := credential.Requirement.Name
		if name == "" {
			name = credential.Profile.Name
		}
		where := credential.Requirement.In
		if where == "" {
			where = string(credential.Profile.In)
		}
		return fmt.Sprintf("%s %s: <%s>", where, name, credential.Profile.TokenRef)
	default:
		return "unknown scheme"
	}
}

func mediaLine(tool *catalog.Tool) string {
	if tool.Input.Body == nil {
		return "-"
	}
	required := "optional"
	if tool.Input.Body.Required {
		required = "required"
	}
	return fmt.Sprintf("%s (%s)", tool.Input.Body.MediaType, required)
}
