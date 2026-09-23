package main_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// protocolVersion is the revision this build targets (FR-69). A conformance
// assertion that read the version out of the binary would agree with it by
// construction, so it is written down here instead.
const protocolVersion = "2026-07-28"

// These tests speak JSON-RPC to the real binary rather than going through the
// SDK client, which is the point of them: every other end-to-end test in this
// package asserts that the Go client and the Go server agree, and two halves
// of one SDK agreeing is not evidence about the wire.
//
// What this is not: the cross-SDK conformance runner from the
// modelcontextprotocol project. That is external tooling driving reference
// implementations, it is not vendored here, and it has still not been run
// (docs/release-v0.1.0-alpha.md, criterion 10).

// post sends one raw request to the endpoint and returns the response.
func post(t *testing.T, endpoint, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		if value == "" {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, value)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// decodeFrame reads one JSON-RPC message out of a response, whether it came
// back as JSON or as a single server-sent event.
func decodeFrame(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	payload := string(raw)
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		payload = ""
		for _, line := range strings.Split(string(raw), "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				payload = strings.TrimSpace(after)
				break
			}
		}
	}
	if payload == "" {
		t.Fatalf("no JSON-RPC frame in the response:\n%s", raw)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	return frame
}

const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"` + protocolVersion + `","capabilities":{},` +
	`"clientInfo":{"name":"conformance","version":"v0"}}}`

// sessionlessMeta is what a 2026-07-28 client carries on every request
// instead of holding a session: the protocol version, its capabilities and
// its identity, repeated because there is nowhere to remember them.
const sessionlessMeta = `{"io.modelcontextprotocol/protocolVersion":"` + protocolVersion + `",` +
	`"io.modelcontextprotocol/clientCapabilities":{},` +
	`"io.modelcontextprotocol/clientInfo":{"name":"conformance","version":"v0"}}`

// sessionless sends a request in the new protocol's shape.
func sessionless(t *testing.T, endpoint, method, id string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":{"_meta":` + sessionlessMeta + `}}`
	res := post(t, endpoint, body, map[string]string{
		"Mcp-Protocol-Version": protocolVersion,
		"Mcp-Method":           method,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s: status = %d", method, res.StatusCode)
	}
	return decodeFrame(t, res)
}

// TestHTTPConformance checks the parts of the Streamable HTTP profile a
// client depends on and the SDK client would never exercise, because it
// always does the right thing.
func TestHTTPConformance(t *testing.T) {
	endpoint, _ := serveHTTP(t, miniSpecPath(t), "--lax")

	// This is FR-69's actual claim, and until this test existed nothing
	// checked it: every other end-to-end test here lets the SDK client do the
	// handshake, so "sessionless" was an option we set rather than a
	// behaviour anyone observed.
	t.Run("the sessionless profile needs no handshake", func(t *testing.T) {
		discovery, _ := sessionless(t, endpoint, "server/discover", "1")["result"].(map[string]any)
		if discovery == nil {
			t.Fatalf("server/discover returned no result: %+v", sessionless(t, endpoint, "server/discover", "1"))
		}
		versions, _ := discovery["supportedVersions"].([]any)
		var found bool
		for _, version := range versions {
			if version == protocolVersion {
				found = true
			}
		}
		if !found {
			t.Errorf("supportedVersions = %v, want it to contain %s", versions, protocolVersion)
		}

		// And the point of it: a call with no prior initialize, on a
		// connection that remembers nothing.
		listing, _ := sessionless(t, endpoint, "tools/list", "2")["result"].(map[string]any)
		if listing == nil {
			t.Fatal("tools/list was refused without a session")
		}
		if tools, _ := listing["tools"].([]any); len(tools) == 0 {
			t.Error("tools/list published nothing")
		}
	})

	t.Run("the legacy handshake negotiates a legacy revision", func(t *testing.T) {
		res := post(t, endpoint, initializeRequest, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", res.StatusCode)
		}
		frame := decodeFrame(t, res)
		result, _ := frame["result"].(map[string]any)
		if result == nil {
			t.Fatalf("initialize returned no result: %+v", frame)
		}
		// Not 2026-07-28, and that is correct rather than a shortfall:
		// `initialize` is the handshake the new revision removed, so a client
		// using it is by definition not speaking it. The SDK caps the answer
		// at the last revision that had an initialize.
		if got := result["protocolVersion"]; got != "2025-11-25" {
			t.Errorf("protocolVersion = %v, want 2025-11-25 for a legacy handshake", got)
		}
		info, _ := result["serverInfo"].(map[string]any)
		if info["name"] != "lotsman" {
			t.Errorf("serverInfo.name = %v", info["name"])
		}
		// The sessionless profile assigns no session (FR-69): a client that
		// echoed one back would be told to by a server that is not stateless.
		if id := res.Header.Get("Mcp-Session-Id"); id != "" {
			t.Errorf("a stateless server handed out a session id: %q", id)
		}
	})

	t.Run("GET and DELETE are refused with an Allow header", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			req, err := http.NewRequestWithContext(t.Context(), method, endpoint, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept", "application/json, text/event-stream")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s status = %d, want 405", method, res.StatusCode)
			}
			// RFC 9110 §15.5.6: a 405 MUST say what is allowed.
			if allow := res.Header.Get("Allow"); !strings.Contains(allow, http.MethodPost) {
				t.Errorf("%s Allow = %q, want it to name POST", method, allow)
			}
		}
	})

	t.Run("a wrong content type is refused", func(t *testing.T) {
		res := post(t, endpoint, initializeRequest, map[string]string{"Content-Type": "text/plain"})
		if res.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", res.StatusCode)
		}
	})

	t.Run("an unusable Accept is refused", func(t *testing.T) {
		res := post(t, endpoint, initializeRequest, map[string]string{"Accept": "text/plain"})
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})

	// A known deviation, asserted as loosely as it can honestly be. JSON-RPC
	// 2.0 §5.1 says an unknown method is answered with a -32601 frame; over
	// the stateless HTTP path the SDK rejects it at the transport instead,
	// with a 400 and a plain-text body. The refusal is safe, so this asserts
	// only that -- a test pinned to the current shape would have to be
	// rewritten as a failure the day the SDK conforms.
	t.Run("an unknown method is refused without taking the server down", func(t *testing.T) {
		res := post(t, endpoint,
			`{"jsonrpc":"2.0","id":2,"method":"lotsman/doesNotExist","params":{}}`, nil)
		if res.StatusCode == http.StatusOK {
			if frame := decodeFrame(t, res); frame["error"] == nil {
				t.Errorf("an unknown method was accepted: %+v", frame)
			}
		} else if res.StatusCode < 400 || res.StatusCode >= 500 {
			t.Errorf("status = %d, want a client error", res.StatusCode)
		}
		if next := post(t, endpoint, initializeRequest, nil); next.StatusCode != http.StatusOK {
			t.Errorf("the server stopped answering afterwards: %d", next.StatusCode)
		}
	})

	t.Run("malformed JSON does not take the server down", func(t *testing.T) {
		post(t, endpoint, `{"jsonrpc":"2.0",`, nil)
		if next := post(t, endpoint, initializeRequest, nil); next.StatusCode != http.StatusOK {
			t.Errorf("the server stopped answering after malformed input: %d", next.StatusCode)
		}
	})
}

// TestStdioConformance is the same question on the other transport: stdout
// carries protocol frames and nothing else (FR-68), and an unknown method is
// an error rather than a silence.
func TestStdioConformance(t *testing.T) {
	cmd := exec.Command(buildBinary(t), "serve", miniSpecPath(t), "--lax", "--log-level", "debug")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	frames := bufio.NewScanner(stdout)
	frames.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	read := func() map[string]any {
		t.Helper()
		done := make(chan map[string]any, 1)
		go func() {
			if !frames.Scan() {
				done <- nil
				return
			}
			var frame map[string]any
			if err := json.Unmarshal(frames.Bytes(), &frame); err != nil {
				t.Errorf("stdout carried something that is not a JSON-RPC frame: %q", frames.Text())
				done <- nil
				return
			}
			done <- frame
		}()
		select {
		case frame := <-done:
			if frame == nil {
				t.Fatalf("no frame on stdout\nstderr:\n%s", stderr.String())
			}
			return frame
		case <-time.After(20 * time.Second):
			t.Fatalf("timed out waiting for a frame\nstderr:\n%s", stderr.String())
			return nil
		}
	}

	send(initializeRequest)
	result, _ := read()["result"].(map[string]any)
	if result == nil {
		t.Fatalf("initialize over stdio returned no result")
	}
	// The legacy handshake, so the legacy revision -- same reason as over
	// HTTP: `initialize` is the handshake 2026-07-28 removed.
	if got := result["protocolVersion"]; got != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want 2025-11-25 for a legacy handshake", got)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	send(`{"jsonrpc":"2.0","id":2,"method":"lotsman/doesNotExist","params":{}}`)
	failure, _ := read()["error"].(map[string]any)
	if failure == nil {
		t.Fatal("an unknown method produced no error over stdio")
	}
	if code, _ := failure["code"].(float64); int(code) != -32601 {
		t.Errorf("error code = %v, want -32601", failure["code"])
	}

	// The server logged plenty at debug level; none of it belongs on stdout.
	if stderr.String() == "" {
		t.Error("the server logged nothing, so the stdout assertion above proves little")
	}
}
