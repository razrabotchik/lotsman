package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// Budget bounds every phase of an outbound call (FR-32). One overall timeout
// is not enough: a server that accepts the connection and then sends a byte a
// minute stays inside any total budget while holding the call open, and a
// connect that hangs is indistinguishable from a slow API without its own
// limit.
type Budget struct {
	Total          time.Duration
	Connect        time.Duration
	TLSHandshake   time.Duration
	ResponseHeader time.Duration
	IdleConnection time.Duration
}

// DefaultBudget matches the example configuration in docs/spec.md
// (execution.timeout: 30s) with per-phase limits derived from it.
func DefaultBudget() Budget {
	return Budget{
		Total:          30 * time.Second,
		Connect:        10 * time.Second,
		TLSHandshake:   10 * time.Second,
		ResponseHeader: 20 * time.Second,
		IdleConnection: 60 * time.Second,
	}
}

func (b Budget) orDefaults() Budget {
	defaults := DefaultBudget()
	if b.Total <= 0 {
		b.Total = defaults.Total
	}
	if b.Connect <= 0 {
		b.Connect = defaults.Connect
	}
	if b.TLSHandshake <= 0 {
		b.TLSHandshake = defaults.TLSHandshake
	}
	if b.ResponseHeader <= 0 {
		b.ResponseHeader = defaults.ResponseHeader
	}
	if b.IdleConnection <= 0 {
		b.IdleConnection = defaults.IdleConnection
	}
	return b
}

// Policy is the outbound network policy: where lotsman may connect, and under
// what budgets.
//
// Retry is absent on purpose rather than present and disabled. FR-34 allows it
// only for idempotent operations, bounded to transient failures and honouring
// Retry-After, and a mutation without an idempotency key may never be
// retried -- none of which is expressible until the effect model and the
// config layer meet. A field that could be set to true before those rules
// exist would be a hole with a name.
type Policy struct {
	// AllowedOrigins is the set of "scheme://host:port" origins lotsman may
	// call. An empty policy allows nothing: a server URL authored by an
	// untrusted document is not authorization (ADR-0005).
	AllowedOrigins []string
	// AllowPrivateNetworks permits a *hostname* that resolves into a private,
	// loopback or link-local range. An origin written as an address or as
	// localhost never needs it: naming 127.0.0.1 is intent, not rebinding.
	AllowPrivateNetworks bool
	Budget               Budget

	// resolver is a test seam. Production uses net.DefaultResolver; a test
	// needs to say "this name resolves to the metadata endpoint" without
	// owning a zone to prove it.
	resolver interface {
		LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	}
}

// CheckTarget authorizes a target URL against the policy, before any
// connection is attempted.
func (p *Policy) CheckTarget(target *url.URL) error {
	if err := validateHTTPURL(target, true); err != nil {
		return fmt.Errorf("%w: target: %w", ErrDenied, err)
	}
	if len(p.AllowedOrigins) == 0 {
		return fmt.Errorf("%w: no origin is allowed; configure execution.allowedOrigins or pass --base-url", ErrDenied)
	}

	targetOrigin := origin(target)
	for _, allowed := range p.AllowedOrigins {
		parsed, err := url.Parse(allowed)
		if err != nil {
			return fmt.Errorf("%w: allowed origin is not a URL", ErrDenied)
		}
		if err := validateHTTPURL(parsed, false); err != nil {
			return fmt.Errorf("%w: allowed origin: %w", ErrDenied, err)
		}
		if origin(parsed) == targetOrigin {
			return nil
		}
	}
	return fmt.Errorf("%w: origin %q is not in the allowed set", ErrDenied, targetOrigin)
}

// Client builds the HTTP client for this policy: budgets, no redirects, no
// proxy inherited from the environment, and an address check on every
// connection.
//
// base lets a caller inject a transport (a test server's, typically). Its
// transport is kept as-is: a caller that supplies one has already decided how
// connections are made.
func (p *Policy) Client(base *http.Client) *http.Client {
	budget := p.Budget.orDefaults()

	clone := &http.Client{Timeout: budget.Total}
	if base != nil {
		clone = &http.Client{Transport: base.Transport, Jar: base.Jar, Timeout: budget.Total}
	}
	if clone.Transport == nil {
		clone.Transport = p.transport(budget)
	}
	// Redirects are refused rather than followed: each hop would need to pass
	// the policy again, and sensitive headers must never cross an origin
	// (FR-33). Following them safely is a feature, not a default.
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return clone
}

func (p *Policy) transport(budget Budget) *http.Transport {
	dialer := &net.Dialer{Timeout: budget.Connect, KeepAlive: 30 * time.Second}

	def, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// A caller replaced the default transport; build a plain one rather
		// than inherit whatever it does.
		def = &http.Transport{}
	}
	transport := def.Clone()
	// Proxy settings are not inherited: a proxy from the environment would
	// send every request, and every credential, somewhere lotsman never
	// authorized.
	transport.Proxy = nil
	transport.TLSHandshakeTimeout = budget.TLSHandshake
	transport.ResponseHeaderTimeout = budget.ResponseHeader
	transport.IdleConnTimeout = budget.IdleConnection
	transport.DialContext = p.dial(dialer)
	return transport
}

// dial resolves the name itself, checks every address it gets, and then
// connects to an address it has checked.
//
// Handing the name to the dialer and checking "the address" is the obvious
// implementation and it does not work: http.Transport passes DialContext the
// *host:port from the URL*, so the check would run on a hostname and
// resolution would happen afterwards, inside the dialer, unchecked. Resolving
// first and dialing the resolved address also closes the window between the
// two -- there is no second lookup to return a different answer.
func (p *Policy) dial(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot parse address %q", ErrDenied, address)
		}

		if ip := net.ParseIP(host); ip != nil {
			if err := p.checkIP(host, ip); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		}

		addresses, lookupErr := p.lookup(ctx, host)
		if lookupErr != nil {
			return nil, lookupErr
		}

		var refusals []string
		var lastDialErr error
		for _, candidate := range addresses {
			if checkErr := p.checkIP(host, candidate.IP); checkErr != nil {
				refusals = append(refusals, candidate.IP.String())
				continue
			}
			// Connect to the address that was checked, not to the name.
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastDialErr = dialErr
		}
		if len(refusals) > 0 {
			return nil, fmt.Errorf("%w: %q resolved to %s, which is private or link-local; "+
				"set execution.allowPrivateNetworks to permit it", ErrDenied, host, strings.Join(refusals, ", "))
		}
		if lastDialErr == nil {
			lastDialErr = fmt.Errorf("%w: %q resolved to no usable address", ErrDenied, host)
		}
		return nil, lastDialErr
	}
}

func (p *Policy) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to no address", ErrDenied, host)
	}
	return addresses, nil
}

// checkIP is the DNS rebinding defence (FR-32's neighbour): the origin check
// runs on a name, and a name can resolve to anything -- including
// 169.254.169.254, which on most clouds hands out credentials to whoever asks.
//
// host is the name (or literal) the origin was written as. An origin written
// as an address, or as localhost, is exempt: naming a private address is
// intent, and refusing it would make lotsman unusable against a local API
// while stopping no attack.
func (p *Policy) checkIP(host string, ip net.IP) error {
	if !isPrivate(ip) || p.AllowPrivateNetworks || p.allowsLiteral(host) {
		return nil
	}
	return fmt.Errorf("%w: %q resolved to %s, which is a private or link-local address; "+
		"set execution.allowPrivateNetworks to permit it", ErrDenied, host, ip)
}

// allowsLiteral reports whether an allowed origin names this host directly as
// an address, or names localhost, which is the operator saying they meant it.
//
// A hostname is never "literal": that is the whole case this guard exists for.
func (p *Policy) allowsLiteral(host string) bool {
	if net.ParseIP(host) == nil && !strings.EqualFold(host, "localhost") {
		return false
	}
	for _, allowed := range p.AllowedOrigins {
		parsed, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		hostname := parsed.Hostname()
		if strings.EqualFold(hostname, "localhost") && strings.EqualFold(host, "localhost") {
			return true
		}
		if hostname == host {
			return true
		}
		if ip := net.ParseIP(hostname); ip != nil && ip.String() == host {
			return true
		}
	}
	return false
}

// isPrivate reports whether an address belongs to a range that a public API
// never legitimately lives in.
func isPrivate(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// Carrier-grade NAT and the IPv4-mapped forms of the above.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		return false
	}
	// IPv6 unique local addresses (fc00::/7).
	return len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc
}

// PolicyFromBaseURL is the tracer-bullet policy: the operator named one
// origin on the command line, and that is the only one allowed.
func PolicyFromBaseURL(baseURL string, budget Budget, allowPrivate bool) (Policy, error) {
	if baseURL == "" {
		return Policy{Budget: budget, AllowPrivateNetworks: allowPrivate}, nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return Policy{}, errs.Errorf(errs.ClassUsage, "egress: invalid base URL syntax")
	}
	if err := validateHTTPURL(parsed, false); err != nil {
		return Policy{}, errs.Errorf(errs.ClassUsage, "egress: base URL: %w", err)
	}
	return Policy{
		AllowedOrigins:       []string{origin(parsed)},
		AllowPrivateNetworks: allowPrivate,
		Budget:               budget,
	}, nil
}
