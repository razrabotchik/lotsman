// Package egress guards outbound network access: origin checks, redirect
// denial, transport limits (pipeline stage 7).
//
// Fail-closed by default (Constitution II); the full policy grows in feature 003.
package egress
