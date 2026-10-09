package httpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startServing runs serve with handler and a short read timeout.
func startServing(t *testing.T, handler http.Handler, readTimeout time.Duration) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	opts := &Options{Server: testServer(t), DrainTimeout: time.Second, ReadTimeout: readTimeout}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, handler, opts) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return listener.Addr().String()
}

// A client that sends its headers and then trickles its body is cut off at
// the read timeout instead of holding a connection for as long as it likes.
func TestASlowBodyIsCutOffAtTheReadTimeout(t *testing.T) {
	readErr := make(chan error, 1)
	addr := startServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
		w.WriteHeader(http.StatusBadRequest)
	}), 200*time.Millisecond)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 100\r\n\r\n0123456789")

	select {
	case err := <-readErr:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("reading the body ended with %v, want a timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a body that never arrived was waited for past the read timeout")
	}
}

// The read timeout bounds the read and nothing after it. A tool call that
// outlives it -- most do, against a slow API -- keeps its context: with
// cancellation propagated to the upstream request, anything else would cut
// every long call off mid-flight.
func TestAHandlerThatOutlivesTheReadTimeoutIsNotCancelled(t *testing.T) {
	cancelled := make(chan error, 1)
	addr := startServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- r.Context().Err()
		case <-time.After(600 * time.Millisecond):
			cancelled <- nil
		}
		w.WriteHeader(http.StatusOK)
	}), 200*time.Millisecond)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = res.Body.Close()
	if err := <-cancelled; err != nil {
		t.Errorf("the handler's context ended at the read timeout: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
}
