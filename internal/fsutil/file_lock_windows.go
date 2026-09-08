//go:build windows

package fsutil

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// LockBusy reports whether another owner holds the requested kernel lock.
func LockBusy(err error) bool {
	return errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

// ProcessRunning checks process existence conservatively, treating inaccessible processes as active.
func ProcessRunning(pid int) bool {
	handle, err := syscall.OpenProcess(0x1000, false, uint32(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return !errors.Is(err, syscall.Errno(87)) // ERROR_INVALID_PARAMETER: no such PID
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	return syscall.GetExitCodeProcess(handle, &code) != nil || code == 259 // STILL_ACTIVE
}

var kernelLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

var kernelUnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")

// TryLock attempts a nonblocking exclusive kernel lock. Keep the file open and do not unlink its path.
func TryLock(file *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := kernelLockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}

// Unlock releases a kernel lock held by the file.
func Unlock(file *os.File) {
	var overlapped syscall.Overlapped
	_, _, _ = kernelUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
}
