//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernelLockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	kernelUnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func lockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := kernelLockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}

func unlockFile(file *os.File) {
	var overlapped syscall.Overlapped
	_, _, _ = kernelUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
}
