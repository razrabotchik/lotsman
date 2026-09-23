package inbound

import "net/http"

// challenge makes a refusal say what would satisfy it (FR-84, RFC 6750 §3).
//
// The SDK's middleware only emits `WWW-Authenticate` when it has resource
// metadata or scopes to advertise, which a shared secret has neither of. A
// bare 401 with no challenge at all is a refusal that does not name the scheme
// it wants, and a client cannot act on it.
//
// What the header must not do is explain anything further. `Bearer` tells a
// caller how to authenticate; a realm naming the deployment, or an error
// description distinguishing "no token" from "wrong token", would tell anyone
// who can reach the port something about how the endpoint is configured.
func challenge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&challenging{ResponseWriter: w}, r)
	})
}

// challenging adds the scheme to an unauthenticated response, and only to one.
type challenging struct {
	http.ResponseWriter
	written bool
}

func (c *challenging) WriteHeader(status int) {
	if !c.written {
		c.written = true
		if status == http.StatusUnauthorized && c.Header().Get("WWW-Authenticate") == "" {
			c.Header().Set("WWW-Authenticate", "Bearer")
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

// Write covers a handler that answers without calling WriteHeader first: the
// status is 200 by then, so there is nothing to challenge, but `written` must
// still be set or a later WriteHeader would rewrite headers already sent.
func (c *challenging) Write(b []byte) (int, error) {
	c.written = true
	return c.ResponseWriter.Write(b)
}
