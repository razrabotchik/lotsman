package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

func testServer(t *testing.T) (srv *Server, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: lotsman.dev/v1alpha1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := workbench.Pipeline{
		Evaluate: func(_ context.Context, data []byte) (*workbench.Snapshot, error) {
			op := workbench.Operation{Key: "k", Method: "POST", Path: "/k", OperationID: "makeK", Effect: "write",
				InputSchema: map[string]any{"big": true}}
			if file, err := config.Parse(data); err == nil {
				for i := range file.OperationOverrides {
					if file.OperationOverrides[i].Effect != "" {
						op.Effect = file.OperationOverrides[i].Effect
					}
				}
			}
			return &workbench.Snapshot{Operations: []workbench.Operation{op}}, nil
		},
		Explain: func(context.Context, []byte, string, []byte) (string, error) { return "x", nil },
	}
	wb, err := workbench.New(t.Context(), path, "", pipe)
	if err != nil {
		t.Fatal(err)
	}
	srv, err = Listen(t.Context(), wb, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.listener.Close() })
	return srv, path
}

func own(srv *Server) string { return fmt.Sprintf("127.0.0.1:%d", srv.port) }

func request(t *testing.T, srv *Server, method, target, host, origin string, cookie bool, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), srv.token) {
		t.Fatalf("%s %s: the token appears in the response body", method, target)
	}
	return rec
}

func do(t *testing.T, srv *Server, method, target, host, origin string, cookie bool) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, srv, method, target, host, origin, cookie, nil)
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	addr, ok := srv.listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v", srv.listener.Addr())
	}
	if len(srv.token) != 64 || !strings.Contains(srv.URL(), "http://127.0.0.1:") || !strings.HasSuffix(srv.URL(), "/?token="+srv.token) {
		t.Fatalf("url = %s", srv.URL())
	}
	other, _ := testServer(t)
	if other.token == srv.token {
		t.Fatal("two servers share a token")
	}
}

// Review focus 3.
func TestGuard(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	host := own(srv)
	origin := "http://" + host
	cases := []struct {
		name, method, target, host, origin string
		cookie                             bool
		want                               int
	}{
		{"no token", "GET", "/api/state", host, "", false, 403},
		{"no token, page", "GET", "/", host, "", false, 403},
		{"foreign host (DNS rebinding)", "GET", "/api/state", "evil.example:" + fmt.Sprint(srv.port), "", true, 403},
		{"our address on another port", "GET", "/api/state", fmt.Sprintf("127.0.0.1:%d", srv.port+1), "", true, 403},
		{"write without origin", "POST", "/api/reload", host, "", true, 403},
		{"write from another site", "POST", "/api/reload", host, "http://evil.example", true, 403},
		{"write from a null origin", "POST", "/api/reload", host, "null", true, 403},
		{"delete without origin", "DELETE", "/api/draft?id=x", host, "", true, 403},
		{"preflight without origin", "OPTIONS", "/api/draft", host, "", true, 403},
		{"preflight from another site", "OPTIONS", "/api/draft", host, "http://evil.example", true, 403},
		{"someone else's token in the URL", "GET", "/?token=" + strings.Repeat("0", 64), host, "", false, 403},
		{"our token sets the cookie", "GET", "/?token=" + srv.token, host, "", false, 303},
		{"read with cookie", "GET", "/api/state", host, "", true, 200},
		{"page with cookie", "GET", "/", host, "", true, 200},
		{"unknown path", "GET", "/nothing-here", host, "", true, 404},
		{"localhost alias", "GET", "/api/state", fmt.Sprintf("localhost:%d", srv.port), "", true, 200},
		{"write with cookie and origin", "POST", "/api/reload", host, origin, true, 200},
		{"write from the localhost alias", "POST", "/api/reload", fmt.Sprintf("localhost:%d", srv.port), fmt.Sprintf("http://localhost:%d", srv.port), true, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, srv, tc.method, tc.target, tc.host, tc.origin, tc.cookie)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			h := rec.Header()
			if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
				!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatalf("CSP = %q", h.Get("Content-Security-Policy"))
			}
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("headers = %v", h)
			}
			cookies := rec.Result().Cookies()
			if tc.want == 303 {
				if len(cookies) != 1 || cookies[0].Value != srv.token || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
					t.Fatalf("cookie = %+v", cookies)
				}
				if rec.Header().Get("Location") != "/" {
					t.Fatalf("redirect to %q: the token must leave the address bar", rec.Header().Get("Location"))
				}
			} else if len(cookies) != 0 {
				t.Fatalf("a cookie was set: %+v", cookies)
			}
		})
	}
}

func TestAWrongCookieIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/state", http.NoBody)
	req.Host = own(srv)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: strings.Repeat("0", 64)})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAnOversizedBodyIsRefused(t *testing.T) {
	t.Parallel()
	srv, path := testServer(t)
	huge := `{"kind":"operation","operation":{"match":{"operationId":"makeK"},"note":"` + strings.Repeat("x", 2<<20) + `"}}`
	rec := request(t, srv, "POST", "/api/draft", own(srv), "http://"+own(srv), true, strings.NewReader(huge))
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want 4xx", rec.Code)
	}
	st := do(t, srv, "GET", "/api/state", own(srv), "", true)
	var state struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(st.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("the oversized entry was taken: %s", st.Body.String())
	}
	if out, _ := os.ReadFile(path); strings.Contains(string(out), "xxxx") {
		t.Fatal("the oversized entry reached the file")
	}
}

func TestStateIsLight(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, "GET", "/api/state", own(srv), "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "big") {
		t.Fatalf("state carries schemas: %s", rec.Body.String())
	}
	rec = do(t, srv, "GET", "/api/operation?key=k", own(srv), "", true)
	if !strings.Contains(rec.Body.String(), "big") {
		t.Fatalf("operation lacks its schema: %s", rec.Body.String())
	}
	if rec = do(t, srv, "GET", "/api/operation?key=missing", own(srv), "", true); rec.Code != 404 {
		t.Fatalf("unknown operation: %d", rec.Code)
	}
}

// The API end to end over the handler: a change, its diff, a refused save
// without the acknowledgement, the save with it.
func TestDraftDiffAndSaveOverTheAPI(t *testing.T) {
	t.Parallel()
	srv, path := testServer(t)
	host, origin := own(srv), "http://"+own(srv)
	post := func(target, body string) *httptest.ResponseRecorder {
		return request(t, srv, "POST", target, host, origin, true, strings.NewReader(body))
	}
	rec := post("/api/draft", `{"kind":"operation","operationKey":"k","operation":{"match":{"operationId":"makeK"},"effect":"read"}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"effect-read"`) {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	if rec = do(t, srv, "GET", "/api/diff", host, "", true); !strings.Contains(rec.Body.String(), "+    effect: read") {
		t.Fatalf("diff: %s", rec.Body.String())
	}
	if rec = post("/api/save", `{"acknowledged":[]}`); rec.Code != 422 {
		t.Fatalf("save without acknowledgement: %d %s", rec.Code, rec.Body.String())
	}
	if out, _ := os.ReadFile(path); strings.Contains(string(out), "effect") {
		t.Fatal("the unacknowledged save wrote the file")
	}
	if rec = post("/api/save", `{"acknowledged":["k#effect-read"]}`); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if out, _ := os.ReadFile(path); !strings.Contains(string(out), "effect: read") {
		t.Fatalf("saved:\n%s", out)
	}
	if rec = post("/api/draft", `not json`); rec.Code != 400 {
		t.Fatalf("invalid entry: %d", rec.Code)
	}
	if rec = post("/api/explain", `{"key":"k","arguments":{}}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"x"`) {
		t.Fatalf("explain: %d %s", rec.Code, rec.Body.String())
	}
}

func TestASaveOverAHandEditIsAConflict(t *testing.T) {
	t.Parallel()
	srv, path := testServer(t)
	host, origin := own(srv), "http://"+own(srv)
	rec := request(t, srv, "POST", "/api/draft", host, origin, true,
		strings.NewReader(`{"kind":"operation","operationKey":"k","operation":{"match":{"operationId":"makeK"},"effect":"destructive"}}`))
	if rec.Code != 200 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	if err := os.WriteFile(path, []byte("apiVersion: lotsman.dev/v1alpha1\n# by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec = request(t, srv, "POST", "/api/save", host, origin, true, strings.NewReader(`{}`)); rec.Code != 409 {
		t.Fatalf("save over a hand edit: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEmbeddedFrontendFitsTheBudget(t *testing.T) {
	t.Parallel()
	var total int64
	if err := fsWalk(func(size int64) { total += size }); err != nil {
		t.Fatal(err)
	}
	if total == 0 || total > 400_000 {
		t.Fatalf("embedded frontend is %d bytes; the budget is 400 000 (spec §4)", total)
	}
}

func fsWalk(add func(int64)) error {
	return fs.WalkDir(web, "web", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			add(info.Size())
		}
		return err
	})
}

// The page loads nothing the CSP forbids: no inline script or style, no
// third-party origin.
func TestThePageNeedsNothingTheCSPForbids(t *testing.T) {
	t.Parallel()
	page, err := fs.ReadFile(web, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, bad := range []string{"<script>", "style=", "http://", "https://", "onclick", "onload"} {
		if strings.Contains(html, bad) {
			t.Errorf("index.html contains %q", bad)
		}
	}
}
