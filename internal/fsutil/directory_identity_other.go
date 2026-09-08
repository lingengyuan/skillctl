//go:build !windows

package fsutil

import (
	"path/filepath"
)

// IdentifyDirectory returns a stable directory identity and its resolved path, including junctions on Windows.
func IdentifyDirectory(path string) (string, string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	if absolute, absErr := filepath.Abs(canonical); absErr == nil {
		canonical = absolute
	}
	return PathKey(canonical), canonical, nil
}
