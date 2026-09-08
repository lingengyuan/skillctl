package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptWritesUsablePath(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	want := `path_line='export PATH="$HOME/.local/bin:$PATH"'`
	bad := `path_line='export PATH=\"$HOME/.local/bin:$PATH\"'`
	if !strings.Contains(text, want) {
		t.Fatalf("installer is missing usable PATH line %q", want)
	}
	if strings.Contains(text, bad) {
		t.Fatalf("installer still writes escaped quotes: %q", bad)
	}
}
