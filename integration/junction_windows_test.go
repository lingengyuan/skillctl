//go:build windows

package integration_test

import (
	"encoding/json"
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

	result := runSkillctl(t, home, "check", "--json", "--path", target, "--path", root)
	if result.exitCode != 0 {
		t.Fatalf("unexpected junction skill result (%d):\n%s", result.exitCode, result.output)
	}
	var reports []struct {
		Path    string   `json:"path"`
		Aliases []string `json:"aliases"`
	}
	if err := json.Unmarshal([]byte(result.output), &reports); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result.output)
	}
	if len(reports) != 1 {
		t.Fatalf("instances=%d: %s", len(reports), result.output)
	}
	found := false
	for _, alias := range reports[0].Aliases {
		if samePathForTest(alias, link) {
			found = true
		}
	}
	if !found {
		t.Fatalf("junction alias missing: %#v", reports[0])
	}
}

func samePathForTest(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
