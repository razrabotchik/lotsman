// Package errs defines lotsman's stable error taxonomy: a small, closed set
// of classes every failure is tagged with, independent of the package that
// produced it.
//
// It exists so the layers added after the M0 security floor (argument
// validation, effect policy, auth, egress) classify failures the same way
// from their first commit instead of being retrofitted once CLI exit codes
// and MCP error prefixes are contractual (T034, FR-77, contracts/cli.md).
//
// Constitution VII: no error framework — an error stays an ordinary error,
// wrapping and errors.Is/As keep working, and a class is one extra fact
// carried alongside.
package errs
