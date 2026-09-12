// Package redact keeps resolved secret values out of everything lotsman
// writes: logs, tool results, errors and reports (FR-61, pitfall #14).
//
// It is the second line, not the first. Credentials are applied by the auth
// layer immediately before the wire precisely so that nothing upstream ever
// holds one; this package catches what a mistake elsewhere would otherwise
// print.
package redact
