// Package audit defines audit events and their sinks (stderr in M1; storage
// sinks deferred).
//
// Events carry operation key, effect, decision, origin without query, status,
// timing and sizes — never secrets (Constitution VI).
package audit
