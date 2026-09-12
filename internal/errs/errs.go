package errs

import (
	"errors"
	"fmt"
)

// Class is the stable, machine-readable failure taxonomy. The set is closed:
// adding a member is a contract change because T034 maps these onto CLI exit
// codes (contracts/cli.md) and MCP error prefixes.
type Class string

const (
	// ClassInternal is an I/O or internal runtime failure: lotsman itself
	// could not do its job. It is the default for unclassified errors, so a
	// forgotten annotation degrades to "our fault", never to "safe".
	ClassInternal Class = "internal"

	// ClassUsage is an operator mistake: bad flags, bad config values.
	ClassUsage Class = "usage"

	// ClassSpecInvalid is a document that cannot be trusted or parsed: bad
	// YAML/JSON, structural OpenAPI errors, or a document that blew a parse
	// budget (an alias bomb is not a valid spec, it is an attack).
	ClassSpecInvalid Class = "spec_invalid"

	// ClassUnsupported is a well-formed document lotsman refuses to translate
	// or execute under the selected strictness: rejected operations, runtime
	// execution blockers, limits that a valid-but-huge spec exceeds.
	// Principle I lives here — refusal is a normal, expected outcome.
	ClassUnsupported Class = "unsupported"

	// ClassPolicy is a refusal by a security policy at call time: egress
	// denial, mutation gate, effect policy. Distinct from ClassUnsupported
	// because the operation is translatable — policy said no.
	ClassPolicy Class = "policy"

	// ClassAuth is a failure to resolve or apply credentials (secretRef
	// resolution, missing auth profile). Reserved for T029-T031; kept in the
	// taxonomy now so the exit-code table stays stable.
	ClassAuth Class = "auth"

	// ClassUpstream is a transport-level failure talking to the target API.
	// An HTTP response with a 4xx/5xx status is NOT this class: that is a
	// successful call reported as isError (FR-39).
	ClassUpstream Class = "upstream"
)

// classed is the (unexported) contract an error implements to carry a Class.
// Unexported so the only way to produce one is through this package and the
// taxonomy cannot be extended from outside.
type classed interface {
	error
	errorClass() Class
}

type classedError struct {
	class Class
	err   error
}

func (e *classedError) Error() string     { return e.err.Error() }
func (e *classedError) Unwrap() error     { return e.err }
func (e *classedError) errorClass() Class { return e.class }

// Errorf formats an error and tags it with class. It behaves exactly like
// fmt.Errorf, including %w wrapping.
func Errorf(class Class, format string, args ...any) error {
	return &classedError{class: class, err: fmt.Errorf(format, args...)}
}

// Wrap tags an existing error with class, preserving the chain for
// errors.Is/As. It returns nil for a nil error so callers can write
// `return errs.Wrap(class, f())`.
func Wrap(class Class, err error) error {
	if err == nil {
		return nil
	}
	return &classedError{class: class, err: err}
}

// ClassOf reports the class of err. The outermost classification wins: a
// caller that re-tags a wrapped error is stating the class in its own terms,
// which is what a boundary (CLI, MCP handler) should report.
//
// An unclassified error is ClassInternal, not "unknown": if no layer claimed
// it, lotsman does not understand it, and that is a bug in lotsman.
func ClassOf(err error) Class {
	if err == nil {
		return ""
	}
	var c classed
	if errors.As(err, &c) {
		return c.errorClass()
	}
	return ClassInternal
}

// HasClass reports whether err classifies as class.
func HasClass(err error, class Class) bool {
	return err != nil && ClassOf(err) == class
}
