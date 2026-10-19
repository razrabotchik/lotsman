package cli

import (
	"strings"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// File inputs (feature 009) are decided here, in the CLI, because this is the
// one layer that knows both the configuration and the transport. Everything
// below gets an answer: the catalog records why an upload cannot run, and the
// server either holds a resolver or holds nothing.

// fileInputsAllowed reports whether a local file may be read for a request
// body, and why not when it may not.
//
// Over stdio the client and the files belong to the same person. Over HTTP
// they do not: a remote client naming a path would be reading this machine's
// disk, which needs a second, explicit permission (ADR-0022 decision 4).
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func fileInputsAllowed(runtime config.Runtime) catalog.FileInputs {
	files := runtime.FileInputs
	if !files.Enabled {
		return catalog.FileInputs{
			Reason: domain.ReasonFileInputsDisabled,
			Message: "this operation sends a local file; set execution.fileInputs.enabled with " +
				"roots and allowedContentTypes to allow it",
		}
	}
	if runtime.Server.Transport == config.TransportHTTP && !files.AllowHTTPTransport {
		return catalog.FileInputs{
			Reason: domain.ReasonFileInputsTransportNotAllowed,
			Message: "this operation sends a local file and the transport is http, where the client " +
				"is not the owner of this machine's disk; set " +
				"execution.fileInputs.allowHTTPTransport to allow it anyway",
		}
	}
	return catalog.FileInputs{Available: true}
}

// fileResolver builds the sandbox the configuration describes, or nil when
// file inputs are not available. A root that cannot be resolved is a startup
// error: an operator who named a directory that is not there has not granted
// what they think they granted.
//
//nolint:gocritic // hugeParam: see fileInputsAllowed.
func fileResolver(runtime config.Runtime) (*fileinput.Resolver, error) {
	if !fileInputsAllowed(runtime).Available {
		return nil, nil
	}
	files := runtime.FileInputs
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               files.Roots,
		MaxFileBytes:        files.MaxFileBytes,
		MaxTotalBytes:       files.MaxTotalBytes,
		AllowedContentTypes: files.AllowedContentTypes,
	})
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "config: execution.fileInputs: %w", err)
	}
	return resolver, nil
}

// fileInputsWarning is what `config check` and a starting server say about a
// permission wide enough to be worth repeating out loud.
//
//nolint:gocritic // hugeParam: see fileInputsAllowed.
func fileInputsWarning(runtime config.Runtime) string {
	files := runtime.FileInputs
	if !files.Enabled || !files.AllowHTTPTransport {
		return ""
	}
	return "execution.fileInputs.allowHTTPTransport is set: a client reached over HTTP can name " +
		"any file under " + strings.Join(files.Roots, ", ") + " and have it sent to the API"
}
