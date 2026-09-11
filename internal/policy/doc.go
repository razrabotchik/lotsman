// Package policy decides whether a call may proceed: effect classification and
// the read-only gate (pipeline stage 5).
//
// Constitution III: server-side policy is the only security boundary — tool
// annotations and client-side approval are hints, never enforcement.
package policy
