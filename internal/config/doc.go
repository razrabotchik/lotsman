// Package config loads lotsman's runtime configuration and the secret
// references it points at.
//
// It never holds a secret: a configuration carries `env:NAME` or `file:/path`
// references, and the value behind one is read at the moment a provider needs
// it (FR-59/60). That is why this package can be logged, exported and diffed
// without redaction, and why `config export` is not a way to leak a token.
package config
