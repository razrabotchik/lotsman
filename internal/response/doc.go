// Package response shapes an upstream HTTP response into an MCP tool result
// (pipeline stage 8).
//
// Bounded reads, truncation that never emits broken JSON as JSON, header
// allowlist, isError mapping.
package response
