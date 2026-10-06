package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// PUT and PATCH exist here for one reason: effect classification. lotsman reads
// an effect from the method unless the document says otherwise, so a fixture
// with only POST and DELETE can show two of the four answers.
//
//   - PUT    /v1/pets/{petId}  -- a write: replaces the pet
//   - PATCH  /v1/pets/{petId}  -- a write, declared with a merge-patch media
//                                 type, which is also how a media type other
//                                 than application/json is exercised
//   - POST   /v1/pets/{petId}/actions/rename -- an action whose effect the
//                                 method cannot reveal, so the document has to
//                                 say it (x-lotsman-effect) or lotsman treats
//                                 it as unknown and refuses it by default

func (s *store) registerWrites(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/pets/{petId}", s.replacePet)
	mux.HandleFunc("PATCH /v1/pets/{petId}", s.patchPet)
	mux.HandleFunc("POST /v1/pets/{petId}/actions/rename", s.renamePet)
}

func (s *store) replacePet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != apiKey {
		writeError(w, http.StatusUnauthorized, "valid X-API-Key required")
		return
	}
	id, err := pathInt(r, "petId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	s.mu.Lock()
	_, existed := s.pets[id]
	replaced := pet{ID: id, Name: input.Name, Tags: input.Tags}
	s.pets[id] = replaced
	if id >= s.nextID {
		s.nextID = id + 1
	}
	s.mu.Unlock()

	if existed {
		writeJSON(w, http.StatusOK, replaced)
		return
	}
	writeJSON(w, http.StatusCreated, replaced)
}

func (s *store) patchPet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != apiKey {
		writeError(w, http.StatusUnauthorized, "valid X-API-Key required")
		return
	}
	// The media type is part of what a patch means, and a fixture that accepted
	// anything would make the document's declaration untestable.
	if mediaType := contentType(r); mediaType != "application/merge-patch+json" {
		writeError(w, http.StatusUnsupportedMediaType,
			"this endpoint takes application/merge-patch+json, not "+quoted(mediaType))
		return
	}
	id, err := pathInt(r, "petId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A merge patch distinguishes "absent" from "null", so the fields are
	// pointers: absent leaves the value alone, null clears it.
	var input struct {
		Name *string   `json:"name"`
		Tags *[]string `json:"tags"`
	}
	if !decodeBody(w, r, &input) {
		return
	}

	s.mu.Lock()
	current, ok := s.pets[id]
	if ok {
		if input.Name != nil {
			current.Name = strings.TrimSpace(*input.Name)
		}
		if input.Tags != nil {
			current.Tags = *input.Tags
		}
		s.pets[id] = current
	}
	s.mu.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "pet not found")
		return
	}
	if current.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name must not be cleared")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

// renamePet is the operation whose effect its method does not reveal: a POST
// that is neither a creation nor obviously safe. The document declares it, and
// what lotsman does with an undeclared one is the interesting half.
func (s *store) renamePet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != apiKey {
		writeError(w, http.StatusUnauthorized, "valid X-API-Key required")
		return
	}
	id, err := pathInt(r, "petId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	s.mu.Lock()
	current, ok := s.pets[id]
	if ok {
		current.Name = input.Name
		s.pets[id] = current
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "pet not found")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

// decodeBody reads a bounded JSON body, answering the caller itself when it
// cannot. It reports whether decoding succeeded.
func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// contentType is the media type without its parameters.
func contentType(r *http.Request) string {
	value := r.Header.Get("Content-Type")
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = value[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(value))
}
