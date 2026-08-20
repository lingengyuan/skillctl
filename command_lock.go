package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const commandLockStaleAfter = 24 * time.Hour

type commandLock struct {
	path string
	file *os.File
}

func acquireCommandLock() (*commandLock, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find user config directory: %w", err)
	}
	dir = filepath.Join(dir, "skillctl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create command lock directory: %w", err)
	}
	path := filepath.Join(dir, "operation.lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			return &commandLock{path: path, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire operation lock: %w", err)
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > commandLockStaleAfter {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		return nil, fmt.Errorf("another skillctl update or track operation is already running (%s)", path)
	}
	return nil, fmt.Errorf("could not acquire operation lock: %s", path)
}

func (l *commandLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	_ = os.Remove(l.path)
}
