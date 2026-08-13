//go:build windows

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopiedUpdateRollsBackWhenSourceStateCannotBeSaved(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "rollback-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "rollback-skill")
	copyDirForTest(t, filepath.Join(checkout, "rollback-skill"), installedSkill)

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "rollback-skill")
	if track.exitCode != 0 {
		t.Fatalf("track failed (%d):\n%s", track.exitCode, track.output)
	}
	statePath := filepath.Join(userConfigDir(home), "skillctl", "sources.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(seed, "rollback-skill", "new.txt"), "remote content")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "remote update")
	git(t, seed, "push")

	statePathUTF16, err := syscall.UTF16PtrFromString(statePath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(
		statePathUTF16,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("lock source state: %v", err)
	}
	t.Cleanup(func() { _ = syscall.CloseHandle(handle) })

	result := runSkillctl(t, home, "update", "--path", installedRoot)
	if result.exitCode != 1 || !strings.Contains(result.output, "failed (save source state:") {
		t.Fatalf("state save failure was not reported (%d):\n%s", result.exitCode, result.output)
	}
	if _, err := os.Stat(filepath.Join(installedSkill, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("installed skill was not rolled back after state save failure: %v", err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || string(stateAfter) != string(stateBefore) {
		t.Fatalf("source state changed after failed save: %q, %v", stateAfter, err)
	}
}
