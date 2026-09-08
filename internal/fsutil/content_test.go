package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashAndCopyIgnoreRepositoryMetadata(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: root-skill\ndescription: root\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "objects", "noise"), []byte("changes every fetch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".skillctl-stage-old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".skillctl-stage-old", "noise"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := HashDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := CopyDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".skillctl-stage-old")); !os.IsNotExist(err) {
		t.Fatalf("skillctl transaction directory was copied: %v", err)
	}
	after, err := HashDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("hash changed after safe copy: %s != %s", before, after)
	}
}
