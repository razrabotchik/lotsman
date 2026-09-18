// Command api-gateway runs a deliberately small, deterministic HTTP API used
// to exercise Lotsman's OpenAPI-to-MCP pipeline locally.
//
// It is a development fixture, not a production reverse proxy or gateway.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultAddr = "127.0.0.1:18080"
	apiKey      = "demo-secret"
	bearerToken = "demo-token"
)

//go:embed openapi.yaml
var openAPIDocument []byte

type pet struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

type store struct {
	mu     sync.RWMutex
	nextID int
	pets   map[int]pet
}

func newStore() *store {
	return &store{
		nextID: 3,
		pets: map[int]pet{
			1: {ID: 1, Name: "Murka", Tags: []string{"cat", "calm"}},
			2: {ID: 2, Name: "Bobik", Tags: []string{"dog", "friendly"}},
		},
	}
}

func main() {
	os.Exit(run())
}

// run holds the body of main so that deferred cleanup (signal handling,
// shutdown) actually runs: os.Exit skips defers, so only main may call it.
func run() int {
	addr := flag.String("addr", defaultAddr, "listen address")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "api-gateway: unexpected argument %q\n", flag.Arg(0))
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	server := &http.Server{
		Addr:              *addr,
		Handler:           newHandler(logger),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "error", err)
		}
	}()

	logger.Info("development API listening",
		"address", "http://"+*addr,
		"openapi", "http://"+*addr+"/openapi.yaml")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		return 1
	}
	return 0
}

func newHandler(logger *slog.Logger) http.Handler {
	s := newStore()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)
	mux.HandleFunc("GET /v1/pets", s.listPets)
	mux.HandleFunc("POST /v1/pets", s.createPet)
	mux.HandleFunc("GET /v1/pets/{petId}", s.getPet)
	mux.HandleFunc("DELETE /v1/pets/{petId}", s.deletePet)
	mux.HandleFunc("GET /v1/secure/profile", secureProfile)
	mux.HandleFunc("GET /v1/echo", echo)
	mux.HandleFunc("GET /v1/status/{code}", status)
	mux.HandleFunc("GET /v1/redirect", redirect)
	mux.HandleFunc("GET /v1/large", large)
	mux.HandleFunc("GET /v1/slow", slow)
	return requestLogger(logger, mux)
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "lotsman-test-api",
	})
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPIDocument)
}

func (s *store) listPets(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedInt(r.URL.Query().Get("limit"), 20, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tag := r.URL.Query().Get("tag")
	s.mu.RLock()
	items := make([]pet, 0, len(s.pets))
	for id := 1; len(items) < limit && id < s.nextID; id++ {
		p, ok := s.pets[id]
		if !ok || (tag != "" && !contains(p.Tags, tag)) {
			continue
		}
		items = append(items, p)
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *store) getPet(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "petId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.RLock()
	p, ok := s.pets[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "pet not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *store) createPet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != apiKey {
		writeError(w, http.StatusUnauthorized, "valid X-API-Key required")
		return
	}
	var input struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	s.mu.Lock()
	p := pet{ID: s.nextID, Name: input.Name, Tags: input.Tags}
	s.pets[p.ID] = p
	s.nextID++
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, p)
}

func (s *store) deletePet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != apiKey {
		writeError(w, http.StatusUnauthorized, "valid X-API-Key required")
		return
	}
	id, err := pathInt(r, "petId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	_, ok := s.pets[id]
	delete(s.pets, id)
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "pet not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func secureProfile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+bearerToken {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "valid bearer token required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"subject": "demo-user", "role": "tester"})
}

func echo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"query":     r.URL.Query().Get("query"),
		"tags":      r.URL.Query()["tag"],
		"requestId": r.Header.Get("X-Request-ID"),
	})
}

func status(w http.ResponseWriter, r *http.Request) {
	code, err := boundedInt(r.PathValue("code"), 0, 100, 599)
	if err != nil {
		writeError(w, http.StatusBadRequest, "code must be between 100 and 599")
		return
	}
	writeJSON(w, code, map[string]int{"status": code})
}

func redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/health", http.StatusFound)
}

func large(w http.ResponseWriter, r *http.Request) {
	size, err := boundedInt(r.URL.Query().Get("bytes"), 600*1024, 1, 2*1024*1024)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: nothing from the request is echoed. size is a bounded integer and the body is a run of "x" served as text/plain.
	_, _ = w.Write([]byte(strings.Repeat("x", size)))
}

func slow(w http.ResponseWriter, r *http.Request) {
	delay, err := boundedInt(r.URL.Query().Get("ms"), 100, 0, 60_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	select {
	case <-time.After(time.Duration(delay) * time.Millisecond):
		writeJSON(w, http.StatusOK, map[string]int{"waitedMs": delay})
	case <-r.Context().Done():
		return
	}
}

func pathInt(r *http.Request, name string) (int, error) {
	return boundedInt(r.PathValue(name), 0, 1, 1_000_000)
}

func boundedInt(raw string, fallback, lowest, highest int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lowest || n > highest {
		return 0, fmt.Errorf("value must be an integer between %d and %d", lowest, highest)
	}
	return n, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{"status": statusCode, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(value)
}
