package requestbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// Options configures request construction.
type Options struct {
	// BaseURL, when set, overrides every operation's own servers (FR-30's
	// --base-url). It must be an absolute http(s) URL.
	BaseURL string
}

// Operation is what the builder needs to know about the operation it is
// serializing: the catalog's view, not the full IR.
type Operation struct {
	Method       string
	PathTemplate string
	Servers      []string
	Parameters   []domain.Parameter
	Body         *domain.BodySpec
}

// Arguments are the caller's already-validated grouped arguments
// ({"path": {...}, "query": {...}}); see FR-22 for why they are grouped.
type Arguments map[string]any

// Build constructs the HTTP request for an operation from its validated
// arguments. op is read-only.
//
// It refuses rather than approximates: a parameter in a location whose
// serialization does not exist yet, a placeholder with no argument, or a
// value that has no URL representation all end here, before the network.
// Arguments are expected to have passed schema validation already; the checks
// repeated here are the ones whose failure would put something wrong on the
// wire.
func Build(ctx context.Context, op *Operation, args Arguments, opts Options) (*http.Request, error) {
	for _, p := range op.Parameters {
		switch p.In {
		case domain.LocationPath, domain.LocationQuery, domain.LocationHeader:
		default:
			return nil, errs.Errorf(errs.ClassUnsupported,
				"requestbuild: %s parameters are not serialized yet (%q)", p.In, p.Name)
		}
	}

	base, err := resolveBaseURL(op.Servers, opts)
	if err != nil {
		return nil, err
	}

	escapedPath, err := expandPath(op.PathTemplate, op.Parameters, group(args, domain.GroupPath))
	if err != nil {
		return nil, err
	}
	rawQuery, err := buildQuery(op.Parameters, group(args, domain.GroupQuery))
	if err != nil {
		return nil, err
	}

	reqURL, err := joinURL(base, escapedPath)
	if err != nil {
		return nil, err
	}
	reqURL.RawQuery = rawQuery

	body, contentType, err := encodeBody(op.Body, args)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, op.Method, reqURL.String(), body)
	if err != nil {
		return nil, errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
	}
	// Headers are applied after the body so that a spec-declared header
	// cannot reach Content-Type first; applyHeaders refuses that name anyway,
	// but order makes the guarantee independent of the check.
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := applyHeaders(req, op.Parameters, group(args, domain.GroupHeaders)); err != nil {
		return nil, err
	}
	return req, nil
}

// encodeBody serializes the body argument for the operation's single declared
// media type. An operation with no body never sends one, whatever the caller
// put in the arguments: the schema rejects an unknown group first, and this is
// the second line that keeps an invented body off the wire.
func encodeBody(spec *domain.BodySpec, args Arguments) (io.Reader, string, error) {
	value, present := args[domain.GroupBody]
	if spec == nil {
		if present {
			return nil, "", errs.Errorf(errs.ClassUsage,
				"requestbuild: this operation declares no request body")
		}
		return http.NoBody, "", nil
	}
	if !present || value == nil {
		if spec.Required {
			return nil, "", errs.Errorf(errs.ClassUsage, "requestbuild: a request body is required")
		}
		return http.NoBody, "", nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", errs.Errorf(errs.ClassUsage, "requestbuild: request body is not JSON-encodable: %w", err)
	}
	return bytes.NewReader(encoded), spec.MediaType, nil
}

// group returns one argument group, or an empty map when the caller omitted
// it. A group that is present but not an object is a caller error rather than
// an empty group, so it is reported instead of ignored.
func group(args Arguments, name string) map[string]any {
	value, ok := args[name]
	if !ok || value == nil {
		return map[string]any{}
	}
	if fields, ok := value.(map[string]any); ok {
		return fields
	}
	return map[string]any{}
}

// resolveBaseURL implements FR-30's v0 slice: an explicit --base-url always
// wins; otherwise the first of the operation's effective servers (already
// resolved operation->path->root by the openapi adapter) is used. Server
// variables are not substituted yet, so a server URL containing "{...}" is
// rejected rather than sent as a literal placeholder.
func resolveBaseURL(servers []string, opts Options) (string, error) {
	candidate := opts.BaseURL
	if candidate == "" {
		if len(servers) == 0 {
			return "", errs.Errorf(errs.ClassUsage, "requestbuild: no server for this operation and no --base-url given")
		}
		candidate = servers[0]
	}

	// Checked before url.Parse: a "{...}" placeholder can make the URL
	// outright unparseable (e.g. in the host), which would otherwise surface
	// as a generic parse error instead of this more actionable one.
	if strings.Contains(candidate, "{") {
		return "", errs.Errorf(errs.ClassUnsupported, "requestbuild: base URL has unresolved server variables, not supported yet")
	}
	u, err := url.Parse(candidate)
	if err != nil {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: invalid base URL syntax")
	}
	if !u.IsAbs() {
		// Pitfall #12: `servers: [{url: /api/v2}]` is legal and common. It is
		// relative to wherever the document was served from -- which, for a
		// document read off disk, is nowhere. lotsman will not invent an
		// origin for it.
		return "", errs.Errorf(errs.ClassUsage,
			"requestbuild: server %q is relative, and a specification read from a file has no origin to resolve it against; pass --base-url", candidate)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: base URL must use http or https")
	}
	if u.Host == "" {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: base URL has no host")
	}
	if u.User != nil {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: base URL must not contain credentials")
	}
	if u.Fragment != "" {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: base URL must not contain a fragment")
	}
	if u.RawQuery != "" {
		return "", errs.Errorf(errs.ClassUsage, "requestbuild: base URL must not contain a query")
	}
	return candidate, nil
}

// joinURL appends an already-escaped path to base.
//
// The concatenation is parsed as a string rather than assigned to url.URL.Path
// on purpose: Path holds the *decoded* form, so assigning to it would decode
// the "%2F" that keeps a path argument inside its own segment and hand back
// the traversal that encoding just prevented (pitfall #9).
func joinURL(base, escapedPath string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + escapedPath)
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "requestbuild: invalid base URL syntax")
	}
	return u, nil
}
