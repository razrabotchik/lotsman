// Package inbound authenticates callers of the HTTP transport (FR-79-85).
//
// It answers a question no other package in this codebase has had to ask.
// Everything else decides what a call may do; this decides whether there is a
// caller at all, and it runs before any of it.
//
// An inbound token is permission to talk to lotsman and nothing more. It is
// never forwarded upstream, never reused as an upstream credential and never
// widens what the operator configured (FR-83). The two audiences are
// different on purpose: a deployment where reaching the endpoint is reaching
// the API has no inbound authorization, it has a second door to the same room.
package inbound
