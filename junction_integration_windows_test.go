//go:build windows && integration

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIntegrationWindowsJunction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target", "junction-skill")
	writeTestSkill(t, target, "junction-skill", "junction")
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "junction-skill")
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("junction is unavailable: %v: %s", err, output)
	}
	items, failed := scan([]scanRoot{{Path: target}, {Path: root}}, false, io.Discard)
	if failed || len(items) != 1 {
		t.Fatalf("items=%#v failed=%v", items, failed)
	}
	found := false
	for _, alias := range items[0].Aliases {
		if samePath(alias, link) {
			found = true
		}
	}
	if !found {
		t.Fatalf("junction alias missing: %#v", items[0])
	}
}
