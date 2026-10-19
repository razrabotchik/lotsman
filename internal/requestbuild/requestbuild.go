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
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// Options configures request construction.
type Options struct {
	// BaseURL, when set, overrides every operation's own servers (FR-30's
	// --base-url). It must be an absolute http(s) URL.
	BaseURL string
	// Files opens the local files a multipart or single-file body is made of
	// (feature 009). Nil is the ordinary case: without `execution.fileInputs`
	// no operation with such a body is published, so none can be built.
	Files *fileinput.Resolver
	// Boundary pins the multipart boundary. Tests set it to compare bodies
	// byte for byte; in a real call it is empty and the boundary is random.
	Boundary string
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

	base, err := BaseURL(op.Servers, opts)
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

	body, err := encodeBody(op.Body, args, opts)
	if err != nil {
		return nil, err
	}
	// From here on the body may own open file descriptors, so every exit has
	// to close it: a refused call must not leave one behind (ADR-0022).
	req, err := http.NewRequestWithContext(ctx, op.Method, reqURL.String(), body.reader)
	if err != nil {
		body.close()
		return nil, errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
	}
	if body.length >= 0 {
		// A streamed body is not one http.NewRequest can measure, and a
		// request without a Content-Length is sent chunked -- which the PHP
		// backends this feature exists for receive as an empty upload.
		req.ContentLength = body.length
	}
	if body.files != nil {
		// Closing the request body closes the files with it, so the call path
		// has one thing to close and not two.
		req.Body = &sessionBody{ReadCloser: req.Body, body: body}
	}
	contentType := body.contentType
	// Headers are applied after the body so that a spec-declared header
	// cannot reach Content-Type first; applyHeaders refuses that name anyway,
	// but order makes the guarantee independent of the check.
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := applyHeaders(req, op.Parameters, group(args, domain.GroupHeaders)); err != nil {
		CloseBody(req)
		return nil, err
	}
	return req, nil
}

// CloseBody releases whatever a built request still holds -- the open files of
// a multipart or single-file body -- for a call that will not be sent: an
// egress refusal, a pending approval, a rebuild for the one allowed retry.
// Sending the request closes the body itself, and Close is idempotent, so a
// caller may always call this.
func CloseBody(req *http.Request) {
	if req == nil || req.Body == nil {
		return
	}
	_ = req.Body.Close()
}

// sessionBody ties the open files to the request body: whoever closes the
// body -- the transport after sending it, or a caller that decided not to --
// closes the descriptors.
type sessionBody struct {
	io.ReadCloser
	body encodedBody
}

func (s *sessionBody) Close() error {
	err := s.ReadCloser.Close()
	s.body.close()
	return err
}

// Files reports the files a built request will send, in part order: what the
// approval prompt describes and the audit event records.
func Files(req *http.Request) []*fileinput.File {
	body, ok := req.Body.(*sessionBody)
	if !ok || body.body.files == nil {
		return nil
	}
	return body.body.files.Files()
}

// encodeBody serializes the body argument for the operation's single declared
// media type. An operation with no body never sends one, whatever the caller
// put in the arguments: the schema rejects an unknown group first, and this is
// the second line that keeps an invented body off the wire.
func encodeBody(spec *domain.BodySpec, args Arguments, opts Options) (encodedBody, error) {
	value, present := args[domain.GroupBody]
	if spec == nil {
		if present {
			return encodedBody{}, errs.Errorf(errs.ClassUsage,
				"requestbuild: this operation declares no request body")
		}
		return emptyBody(), nil
	}
	if !present {
		if spec.Required {
			return encodedBody{}, errs.Errorf(errs.ClassUsage, "requestbuild: a request body is required")
		}
		return emptyBody(), nil
	}
	if spec.CarriesFiles() {
		return fileBody(spec, value, opts)
	}
	// An explicit null is a body, and it is sent as one. Whether null is
	// allowed is the schema's question, answered before this runs: a body
	// schema that permits it was published that way, and treating the value
	// as absent would either refuse a required body the caller supplied or
	// silently send nothing for an optional one -- neither of which is the
	// request the caller described.

	encoded, err := json.Marshal(value)
	if err != nil {
		return encodedBody{}, errs.Errorf(errs.ClassUsage, "requestbuild: request body is not JSON-encodable: %w", err)
	}
	// length -1 leaves the measuring to http.NewRequest, which also gives a
	// *bytes.Reader body a GetBody -- exactly today's behaviour for JSON.
	return encodedBody{reader: bytes.NewReader(encoded), contentType: spec.MediaType, length: -1}, nil
}

// encodedBody is a serialized request body: what to read, what to call it,
// how long it is, and what to release afterwards.
type encodedBody struct {
	reader      io.Reader
	contentType string
	// length is the exact body length, or -1 when the reader is one
	// http.NewRequest measures itself.
	length int64
	// closer is the stream side of a piped body; files are the descriptors it
	// reads from. Both are nil for a JSON body.
	closer io.Closer
	files  *fileinput.Session
}

func emptyBody() encodedBody {
	return encodedBody{reader: http.NoBody, length: -1}
}

// close releases a body nobody will read. It is idempotent.
func (b *encodedBody) close() {
	if b.closer != nil {
		_ = b.closer.Close()
	}
	if b.files != nil {
		_ = b.files.Close()
	}
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

// BaseURL implements FR-30's v0 slice: an explicit --base-url always
// wins; otherwise the first of the operation's effective servers (already
// resolved operation->path->root by the openapi adapter) is used. Server
// variables are not substituted yet, so a server URL containing "{...}" is
// rejected rather than sent as a literal placeholder.
//
// It is exported so that anything naming where a call goes -- the approval
// prompt, the audit record -- asks the function Build asks, rather than
// re-deriving the answer and one day disagreeing with it.
func BaseURL(servers []string, opts Options) (string, error) {
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
		// Not "invalid base URL": the base was parsed before this, so what
		// failed is the join, and the part that usually causes it is the
		// document's path template. Saying "base URL" sends an operator to
		// check a setting that is fine. The path is not quoted, because it is
		// the untrusted half.
		return nil, errs.Errorf(errs.ClassSpecInvalid,
			"requestbuild: the path template does not form a valid URL against the base")
	}
	return u, nil
}
