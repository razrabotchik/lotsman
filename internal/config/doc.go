// Package config loads and merges configuration: defaults < file < env < flags.
//
// It holds SecretRef values ("env:NAME", "file:/path") only — never literal
// secrets (Constitution VI).
package config
