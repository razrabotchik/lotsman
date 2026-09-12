// Package auth presents credentials to the upstream API.
//
// It is the last thing to touch a request before the wire (pipeline stage 7):
// every other layer -- logging, policy, egress -- sees the request without the
// credential in it, which is what keeps a token out of a trace by construction
// rather than by remembering to redact it.
package auth
