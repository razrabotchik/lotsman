package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodGet, "/health", nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestPetLifecycle(t *testing.T) {
	h := newTestHandler()

	unauthorized := request(t, h, http.MethodPost, "/v1/pets", strings.NewReader(`{"name":"Rex"}`), nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	created := request(t, h, http.MethodPost, "/v1/pets", strings.NewReader(`{"name":"Rex","tags":["dog"]}`), map[string]string{
		"Content-Type": "application/json",
		"X-API-Key":    apiKey,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var got pet
	if err := json.Unmarshal(created.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 3 || got.Name != "Rex" {
		t.Fatalf("created pet = %#v", got)
	}

	fetched := request(t, h, http.MethodGet, "/v1/pets/3", nil, nil)
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"name":"Rex"`) {
		t.Fatalf("get response = %d %s", fetched.Code, fetched.Body.String())
	}

	deleted := request(t, h, http.MethodDelete, "/v1/pets/3", nil, map[string]string{"X-API-Key": apiKey})
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleted.Code)
	}
}

func TestFixtureBehaviors(t *testing.T) {
	h := newTestHandler()
	tests := []struct {
		name   string
		path   string
		header map[string]string
		status int
	}{
		{name: "bearer denied", path: "/v1/secure/profile", status: http.StatusUnauthorized},
		{name: "bearer accepted", path: "/v1/secure/profile", header: map[string]string{"Authorization": "Bearer " + bearerToken}, status: http.StatusOK},
		{name: "custom status", path: "/v1/status/418", status: http.StatusTeapot},
		{name: "redirect", path: "/v1/redirect", status: http.StatusFound},
		{name: "query echo", path: "/v1/echo?query=hello&tag=a&tag=b", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := request(t, h, http.MethodGet, tt.path, nil, tt.header)
			if res.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", res.Code, tt.status, res.Body.String())
			}
		})
	}
}

func TestOpenAPIDocumentIsServed(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodGet, "/openapi.yaml", nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if !bytes.Contains(res.Body.Bytes(), []byte("openapi: 3.1.0")) {
		t.Fatalf("response is not the embedded OpenAPI document")
	}
}

func newTestHandler() http.Handler {
	return newHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(t *testing.T, handler http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}
