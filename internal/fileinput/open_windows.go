//go:build windows

package fileinput

import (
	"os"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// openRegular opens a path already resolved and found inside a root.
//
// Windows has no O_NOFOLLOW, so the final component is checked with Lstat
// before the open and the descriptor is re-checked after it. That is a check
// with a window where the Unix path has none; recorded as a platform
// difference in ADR-0022 and in the README capability matrix.
func openRegular(resolved, argument string) (*os.File, int64, error) {
	link, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
				"no such file")
		}
		return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file cannot be inspected")
	}
	if link.Mode()&os.ModeSymlink != 0 || link.Mode()&os.ModeIrregular != 0 {
		return nil, 0, refuse(domain.ReasonFileInputNotRegularFile, argument, errs.ClassPolicy,
			"the path is a link or reparse point, not a regular file")
	}

	file, err := os.Open(resolved)
	if err != nil {
		if os.IsPermission(err) {
			return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
				"the file cannot be read")
		}
		return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file cannot be opened")
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file cannot be inspected")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, refuse(domain.ReasonFileInputNotRegularFile, argument, errs.ClassPolicy,
			"the path is not a regular file")
	}
	ok = true
	return file, info.Size(), nil
}
