package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// readHeaderTimeout bounds how long a connection may spend sending its
// headers. Without it a single idle socket holds a goroutine forever, which is
// the cheapest denial of service there is.
const readHeaderTimeout = 10 * time.Second

// Options configures the HTTP transport.
type Options struct {
	// Server is the MCP server to serve; mcpserver.New builds it. The same
	// instance backs stdio, which is what makes "the transport changes
	// nothing" a structural fact rather than a promise.
	Server *mcp.Server

	// Logger receives transport activity. stdout stays protocol-only even
	// here: a binary that may still be launched over stdio cannot have a code
	// path that prints (FR-68).
	Logger *slog.Logger

	// Listen is the bind address (host:port).
	Listen string

	// AllowedOrigins are the browser origins permitted to reach the endpoint
	// (FR-71).
	AllowedOrigins []string

	// AllowedHosts, when non-empty, is the exact set of Host headers
	// accepted.
	AllowedHosts []string

	// DrainTimeout bounds graceful shutdown (FR-75).
	DrainTimeout time.Duration

	// Authenticated reports whether an inbound authorization mode is in
	// force. Step 2 of feature 004 supplies it; until then it is false and a
	// public bind needs the opt-in below.
	Authenticated bool

	// AllowUnauthenticatedPublicBind is FR-70's explicitly dangerous opt-in.
	AllowUnauthenticatedPublicBind bool
}

func (o *Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (o *Options) drainTimeout() time.Duration {
	if o.DrainTimeout > 0 {
		return o.DrainTimeout
	}
	return 10 * time.Second
}

// Serve runs the stateless Streamable HTTP endpoint until the context is
// cancelled, then drains.
func Serve(ctx context.Context, opts *Options) error {
	if opts.Server == nil {
		return errs.Errorf(errs.ClassInternal, "server: no MCP server to serve")
	}
	if err := checkBind(opts.Listen, opts.Authenticated, opts.AllowUnauthenticatedPublicBind); err != nil {
		return err
	}
	handler, err := newHandler(opts)
	if err != nil {
		return err
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", opts.Listen)
	if err != nil {
		return errs.Errorf(errs.ClassUsage, "server: cannot listen on %s: %w", opts.Listen, err)
	}
	return serve(ctx, listener, handler, opts)
}

// newHandler wraps the MCP handler in the guards FR-71 requires.
//
// Two of the three are somebody else's implementation on purpose. The SDK
// already rejects a request that arrived over loopback carrying a non-loopback
// Host, which is the DNS-rebinding case, and net/http already implements
// cross-origin rejection. A second opinion about either would be a second
// thing to keep correct, and the one that drifted would be the one nobody
// noticed.
func newHandler(opts *Options) (http.Handler, error) {
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return opts.Server },
		&mcp.StreamableHTTPOptions{
			// The sessionless profile of FR-69: each POST gets a temporary
			// session, and GET and DELETE answer 405. This is what makes two
			// replicas behind a round-robin indistinguishable from one.
			Stateless: true,
			Logger:    opts.logger(),
			// DisableLocalhostProtection is deliberately left false.
			PropagateRequestCancellation: true,
		})
	return guard(mcpHandler, opts)
}

// guard is the chain every request passes before it becomes a tool call. It
// takes the handler it protects as an argument so that a test can put a spy
// there: "the refused request never reached the handler" is the assertion
// that matters, and it cannot be made against something unreachable.
func guard(next http.Handler, opts *Options) (http.Handler, error) {
	protection := http.NewCrossOriginProtection()
	for _, origin := range opts.AllowedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "server: allowedOrigins: %w", err)
		}
	}
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No detail. A refusal that explains itself to an unknown caller is a
		// policy oracle for anyone who can reach the port.
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	return allowHosts(protection.Handler(next), opts.AllowedHosts), nil
}

// allowHosts refuses a Host header the operator did not name.
//
// An empty list is not "allow everything" by choice so much as by honesty:
// behind a proxy the Host is whatever the ingress passes through, and lotsman
// has no way to know it. The loopback case, which is the one rebinding
// attacks aim at, stays covered by the SDK's own check.
func allowHosts(next http.Handler, allowed []string) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	permitted := make(map[string]bool, len(allowed))
	for _, host := range allowed {
		permitted[strings.ToLower(host)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !permitted[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serve runs the listener and drains on cancellation (FR-75).
func serve(ctx context.Context, listener net.Listener, handler http.Handler, opts *Options) error {
	log := opts.logger()
	info := buildinfo.Get()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		// The standard library's error log must not reach stdout either.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("serving mcp over http",
		"addr", listener.Addr().String(),
		"version", info.Version,
		"commit", info.Commit,
		"mcp_sdk", info.MCPSDKVersion,
		"mcp_protocol", info.MCPProtocolVersion,
		"stateless", true,
		"authenticated", opts.Authenticated)
	if !opts.Authenticated && opts.AllowUnauthenticatedPublicBind {
		// Every start, not once: an operator who meant this should keep being
		// told, and one who inherited it should find out from the first line
		// of the log rather than from a stranger.
		log.Warn("serving an unauthenticated endpoint on a public interface because it was explicitly permitted",
			"addr", listener.Addr().String())
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errs.Errorf(errs.ClassInternal, "server: %w", err)
	case <-ctx.Done():
		drain := opts.drainTimeout()
		log.Info("shutting down on signal; draining", "timeout", drain.String())
		// Detached from ctx, which is already cancelled: a drain budget that
		// expires the moment it is granted is not a drain.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("drain budget expired; closing connections", "timeout", drain.String(), "error", err)
			_ = srv.Close()
		}
		<-served
		log.Info("shutdown complete")
		return nil
	}
}
