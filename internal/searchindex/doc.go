// Package searchindex ranks operations for a query, locally and
// deterministically.
//
// It exists because one tool per operation stops working above a certain
// catalog: 65 Kubernetes tools serialize to 2.2 MB of tool definitions, which
// is a model's whole context spent on a menu. Search mode publishes five tools
// instead, and this package is what makes the first of them useful.
//
// Two properties matter more than ranking quality. The index is built once per
// immutable catalog and never mutated, so a snapshot cannot change under a
// session. And scoring is fully deterministic, tie-breaks included: the same
// catalog and query produce the same order on every machine, which is what
// makes a recall benchmark meaningful at all.
package searchindex
