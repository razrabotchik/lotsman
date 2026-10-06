package main

import (
	"net/http"
	"strings"
)

// Operations lotsman refuses on purpose, implemented anyway.
//
// The document declares each of these, so `lotsman inspect` prints the refusal
// matrix against a real API instead of against prose: a form-urlencoded body,
// a multipart body, deepObject and the delimited array styles, a cookie
// parameter, four competing patch media types, a body with conditional
// subschemas. Each one is a reason code a reader can see in context.
//
// The endpoints work. That is deliberate: when one of these becomes supported,
// the fixture does not have to be written -- it has to stop being refused, and
// the difference shows up as operations moving from rejected to executable.

func registerRefused(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/forms/urlencoded", urlencodedForm)
	mux.HandleFunc("POST /v1/forms/multipart", multipartForm)
	mux.HandleFunc("GET /v1/filters", filters)
	mux.HandleFunc("GET /v1/session", sessionCookie)
	mux.HandleFunc("PATCH /v1/pets/{petId}/competing", competingPatch)
	mux.HandleFunc("POST /v1/variants", variants)
	mux.HandleFunc("POST /v1/conditional", variants)
}

// urlencodedForm is the Stripe shape: every request body a form.
func urlencodedForm(w http.ResponseWriter, r *http.Request) {
	if mediaType := contentType(r); mediaType != "application/x-www-form-urlencoded" {
		writeError(w, http.StatusUnsupportedMediaType,
			"this endpoint takes application/x-www-form-urlencoded, not "+quoted(mediaType))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "form body expected")
		return
	}
	fields := map[string][]string{}
	for key, values := range r.PostForm {
		fields[key] = values
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

// multipartForm takes a field and a file, which is the other body shape a
// document can ask for and this runtime does not serialize.
func multipartForm(w http.ResponseWriter, r *http.Request) {
	// ParseMultipartForm bounds what it keeps in memory and not what it reads,
	// so the body is bounded first: the rest would spill to disk.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	//nolint:gosec // G120: the body is bounded by the MaxBytesReader above; this argument bounds only what is held in memory.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "multipart body expected")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	reported := map[string]any{"name": r.FormValue("name")}
	if headers := r.MultipartForm.File["attachment"]; len(headers) > 0 {
		reported["attachment"] = map[string]any{
			"filename": headers[0].Filename,
			"bytes":    headers[0].Size,
		}
	}
	writeJSON(w, http.StatusOK, reported)
}

// filters reads the query styles a URL slot cannot carry unambiguously:
// deepObject (`filter[name]=x`), spaceDelimited and pipeDelimited arrays.
func filters(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	deep := map[string]string{}
	for key, values := range query {
		if name, found := strings.CutPrefix(key, "filter["); found && strings.HasSuffix(name, "]") {
			deep[strings.TrimSuffix(name, "]")] = values[0]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"filter":         deep,
		"spaceDelimited": splitNonEmpty(query.Get("tags"), " "),
		"pipeDelimited":  splitNonEmpty(query.Get("ids"), "|"),
	})
}

// sessionCookie reads a cookie parameter: a location this runtime reports as
// not implemented rather than guessing at.
func sessionCookie(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err != nil {
		writeError(w, http.StatusBadRequest, "a session cookie is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionShown": fingerprint(cookie.Value),
	})
}

// competingPatch offers four patch media types at once, which is what
// Kubernetes does and why twelve of its operations are refused: choosing one
// of four without being told which is a guess.
func competingPatch(w http.ResponseWriter, r *http.Request) {
	accepted := map[string]bool{
		"application/json-patch+json":            true,
		"application/merge-patch+json":           true,
		"application/strategic-merge-patch+json": true,
		"application/apply-patch+yaml":           true,
	}
	mediaType := contentType(r)
	if !accepted[mediaType] {
		writeError(w, http.StatusUnsupportedMediaType,
			"this endpoint takes one of four patch media types, not "+quoted(mediaType))
		return
	}
	if _, err := pathInt(r, "petId"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patchedWith": mediaType})
}

// variants serves both the oneOf body (which lotsman supports) and the
// if/then/else one (which it refuses). One handler, because the difference that
// matters is in the document rather than in what the server does with it.
func variants(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if !decodeBody(w, r, &input) {
		return
	}
	kind := "unrecognised"
	switch {
	case input["sku"] != nil:
		kind = "product"
	case input["email"] != nil:
		kind = "person"
	}
	writeJSON(w, http.StatusOK, map[string]any{"recognisedAs": kind, "fields": len(input)})
}

func splitNonEmpty(value, separator string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, separator)
}
