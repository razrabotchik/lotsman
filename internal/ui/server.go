package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/razrabotchik/lotsman/internal/workbench"
)

//go:embed web
var web embed.FS

// cookiePrefix names the session cookie; the port follows, so two
// workbenches on one machine do not overwrite each other's session (cookies
// on loopback are not scoped by port).
const cookiePrefix = "lotsman_ui_"

// exchangePage moves the browser off the token address once the session
// cookie is set. It is a 200 page, not a redirect, so the navigation to "/"
// starts from this origin and the SameSite=Strict cookie goes with it even
// when the token address was opened from a local file. No script: the CSP
// allows none.
const exchangePage = `<!doctype html><html><head><meta charset="utf-8">` +
	`<meta http-equiv="refresh" content="0;url=/"><title>lotsman ui</title></head>` +
	`<body><p><a href="/">Continue to lotsman ui</a></p></body></html>
`

// usedMessage is what a second use of the token address gets.
const usedMessage = "this address was already used; restart lotsman ui for a new one"

// maxBody bounds a request body; a correction is a few kilobytes.
const maxBody = 1 << 20

// csp lets the page load only itself: no inline script, no third-party
// anything, no framing.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Server serves one workbench on loopback.
type Server struct {
	wb *workbench.Workbench
	// token is in the address the terminal prints; it is good for one
	// exchange, for session.
	token string
	// session is the cookie value: random, and never the token, so the
	// printed address (in a terminal's scrollback, a shell history, a
	// browser's history) cannot be replayed into a session.
	session  string
	used     atomic.Bool
	listener net.Listener
	port     int
}

// Listen binds 127.0.0.1:port (0 picks a free port) and generates the token.
// It never binds another address: the workbench is for this machine only.
func Listen(ctx context.Context, wb *workbench.Workbench, port int) (*Server, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return nil, fmt.Errorf("unexpected listener address %v", l.Addr())
	}
	token, err := random()
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	session, err := random()
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return &Server{wb: wb, token: token, session: session, listener: l, port: addr.Port}, nil
}

func random() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Server) cookieName() string { return cookiePrefix + strconv.Itoa(s.port) }

// URL is the address to open, token included. It is good once: opening it
// exchanges the token for a session cookie, and the address is refused
// after that.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.port, s.token)
}

// Serve runs until ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		// ctx is already done; the drain gets its own short deadline.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the whole server: guard first, then routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/operation", s.operation)
	mux.HandleFunc("POST /api/draft", s.apply)
	mux.HandleFunc("DELETE /api/draft", s.revert)
	mux.HandleFunc("GET /api/diff", s.diff)
	mux.HandleFunc("POST /api/save", s.save)
	mux.HandleFunc("POST /api/reload", s.reload)
	mux.HandleFunc("POST /api/explain", s.explain)
	return s.guard(mux)
}

// guard enforces spec §4 before any route: Host (DNS rebinding), token
// (another local user or a page in the browser), Origin on writes (CSRF),
// and the security headers on every response.
func (s *Server) guard(next http.Handler) http.Handler {
	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("localhost:%d", s.port): true,
	}
	origins := map[string]bool{
		fmt.Sprintf("http://127.0.0.1:%d", s.port): true,
		fmt.Sprintf("http://localhost:%d", s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !hosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !origins[r.Header.Get("Origin")] {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		hasSession := false
		if c, err := r.Cookie(s.cookieName()); err == nil && equal(c.Value, s.session) {
			hasSession = true
		}
		// The token arrives once, in the address the terminal printed. It is
		// accepted only if it is ours, so a page cannot fix a session of its
		// own choosing into the cookie, and only once.
		if q := r.URL.Query().Get("token"); q != "" && r.Method == http.MethodGet && r.URL.Path == "/" {
			switch {
			case !equal(q, s.token):
				http.Error(w, "forbidden", http.StatusForbidden)
			case hasSession:
				http.Redirect(w, r, "/", http.StatusSeeOther)
			case !s.used.CompareAndSwap(false, true):
				http.Error(w, usedMessage, http.StatusForbidden)
			default:
				// Not Secure: the server is plain HTTP on loopback by design.
				//nolint:gosec // G124: HttpOnly and SameSite=Strict are set; Secure cannot be over http://127.0.0.1.
				http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: s.session, Path: "/",
					HttpOnly: true, SameSite: http.SameSiteStrictMode})
				h.Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, exchangePage)
			}
			return
		}
		if !hasSession {
			http.Error(w, "open the address lotsman ui printed in the terminal", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// lighten drops per-operation schemas from a state: the tree needs status
// and names, and Kubernetes' schemas alone are megabytes. The form fetches
// one operation in full.
func lighten(st *workbench.State) *workbench.State {
	if st == nil || st.Draft == nil {
		return st
	}
	light := *st
	snap := *st.Draft
	snap.Operations = make([]workbench.Operation, len(st.Draft.Operations))
	for i := range st.Draft.Operations {
		op := st.Draft.Operations[i] // a copy: the caller's state is not touched
		op.InputSchema = nil
		op.Body = nil
		params := make([]workbench.Parameter, len(op.Parameters))
		for j, p := range op.Parameters {
			p.Schema = nil
			params[j] = p
		}
		op.Parameters = params
		snap.Operations[i] = op
	}
	light.Draft = &snap
	return &light
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.State(r.Context())
	reply(w, lighten(st), err)
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	st, err := s.wb.State(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	find := func(snap *workbench.Snapshot) *workbench.Operation {
		if snap == nil {
			return nil
		}
		for i := range snap.Operations {
			if snap.Operations[i].Key == key {
				return &snap.Operations[i]
			}
		}
		return nil
	}
	after := find(st.Draft)
	if after == nil {
		http.Error(w, "unknown operation", http.StatusNotFound)
		return
	}
	reply(w, map[string]any{"before": find(st.Base), "after": after}, nil)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var e workbench.Entry
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid entry", http.StatusBadRequest)
		return
	}
	st, err := s.wb.Apply(r.Context(), e)
	reply(w, lighten(st), err)
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.Revert(r.Context(), r.URL.Query().Get("id"))
	reply(w, lighten(st), err)
}

func (s *Server) diff(w http.ResponseWriter, _ *http.Request) {
	d, err := s.wb.Diff()
	reply(w, map[string]string{"diff": d}, err)
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Acknowledged []string `json:"acknowledged"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	st, err := s.wb.Save(r.Context(), body.Acknowledged)
	reply(w, lighten(st), err)
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.Reload(r.Context())
	reply(w, lighten(st), err)
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string          `json:"key"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	text, err := s.wb.Explain(r.Context(), body.Key, body.Arguments)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// reply maps workbench errors to statuses; a refused change still carries
// the state so the frontend can show why.
func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.Is(err, workbench.ErrChanged):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	case errors.Is(err, workbench.ErrRejected), errors.Is(err, workbench.ErrUnacknowledged):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "state": v})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
