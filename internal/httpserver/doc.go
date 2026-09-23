// Package httpserver serves the catalog over the MCP stateless Streamable
// HTTP profile (FR-69).
//
// A listening socket is a different thing from a pipe to a parent process.
// Until this package existed, nothing could reach lotsman that was not already
// on the machine, and every security decision in the codebase was written
// under that assumption. So everything here is either a guard or a shutdown,
// the guards run before the handler rather than inside it, and none of them
// can be reached around: a request that fails one never becomes a tool call.
//
// The transport decides nothing about what a call may do. Effect policy,
// operator rules, overrides and the approval prompt were all settled before a
// transport existed, and a behaviour that differs between stdio and HTTP is a
// bug in the server rather than a property of the protocol.
package httpserver
