// Package fileinput opens a local file named by an untrusted tool argument,
// inside the roots and limits an operator configured (feature 009, ADR-0022).
//
// A path arrives in tool arguments, which are untrusted (docs/spec.md §6.1,
// Constitution V): a prompt injection in an upstream response can propose
// `~/.ssh/id_rsa`. The boundary is therefore the operator's configuration, and
// every decision is made on the open file descriptor rather than on the string:
// the path is validated, resolved, checked for containment component by
// component, opened without following a final symlink, and then measured and
// typed through that descriptor. A refusal carries its own reason code and
// happens before the network.
//
// The package knows nothing about OpenAPI, configuration structs or HTTP
// (Constitution VIII); it is also the seam a multi-tenant deployment will use
// to hand out per-client roots instead of the process-wide ones.
package fileinput
