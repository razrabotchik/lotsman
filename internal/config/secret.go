package config

import (
	"os"
	"strings"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// Secret reference schemes (FR-60). `keyring:` is deferred to the portability
// spike in feature 004; it is listed here so the error message can name it as
// "not yet" rather than "never", which is a different thing to an operator.
const (
	schemeEnv     = "env:"
	schemeFile    = "file:"
	schemeKeyring = "keyring:"
)

// SecretRef points at a secret without being one. It is safe to print.
type SecretRef string

// ParseSecretRef validates a reference and refuses anything that looks like a
// literal secret.
//
// The refusal is the point: an operator who pastes a token into a flag or a
// config file has put it in their shell history, their process table and
// their version control, and no amount of redaction downstream takes it back
// out (FR-59).
func ParseSecretRef(value string) (SecretRef, error) {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "":
		return "", errs.Errorf(errs.ClassUsage, "secret reference is empty")
	case strings.HasPrefix(trimmed, schemeEnv):
		if strings.TrimPrefix(trimmed, schemeEnv) == "" {
			return "", errs.Errorf(errs.ClassUsage, "secret reference %q names no environment variable", trimmed)
		}
		return SecretRef(trimmed), nil
	case strings.HasPrefix(trimmed, schemeFile):
		if strings.TrimPrefix(trimmed, schemeFile) == "" {
			return "", errs.Errorf(errs.ClassUsage, "secret reference %q names no file", trimmed)
		}
		return SecretRef(trimmed), nil
	case strings.HasPrefix(trimmed, schemeKeyring):
		return "", errs.Errorf(errs.ClassUsage,
			"secret reference %q uses keyring:, which is not implemented yet; use env: or file:", trimmed)
	default:
		// The value itself is never echoed: it may be the secret.
		return "", errs.Errorf(errs.ClassUsage,
			"a secret must be a reference, not a value: use env:NAME or file:/path (%d characters given)", len(trimmed))
	}
}

// Resolve reads the value behind the reference. It is called by a provider at
// the moment the credential is needed, never at load time, so a configuration
// can be checked on a machine that holds none of the secrets.
func (r SecretRef) Resolve() (string, error) {
	switch {
	case strings.HasPrefix(string(r), schemeEnv):
		name := strings.TrimPrefix(string(r), schemeEnv)
		value, ok := os.LookupEnv(name)
		if !ok {
			return "", errs.Errorf(errs.ClassAuth, "environment variable %s is not set", name)
		}
		if value == "" {
			return "", errs.Errorf(errs.ClassAuth, "environment variable %s is empty", name)
		}
		return value, nil

	case strings.HasPrefix(string(r), schemeFile):
		path := strings.TrimPrefix(string(r), schemeFile)
		// #nosec G304 -- the path comes from the operator's own configuration,
		// which is the documented way to point at a secret file.
		data, err := os.ReadFile(path)
		if err != nil {
			// The error is reported without the file's contents, which is the
			// one thing an unreadable secret file must not put in a log.
			return "", errs.Errorf(errs.ClassAuth, "secret file %s cannot be read", path)
		}
		// A trailing newline is what every editor and `echo` adds; sending it
		// as part of a token is a support ticket waiting to happen.
		value := strings.TrimRight(string(data), "\r\n")
		if value == "" {
			return "", errs.Errorf(errs.ClassAuth, "secret file %s is empty", path)
		}
		return value, nil

	default:
		return "", errs.Errorf(errs.ClassUsage, "unsupported secret reference")
	}
}

// String makes the reference safe to print by construction.
func (r SecretRef) String() string { return string(r) }
