package fileinput

import (
	"errors"
	"fmt"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// Error is a refusal to read a file, with the code a client branches on.
//
// The message quotes the caller's own path and, where it helps, the roots that
// were allowed: a model that cannot see why its path was refused cannot
// correct it and will guess instead (ADR-0022 decision 8). Logs and audit
// events take the basename from the File instead, never this.
type Error struct {
	Code domain.ReasonCode
	// Path is the argument as the caller wrote it, empty when the argument
	// was not safe to quote (a NUL byte).
	Path    string
	Message string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("file input: %s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("file input %q: %s (%s)", e.Path, e.Message, e.Code)
}

// ReasonOf reports the file-input reason code an error carries, or "".
func ReasonOf(err error) domain.ReasonCode {
	var fileErr *Error
	if errors.As(err, &fileErr) {
		return fileErr.Code
	}
	return ""
}

// refuse builds a classed refusal. The class is what the taxonomy already
// says: a limit or a root is the operator's policy, a malformed argument or a
// missing file is the caller's mistake.
func refuse(code domain.ReasonCode, path string, class errs.Class, format string, args ...any) error {
	return errs.Wrap(class, &Error{
		Code:    code,
		Path:    path,
		Message: fmt.Sprintf(format, args...),
	})
}
