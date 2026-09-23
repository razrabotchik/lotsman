package main_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A transport is one way to reach the real binary. Every scenario below runs
// from this table, which is the whole point: a refusal that holds over stdio
// and not over HTTP would be a bug in the server rather than a property of
// the protocol, and the only way to keep that true is to stop writing the
// assertions twice (T305).
var transports = []struct {
	name    string
	connect func(t *testing.T, args ...string) (*mcp.ClientSession, *syncBuffer)
}{
	{name: "stdio", connect: stdioSession},
	{name: "http", connect: httpSession},
}

// stdioSession starts the binary on a pipe, the way a desktop client does.
func stdioSession(t *testing.T, args ...string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(buildBinary(t), append([]string{"serve"}, args...)...)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-transport", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(),
		&mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, stderr
}

// httpSession starts the binary on a loopback socket and connects to the
// address it reports. The address is read out of the log rather than fixed in
// advance, because that is the only place an operator can read it either.
func httpSession(t *testing.T, args ...string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	endpoint, stderr := serveHTTP(t, args...)

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-transport", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect over http: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, stderr
}

// listening finds the bound address in the server's own startup line.
var listening = regexp.MustCompile(`addr=(\S+)`)

// serveHTTP starts the binary on an ephemeral loopback port and returns the
// endpoint to call it on.
func serveHTTP(t *testing.T, args ...string) (endpoint string, stderr *syncBuffer) {
	t.Helper()
	return serveHTTPEnv(t, nil, append([]string{"--listen", "127.0.0.1:0"}, args...)...)
}

// serveHTTPEnv is the same, with the environment a secret reference needs and
// the bind address left to the caller.
func serveHTTPEnv(t *testing.T, env []string, args ...string) (endpoint string, stderr *syncBuffer) {
	t.Helper()
	endpoint, stderr, _ = serveHTTPProcess(t, env, args...)
	return endpoint, stderr
}

// runBinary runs the CLI once and returns everything it printed.
func runBinary(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	return exec.Command(buildBinary(t), args...).CombinedOutput()
}

// serveHTTPProcess also hands back the process, for a test that needs to
// signal it.
func serveHTTPProcess(t *testing.T, env []string, args ...string) (endpoint string, stderr *syncBuffer, process *os.Process) {
	t.Helper()
	full := append([]string{"serve", "--transport", "http"}, args...)
	cmd := exec.Command(buildBinary(t), full...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stderr = &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if match := listening.FindStringSubmatch(stderr.String()); match != nil {
			return "http://" + match[1], stderr, cmd.Process
		}
		if strings.Contains(stderr.String(), "lotsman:") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the server never reported a listening address\nstderr:\n%s", stderr.String())
	return "", stderr, nil
}

// TestServeOverEveryTransport is the checkpoint of step 1: the scenarios that
// defined correct behaviour over stdio define it over HTTP too, unchanged.
func TestServeOverEveryTransport(t *testing.T) {
	for _, transport := range transports {
		t.Run(transport.name, func(t *testing.T) {
			requests := make(chan string, 8)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method + " " + r.URL.RequestURI()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"p-1","name":"Murka"}`))
			}))
			defer api.Close()

			session, stderr := transport.connect(t, miniSpecPath(t), "--lax", "--base-url", api.URL)

			if got := session.InitializeResult().ServerInfo.Name; got != "lotsman" {
				t.Errorf("server name = %q, want lotsman", got)
			}
			tools, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatalf("tools/list: %v\nstderr:\n%s", err, stderr.String())
			}
			if len(tools.Tools) == 0 {
				t.Fatal("tools/list published nothing")
			}

			// A read executes, with its parameters serialized the same way.
			res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "get_pet",
				Arguments: map[string]any{"path": map[string]any{"petId": "p 1/2"}},
			})
			if err != nil {
				t.Fatalf("tools/call get_pet: %v\nstderr:\n%s", err, stderr.String())
			}
			if res.IsError {
				t.Fatalf("get_pet returned an error result: %+v", res.Content)
			}
			select {
			case got := <-requests:
				if want := "GET /pets/p%201%2F2"; got != want {
					t.Errorf("upstream received %q, want %q", got, want)
				}
			default:
				t.Fatal("upstream received no request")
			}

			// And a mutation is still refused by default, with nothing
			// reaching the upstream. The gate was decided before a transport
			// existed and does not get a second opinion from one.
			mutation, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "create_pet",
				Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
			})
			if err == nil && !mutation.IsError {
				t.Fatal("a mutation executed under the read-only default")
			}
			select {
			case got := <-requests:
				t.Errorf("a refused mutation reached the upstream: %s", got)
			default:
			}
		})
	}
}

// TestHTTPMutationSurvivesTheApprovalRoundTrip is why the stateless profile is
// safe to deploy behind a load balancer at all.
//
// Approval is a multi-round-trip input request (ADR-0013): the server answers
// "input required", the client calls again with the answer, and the handler
// runs from the top. If any part of that ever came to depend on process
// memory, it would work on one replica and fail behind a round robin --
// intermittently, which is the worst way to find out.
func TestHTTPMutationSurvivesTheApprovalRoundTrip(t *testing.T) {
	requests := make(chan string, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-2","name":"Murka"}`))
	}))
	defer api.Close()

	session, stderr := httpSession(t, miniSpecPath(t), "--lax", "--allow-mutations",
		"--base-url", api.URL, "--log-level", "info")
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_pet",
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err != nil {
		t.Fatalf("tools/call create_pet: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("an approved mutation was refused: %+v\nstderr:\n%s", res.Content, stderr.String())
	}
	select {
	case got := <-requests:
		if !strings.HasPrefix(got, "POST ") {
			t.Errorf("upstream received %q, want a POST", got)
		}
	default:
		t.Fatal("the approved mutation never reached the upstream")
	}
	// The call must have gone *through* the prompt, not around it: a test
	// that only checks the POST arrived would pass just as happily if the
	// approval step had quietly stopped running over this transport.
	if log := stderr.String(); !strings.Contains(log, "requesting approval") || !strings.Contains(log, "approved") {
		t.Errorf("the mutation did not pass through an approval round trip:\n%s", log)
	}
}

// TestHTTPIsStatelessAcrossReplicas is the deployment claim of section 7.7,
// made as a test rather than as a diagram: two processes behind an
// alternating caller answer one session's worth of traffic with no sticky
// routing, because the profile has no sessions to be sticky about.
func TestHTTPIsStatelessAcrossReplicas(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"p-1","name":"Murka"}`))
	}))
	defer api.Close()

	first, firstErr := serveHTTP(t, miniSpecPath(t), "--lax", "--base-url", api.URL)
	second, secondErr := serveHTTP(t, miniSpecPath(t), "--lax", "--base-url", api.URL)

	var calls int
	balancer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := first
		if calls%2 == 1 {
			target = second
		}
		calls++
		proxy(t, w, r, target)
	}))
	defer balancer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-lb", Version: "v0"}, approving())
	session, err := client.Connect(t.Context(),
		// The standalone SSE stream is a persistent connection, which is the
		// one thing a round robin cannot serve; the sessionless profile does
		// not need it (FR-69).
		&mcp.StreamableClientTransport{Endpoint: balancer.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect through the balancer: %v\nfirst:\n%s\nsecond:\n%s",
			err, firstErr.String(), secondErr.String())
	}
	defer func() { _ = session.Close() }()

	if _, listErr := session.ListTools(t.Context(), nil); listErr != nil {
		t.Fatalf("tools/list through the balancer: %v", listErr)
	}
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}},
	})
	if err != nil {
		t.Fatalf("tools/call through the balancer: %v\nfirst:\n%s\nsecond:\n%s",
			err, firstErr.String(), secondErr.String())
	}
	if res.IsError {
		t.Fatalf("the call was refused: %+v", res.Content)
	}
	if calls < 2 {
		t.Fatalf("the balancer forwarded %d requests; the test never alternated replicas", calls)
	}
}

// proxy forwards one request to one replica. It is deliberately the dumbest
// possible load balancer: anything cleverer would be hiding the property
// under test.
func proxy(t *testing.T, w http.ResponseWriter, r *http.Request, target string) {
	t.Helper()
	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.Path, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for name, values := range r.Header {
		for _, value := range values {
			outbound.Header.Add(name, value)
		}
	}
	res, err := http.DefaultClient.Do(outbound)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = res.Body.Close() }()
	for name, values := range res.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(res.StatusCode)
	if _, err := io.Copy(w, res.Body); err != nil {
		t.Logf("proxy: copy body: %v", err)
	}
}

// TestPublicBindWithoutAuthenticationRefusesToStart is FR-70 at the binary's
// own boundary: not a warning, not a 403 on the first call, but a process
// that does not come up.
func TestPublicBindWithoutAuthenticationRefusesToStart(t *testing.T) {
	out, err := exec.Command(buildBinary(t), "serve", miniSpecPath(t), "--lax",
		"--transport", "http", "--listen", "0.0.0.0:0").CombinedOutput()
	if err == nil {
		t.Fatalf("serve started on a public interface with nothing authenticating it\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", code)
	}
	if !strings.Contains(string(out), "FR-70") {
		t.Errorf("the refusal does not name the requirement it enforces:\n%s", out)
	}
}

// TestHTTPWithoutASpecIsRefused: a reachable endpoint publishing nothing is
// indistinguishable from a broken deployment.
func TestHTTPWithoutASpecIsRefused(t *testing.T) {
	out, err := exec.Command(buildBinary(t), "serve", "--transport", "http").CombinedOutput()
	if err == nil {
		t.Fatalf("serve --transport=http started with no specification\n%s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", code)
	}
}

// exitCodeOf reports the exit status of a failed command.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var exit *exec.ExitError
	if !errorAs(err, &exit) {
		t.Fatalf("not an exit error: %v", err)
	}
	return exit.ExitCode()
}
