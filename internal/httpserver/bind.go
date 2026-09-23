package httpserver

import (
	"net"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// checkBind refuses a public bind that nothing authenticates (FR-70).
//
// It refuses at startup rather than per request on purpose: a deployment that
// comes up and then denies everything looks like an outage, and one that will
// not come up looks like what it is.
func checkBind(listen string, authenticated, optIn bool) error {
	loopback, err := isLoopback(listen)
	if err != nil {
		return err
	}
	if loopback || authenticated || optIn {
		return nil
	}
	return errs.Errorf(errs.ClassUsage,
		"server: %s is not a loopback address and nothing authenticates the endpoint; "+
			"configure inbound authentication or pass --allow-unauthenticated-public-bind (FR-70)",
		listen)
}

// isLoopback reports whether the bind address can only be reached from this
// machine.
//
// A hostname that is not literally "localhost" counts as public even if it
// happens to resolve to 127.0.0.1 today. That is deliberate: resolving names
// at startup would make the safety of a configuration depend on a DNS answer,
// and the answer can change after the check without the process noticing.
// Refusing a name lotsman cannot reason about costs an operator one explicit
// address; trusting one costs them the guard.
func isLoopback(listen string) (bool, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, errs.Errorf(errs.ClassUsage, "server: listen address %q is not host:port", listen)
	}
	switch host {
	case "":
		// ":8080" is every interface, which is the opposite of loopback even
		// though it looks like an omission.
		return false, nil
	case "localhost":
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}
