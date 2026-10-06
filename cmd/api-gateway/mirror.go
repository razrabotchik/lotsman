package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// The mirror answers the question every other endpoint leaves implicit: what
// did lotsman actually put on the wire?
//
// Serialization is the part of the pipeline with the most ways to be subtly
// wrong -- percent-encoding of reserved characters in a path slot, explode and
// style for arrays, repeated query keys, header joining, which headers a
// document is not allowed to set. Reading a report tells you what lotsman
// decided; reading this tells you what arrived.
//
// It reflects the request and nothing else. Every value is JSON-encoded on the
// way out, so a value carrying a quote or a newline is visible as data rather
// than becoming structure.

// mirrorReflection is what the mirror reports.
type mirrorReflection struct {
	Method string `json:"method"`
	// Path is the decoded path; RawPath is what was on the wire, which is the
	// one that answers "was this encoded or did it become a new segment".
	Path    string `json:"path"`
	RawPath string `json:"rawPath"`
	// Segments is the path split on "/", so a value that smuggled a separator
	// shows up as an extra entry rather than as a longer string.
	Segments []string `json:"segments"`
	RawQuery string   `json:"rawQuery"`
	// Query keeps repeats, because a repeated key is how an exploded array is
	// spelled and collapsing them would hide the difference.
	Query   map[string][]string `json:"query"`
	Headers map[string][]string `json:"headers"`
	Cookies map[string]string   `json:"cookies,omitempty"`
	// Body is the request body as text when it is valid UTF-8, and its length
	// otherwise. ContentType is reported separately because a body means
	// nothing without it.
	ContentType string `json:"contentType,omitempty"`
	Body        string `json:"body,omitempty"`
	BodyBytes   int    `json:"bodyBytes"`
	// BodyJSON is the decoded body when it is JSON, so a reader can see the
	// structure lotsman sent rather than re-reading an escaped string.
	BodyJSON any `json:"bodyJSON,omitempty"`
}

// hiddenHeaders are the ones a fixture should not reflect. The point of the
// mirror is to show serialization, not to print a credential into a terminal
// and a scrollback buffer -- lotsman has its own canary tests for the
// credential path, and this endpoint would only make them harder to trust.
var hiddenHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"x-api-key":           true,
	"proxy-authorization": true,
}

func registerMirror(mux *http.ServeMux) {
	for _, pattern := range []string{
		"GET /v1/mirror",
		"POST /v1/mirror",
		"PUT /v1/mirror",
		"PATCH /v1/mirror",
		"DELETE /v1/mirror",
		"GET /v1/mirror/{segment}",
		"POST /v1/mirror/{segment}",
	} {
		mux.HandleFunc(pattern, mirror)
	}
}

func mirror(w http.ResponseWriter, r *http.Request) {
	// Bounded: this is a fixture, and a body it cannot hold is a body it should
	// refuse rather than buffer.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body is larger than 256 KiB")
		return
	}

	reflection := mirrorReflection{
		Method:      r.Method,
		Path:        r.URL.Path,
		RawPath:     r.URL.EscapedPath(),
		Segments:    strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/"),
		RawQuery:    r.URL.RawQuery,
		Query:       map[string][]string{},
		Headers:     map[string][]string{},
		ContentType: r.Header.Get("Content-Type"),
		BodyBytes:   len(body),
	}
	for key, values := range r.URL.Query() {
		reflection.Query[key] = values
	}
	for name, values := range r.Header {
		if hiddenHeaders[strings.ToLower(name)] {
			reflection.Headers[name] = []string{"[present, not shown]"}
			continue
		}
		reflection.Headers[name] = values
	}
	if cookies := r.Cookies(); len(cookies) > 0 {
		reflection.Cookies = map[string]string{}
		for _, cookie := range cookies {
			// Names only: a cookie is where a credential travels, the same
			// reason lotsman's own approval prompt names them without values.
			reflection.Cookies[cookie.Name] = "[present, not shown]"
		}
	}
	if len(body) > 0 {
		reflection.Body = string(body)
		var decoded any
		if json.Unmarshal(body, &decoded) == nil {
			reflection.BodyJSON = decoded
		}
	}
	writeJSON(w, http.StatusOK, reflection)
}
