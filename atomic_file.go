package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceFile replaces target with temp. os.Rename is atomic on platforms that
// support replacing an existing file. The fallback keeps a recoverable backup
// for platforms, notably Windows, where replacing an existing destination may
// fail.
func replaceFile(temp, target string) error {
	if err := os.Rename(temp, target); err == nil {
		return nil
	}

	backupFile, err := os.CreateTemp(filepath.Dir(target), ".skillctl-replace-*")
	if err != nil {
		return err
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backup)
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}

	movedOriginal := false
	if err := os.Rename(target, backup); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		movedOriginal = true
	}

	if err := os.Rename(temp, target); err != nil {
		if movedOriginal {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return fmt.Errorf("replace file: %w; restore original: %v", err, restoreErr)
			}
		}
		return err
	}
	if movedOriginal {
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove replaced file backup: %w", err)
		}
	}
	return nil
}
