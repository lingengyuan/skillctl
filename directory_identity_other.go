//go:build !windows

package main

import "path/filepath"

func identifyDirectory(path string) (string, string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	if absolute, absErr := filepath.Abs(canonical); absErr == nil {
		canonical = absolute
	}
	return canonicalPathKey(canonical), canonical, nil
}
