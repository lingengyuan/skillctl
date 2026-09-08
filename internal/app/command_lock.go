package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type commandLock struct {
	path string
	file *os.File
}

func acquireCommandLock() (*commandLock, error) {
	dir, err := skillctlDirectory()
	if err != nil {
		return nil, fmt.Errorf("find user config directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create command lock directory: %w", err)
	}
	path := filepath.Join(dir, "operation.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open operation lock: %w", err)
	}
	if err := fsutil.TryLock(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another skillctl operation is running (%s): %w", path, err)
	}
	lock := &commandLock{path: path, file: file}
	if err := file.Truncate(0); err != nil {
		lock.release()
		return nil, err
	}
	if _, err := fmt.Fprintf(file, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		lock.release()
		return nil, err
	}
	return lock, nil
}

func (l *commandLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		fsutil.Unlock(l.file)
		_ = l.file.Close()
		l.file = nil
	}
	// Never unlink: another process may already have opened this inode. Kernel
	// locks release automatically on exit, so interrupted commands do not leave
	// a stale lock requiring a time-based deletion.
}

func operationLockActive(path string) bool {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	if err := fsutil.TryLock(file); err != nil {
		return true
	}
	fsutil.Unlock(file)
	return false
}
