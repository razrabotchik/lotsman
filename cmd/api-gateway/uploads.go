package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
)

// The upload endpoints: the body shapes lotsman sends as files (feature 009).
//
// Each answers with what it parsed -- the field values, and per file its name,
// its length, its declared part type and the sha256 of the bytes that arrived.
// A test asserting the upload was byte-exact needs the receiving end to say so,
// and "the request succeeded" is not that statement.

func registerUploads(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/forms/multipart", multipartForm)
	mux.HandleFunc("PUT /v1/uploads/raw", rawUpload)
}

// multipartForm takes a text field and a file, which is the multipart shape
// lotsman serializes.
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

	reported := map[string]any{
		"name":          r.FormValue("name"),
		"contentLength": r.ContentLength,
	}
	if headers := r.MultipartForm.File["attachment"]; len(headers) > 0 {
		header := headers[0]
		file, err := header.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "the attachment could not be read")
			return
		}
		defer func() { _ = file.Close() }()
		digest := sha256.New()
		written, err := io.Copy(digest, file)
		if err != nil {
			writeError(w, http.StatusBadRequest, "the attachment could not be read to the end")
			return
		}
		reported["attachment"] = map[string]any{
			"filename":    header.Filename,
			"bytes":       written,
			"contentType": header.Header.Get("Content-Type"),
			"sha256":      hex.EncodeToString(digest.Sum(nil)),
		}
	}
	writeJSON(w, http.StatusOK, reported)
}

// rawUpload takes a whole body of bytes: the application/octet-stream shape.
func rawUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	digest := sha256.New()
	written, err := io.Copy(digest, r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the body could not be read to the end")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bytes":         written,
		"contentLength": r.ContentLength,
		"contentType":   contentType(r),
		"sha256":        hex.EncodeToString(digest.Sum(nil)),
	})
}
