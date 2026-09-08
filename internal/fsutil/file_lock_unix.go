//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package fsutil

import (
	"errors"
	"os"
	"syscall"
)

// LockBusy reports whether another owner holds the requested kernel lock.
func LockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// ProcessRunning checks process existence conservatively, treating inaccessible processes as active.
func ProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TryLock attempts a nonblocking exclusive kernel lock. Keep the file open and do not unlink its path.
func TryLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// Unlock releases a kernel lock held by the file.
func Unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
