//go:build !windows

package fileinput

import (
	"errors"
	"os"
	"syscall"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// openRegular opens a path that has already been resolved and found inside a
// root, and decides on the descriptor what the string could only suggest.
//
// O_NOFOLLOW is the part a check on the path cannot do: between EvalSymlinks
// and this open, the final component can be replaced by a symlink pointing
// anywhere. With it, that swap fails the open instead of redirecting the read.
// The remaining window -- an intermediate directory replaced in the same
// moment -- needs openat2(RESOLVE_BENEATH) to close and is an accepted
// residual risk (ADR-0022).
func openRegular(resolved, argument string) (*os.File, int64, error) {
	file, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		switch {
		case isSymlinkRefusal(err):
			return nil, 0, refuse(domain.ReasonFileInputNotRegularFile, argument, errs.ClassPolicy,
				"the path became a symbolic link while it was being opened")
		case os.IsNotExist(err):
			return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
				"no such file")
		case os.IsPermission(err):
			return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
				"the file cannot be read")
		default:
			return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
				"the file cannot be opened")
		}
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()

	// fstat, not stat: the question is what this descriptor points at, and
	// nothing about the name can answer it any more.
	info, err := file.Stat()
	if err != nil {
		return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file cannot be inspected")
	}
	if !info.Mode().IsRegular() {
		// A directory, FIFO, socket, device or anything under /proc ends
		// here, by what it is rather than by where it lives: a path pattern
		// would be a list of the ones we thought of.
		return nil, 0, refuse(domain.ReasonFileInputNotRegularFile, argument, errs.ClassPolicy,
			"the path is a %s, not a regular file", describeMode(info.Mode()))
	}
	// O_NONBLOCK kept the open from hanging on a FIFO; a regular file does
	// not need it, and leaving it set would make a short read look like EOF.
	if err := clearNonblock(file); err != nil {
		return nil, 0, refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file cannot be read in blocking mode")
	}
	ok = true
	return file, info.Size(), nil
}

func isSymlinkRefusal(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// Linux answers ELOOP, the BSDs (and macOS) answer EMLINK or EFTYPE.
	return errno == syscall.ELOOP || errno == syscall.EMLINK
}

func clearNonblock(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	err = raw.Control(func(fd uintptr) {
		setErr = syscall.SetNonblock(int(fd), false)
	})
	if err != nil {
		return err
	}
	return setErr
}

func describeMode(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeCharDevice != 0:
		return "character device"
	default:
		return "special file"
	}
}
