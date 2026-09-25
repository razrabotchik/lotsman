package response

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// counting is a body that reports how much of it was actually read.
type counting struct {
	remaining int
	read      int
}

func (c *counting) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > c.remaining {
		n = c.remaining
	}
	for i := range n {
		p[i] = 'a'
	}
	c.remaining -= n
	c.read += n
	return n, nil
}

func (c *counting) Close() error { return nil }

// MaxBodyBytes bounds how much is *read into memory*, and that is a different
// property from how much is handed back.
//
// The truncation tests cannot see it: without the limit, io.ReadAll pulls the
// whole body in and the result is then cut to size anyway, so every assertion
// about the returned Body still holds while the process has already allocated
// whatever the API chose to send. What stops an upstream from deciding this
// runtime's memory footprint is the reader, and this is the test that says so.
func TestNoMoreThanTheLimitIsEverReadIntoMemory(t *testing.T) {
	body := &counting{remaining: MaxBodyBytes * 4}
	result, err := FromHTTP(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       body,
	}, 0)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}

	// One byte past the limit is what tells "exactly the limit" and
	// "truncated" apart; anything beyond that was read for nothing.
	if body.read > MaxBodyBytes+1 {
		t.Errorf("read %d bytes into memory, want at most %d: an upstream can choose this "+
			"process's memory footprint", body.read, MaxBodyBytes+1)
	}
	if !result.Truncated {
		t.Error("a body over the limit was not reported as truncated")
	}
	if len(result.Body) > MaxBodyBytes {
		t.Errorf("Body is %d bytes, over the limit", len(result.Body))
	}
}

// And a body under the limit is read exactly once and whole.
func TestASmallBodyIsReadWhole(t *testing.T) {
	const size = 1024
	body := &counting{remaining: size}
	result, err := FromHTTP(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       body,
	}, 0)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if body.read != size {
		t.Errorf("read %d bytes, want %d", body.read, size)
	}
	if result.Truncated {
		t.Error("a complete body was reported as truncated")
	}
	if result.Body != strings.Repeat("a", size) {
		t.Errorf("Body is %d bytes, want %d", len(result.Body), size)
	}
}

// A configured cap is the one that holds, which is the point of it being a
// parameter: an operator lowering the bound gets the lower number, not the
// package default.
func TestAConfiguredLimitHolds(t *testing.T) {
	const tighter = 4096
	body := &counting{remaining: MaxBodyBytes}
	result, err := FromHTTP(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       body,
	}, tighter)
	if err != nil {
		t.Fatalf("FromHTTP: %v", err)
	}
	if body.read > tighter+1 {
		t.Errorf("read %d bytes with a %d-byte cap configured", body.read, tighter)
	}
	if len(result.Body) != tighter {
		t.Errorf("Body is %d bytes, want the configured %d", len(result.Body), tighter)
	}
	if !result.Truncated {
		t.Error("a body over the configured cap was not reported as truncated")
	}
}
