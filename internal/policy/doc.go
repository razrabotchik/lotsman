// Package policy decides what an operation does to the outside world and
// whether it may do it (pipeline stage 3.6 and the runtime gate).
//
// It is the security boundary for mutations: tool annotations are hints to a
// client and never an input here (Constitution III, FR-43). Doubt resolves to
// refusal -- an effect that cannot be determined is not a read.
package policy
