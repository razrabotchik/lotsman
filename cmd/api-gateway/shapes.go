package main

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Response shapes an upstream really produces, each one a decision lotsman has
// to make before a model sees anything:
//
//   - 204 and an empty body        -- "no content" must not read as "no answer"
//   - text/plain and octet-stream  -- a body that is not JSON must not be
//                                     presented as structured content
//   - Set-Cookie and Authorization -- never returned, whatever the upstream says
//   - rate-limit headers           -- returned, because a model can act on them
//   - problem+json                 -- an error body is worth showing verbatim
//   - gzip                         -- decoded before the byte cap applies, or
//                                     the cap would measure the wrong thing
//   - a dripping body              -- the idle phase of the time budget
//   - many headers / a long header -- bounds on what is copied out
//   - redirect loop, chain, and a
//     redirect to another origin   -- never followed, each for its own reason

func registerShapes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/shapes/no-content", noContent)
	mux.HandleFunc("GET /v1/shapes/text", plainText)
	mux.HandleFunc("GET /v1/shapes/binary", binaryBody)
	mux.HandleFunc("GET /v1/shapes/cookie", cookieAndCredentialHeaders)
	mux.HandleFunc("GET /v1/shapes/rate-limited", rateLimited)
	mux.HandleFunc("GET /v1/shapes/problem", problemJSON)
	mux.HandleFunc("GET /v1/shapes/gzip", gzipped)
	mux.HandleFunc("GET /v1/shapes/drip", drip)
	mux.HandleFunc("GET /v1/shapes/headers", manyHeaders)
	mux.HandleFunc("GET /v1/redirect/loop", redirectLoop)
	mux.HandleFunc("GET /v1/redirect/chain", redirectChain)
	mux.HandleFunc("GET /v1/redirect/external", redirectExternal)
}

func noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func plainText(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("not JSON, and saying so is the whole point\n"))
}

func binaryBody(w http.ResponseWriter, _ *http.Request) {
	// A short run of bytes that are not text, including a NUL and an invalid
	// UTF-8 sequence: the shape that makes "treat a body as a string" fail.
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// cookieAndCredentialHeaders answers with the two headers a runtime must never
// hand onwards, plus one it may.
func cookieAndCredentialHeaders(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Set-Cookie", "session=FIXTURE-SESSION-DO-NOT-FORWARD; Path=/; HttpOnly")
	w.Header().Set("Authorization", "Bearer FIXTURE-ECHOED-CREDENTIAL")
	w.Header().Set("X-Request-Id", "fixture-request-1")
	writeJSON(w, http.StatusOK, map[string]any{
		"note": "the response carries Set-Cookie and Authorization; neither may reach a model",
	})
}

// rateLimited carries the headers an API uses to say "slow down". They are on
// the allowlist on purpose: a model that can read them can act on them.
func rateLimited(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("RateLimit-Limit", "60")
	w.Header().Set("RateLimit-Remaining", "0")
	w.Header().Set("RateLimit-Reset", "30")
	w.Header().Set("Retry-After", "30")
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": map[string]any{"status": 429, "message": "too many requests; retry in 30s"},
	})
}

func problemJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write([]byte(`{"type":"https://example.com/probs/validation",` +
		`"title":"Validation failed","status":422,` +
		`"detail":"name must not be empty","instance":"/v1/pets"}` + "\n"))
}

// gzipped compresses its body, so the byte cap is measured against what the
// body actually is rather than against its compressed size.
func gzipped(w http.ResponseWriter, r *http.Request) {
	size, err := boundedInt(r.URL.Query().Get("bytes"), 64*1024, 1, 4*1024*1024)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(http.StatusOK)

	compressor := gzip.NewWriter(w)
	defer func() { _ = compressor.Close() }()
	// A JSON document whose single field is long, so the result is both valid
	// JSON and highly compressible.
	_, _ = compressor.Write([]byte(`{"filler":"`))
	_, _ = compressor.Write([]byte(strings.Repeat("y", size)))
	_, _ = compressor.Write([]byte(`"}`))
}

// drip sends the body a chunk at a time with a pause between chunks: the shape
// that separates "slow to start" from "slow throughout", which are different
// phases of the time budget.
func drip(w http.ResponseWriter, r *http.Request) {
	chunks, err := boundedInt(r.URL.Query().Get("chunks"), 5, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pause, err := boundedInt(r.URL.Query().Get("ms"), 200, 0, 10_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	for i := range chunks {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(pause) * time.Millisecond):
		}
		//nolint:gosec // G705: both values are bounded integers and the body is text/plain; nothing from the request is echoed.
		_, _ = fmt.Fprintf(w, "chunk %d of %d\n", i+1, chunks)
		if canFlush {
			flusher.Flush()
		}
	}
}

// manyHeaders answers with as many response headers as asked for, which is how
// a reader finds out whether anything bounds the header map that travels to a
// model.
func manyHeaders(w http.ResponseWriter, r *http.Request) {
	count, err := boundedInt(r.URL.Query().Get("count"), 50, 1, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	length, err := boundedInt(r.URL.Query().Get("length"), 64, 1, 8192)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for i := range count {
		// X-Request-Id is on lotsman's allowlist, so the repeated one is the
		// header a reader should expect to see arriving; the numbered ones are
		// not, and should not.
		w.Header().Set("X-Fixture-"+strconv.Itoa(i), strings.Repeat("h", length))
	}
	w.Header().Set("X-Request-Id", strings.Repeat("r", length))
	writeJSON(w, http.StatusOK, map[string]any{"headers": count, "valueLength": length})
}

// redirectLoop points at itself: a client that follows redirects without a hop
// limit never comes back, which is a failure mode worth being able to produce
// on purpose.
func redirectLoop(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/v1/redirect/loop", http.StatusFound)
}

// redirectChain counts down, so a hop limit can be observed rather than
// inferred.
func redirectChain(w http.ResponseWriter, r *http.Request) {
	hops, err := boundedInt(r.URL.Query().Get("hops"), 3, 0, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hops == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"arrived": true})
		return
	}
	http.Redirect(w, r, "/v1/redirect/chain?hops="+strconv.Itoa(hops-1), http.StatusFound)
}

// redirectExternal sends the caller to another origin. Following it would mean
// carrying a credential across an origin boundary, which is the reason the
// redirect guard exists rather than a nicety.
func redirectExternal(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
}
