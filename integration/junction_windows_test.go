//go:build windows

package integration_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScannerFollowsWindowsJunctionWithoutAdministrator(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "target", "junction-skill")
	mustMkdirAll(t, target)
	mustWrite(t, filepath.Join(target, "SKILL.md"), "---\nname: junction-skill\ndescription: Junction skill\n---\n")
	root := filepath.Join(home, "scan")
	mustMkdirAll(t, root)
	link := filepath.Join(root, "junction-skill")
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("directory junction is unavailable: %v: %s", err, output)
	}

	result := runSkillctl(t, home, "check", "--path", root)
	if result.exitCode != 0 || !strings.Contains(result.output, "junction-skill: unmanaged (source unknown)") {
		t.Fatalf("unexpected junction skill result (%d):\n%s", result.exitCode, result.output)
	}
}
