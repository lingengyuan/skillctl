//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func identifyDirectory(path string) (string, string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	if absolute, absErr := filepath.Abs(canonical); absErr == nil {
		canonical = absolute
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return "", "", err
	}
	key := fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	return key, canonical, nil
}
