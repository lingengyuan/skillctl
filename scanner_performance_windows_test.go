//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestScannerWithManyWindowsJunctionAliasesCompletesWithinFiveSeconds(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "canonical")
	aliases := filepath.Join(dir, "aliases")
	if err := os.MkdirAll(aliases, 0o755); err != nil {
		t.Fatal(err)
	}
	const count = 48
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		target := filepath.Join(canonical, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: %s\ndescription: performance fixture\n---\n", name)
		if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(aliases, name)
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	}
	started := time.Now()
	items, failed := scan([]scanRoot{{Path: canonical}, {Path: aliases}}, false, io.Discard)
	elapsed := time.Since(started)
	if failed || len(items) != count {
		t.Fatalf("items=%d failed=%t", len(items), failed)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("junction alias scan took %s, want less than 5s", elapsed.Round(time.Millisecond))
	}
}
