package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// What happened, when no answer came back? Three endpoints that end a call
// short of an ordinary response, each in a different place, so that the
// record lotsman writes can be checked against what the API actually did
// (ADR-0019):
//
//   - POST /v1/charges          -- the charge is made and the answer is lost:
//                                  outcome_unknown, and the caller is warned
//   - GET  /v1/charges          -- the proof that it was made
//   - GET  /v1/shapes/truncated -- the status arrives and the body does not:
//                                  executed, with the status
//
// The third way, a request that never left, needs no endpoint: stop the
// fixture and call anything. That is `failed`.

// charges is the record of charges made, whether or not anyone heard back.
type charges struct {
	mu   sync.Mutex
	made []charge
}

type charge struct {
	ID     int       `json:"id"`
	Amount int       `json:"amount"`
	At     time.Time `json:"at"`
}

func (c *charges) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/charges", c.create)
	mux.HandleFunc("GET /v1/charges", c.list)
	mux.HandleFunc("GET /v1/shapes/truncated", truncated)
}

// create makes the charge and then hangs up. The order is the point: the API
// acted, and the client cannot know it.
func (c *charges) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Amount int `json:"amount"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Amount < 1 {
		writeError(w, http.StatusBadRequest, "body must be {\"amount\": <positive integer>}")
		return
	}
	c.mu.Lock()
	c.made = append(c.made, charge{ID: len(c.made) + 1, Amount: request.Amount, At: time.Now().UTC()})
	c.mu.Unlock()
	hangUp(w)
}

func (c *charges) list(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	made := append([]charge{}, c.made...)
	writeJSON(w, http.StatusOK, map[string]any{"items": made, "count": len(made)})
}

// truncated promises a body it does not send: the headers and the status
// arrive, then half the body, then the connection closes.
func truncated(w http.ResponseWriter, _ *http.Request) {
	const promised = `{"items":[{"id":1,"name":"Murka"},{"id":2,"name":"Bobik"}],"count":2}`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(promised)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(promised[:len(promised)/2]))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	hangUp(w)
}

// hangUp closes the connection under the handler without another byte. A
// writer that cannot hand its connection over -- a test recorder -- gets a
// 500 instead, so the fixture never pretends to have hung up when it did not.
func hangUp(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this connection cannot be dropped")
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "this connection cannot be dropped: "+err.Error())
		return
	}
	_ = conn.Close()
}

// ping is an operation named like lotsman's own ping tool. Its answer says
// which of the two answered.
func ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pong": true, "from": "the API, not lotsman's built-in"})
}
