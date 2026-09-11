// Package domain holds the internal representation (IR) shared by the whole
// pipeline: Operation, InputModel, SecurityAlternative, EffectDecision,
// SupportStatus, Diagnostic.
//
// The IR stores the semantics needed for catalog, validation and serialization
// plus provenance — it does not mirror the OpenAPI AST
// (specs/001-core-runtime/data-model.md).
package domain
