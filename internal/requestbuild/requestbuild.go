package requestbuild

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Options configures request construction.
type Options struct {
	// BaseURL, when set, overrides every operation's own servers (FR-30's
	// --base-url). It must be an absolute http(s) URL.
	BaseURL string
}

// Build constructs the HTTP request for a parameterless operation: no path,
// query, header, cookie or body parameters (T014-018 add those). It is an
// error to call it for an operation whose path template still has an
// unresolved "{...}" placeholder -- this pipeline stage has nothing to fill
// it with yet, and sending the literal "{petId}" to a server is not a
// request, it's a guess.
func Build(ctx context.Context, method, pathTemplate string, servers []string, opts Options) (*http.Request, error) {
	if strings.Contains(pathTemplate, "{") {
		return nil, fmt.Errorf("requestbuild: %s has unresolved path parameters, not supported yet", pathTemplate)
	}

	base, err := resolveBaseURL(servers, opts)
	if err != nil {
		return nil, err
	}

	reqURL, err := joinURL(base, pathTemplate)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("requestbuild: %w", err)
	}
	return req, nil
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
			return "", fmt.Errorf("requestbuild: no server for this operation and no --base-url given")
		}
		candidate = servers[0]
	}

	// Checked before url.Parse: a "{...}" placeholder can make the URL
	// outright unparseable (e.g. in the host), which would otherwise surface
	// as a generic parse error instead of this more actionable one.
	if strings.Contains(candidate, "{") {
		return "", fmt.Errorf("requestbuild: base URL %q has unresolved server variables, not supported yet", candidate)
	}
	u, err := url.Parse(candidate)
	if err != nil {
		return "", fmt.Errorf("requestbuild: invalid base URL %q: %w", candidate, err)
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("requestbuild: base URL %q is not absolute", candidate)
	}
	return candidate, nil
}

// joinURL appends pathTemplate to base's path. Both are trusted at this
// point: base was validated by resolveBaseURL and pathTemplate came from
// the parsed spec with no parameters substituted into it, so there is
// nothing here for an argument to inject.
func joinURL(base, pathTemplate string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("requestbuild: invalid base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + pathTemplate
	return u.String(), nil
}
