package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderCheckCompletesWithinTenSeconds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	gitTest(t, dir, "init", "--bare", remote)
	gitTest(t, dir, "clone", remote, seed)
	gitTest(t, seed, "config", "user.email", "test@example.invalid")
	gitTest(t, seed, "config", "user.name", "test")

	sourceSkill := filepath.Join(seed, "skills", "performance-skill")
	if err := os.MkdirAll(sourceSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: performance-skill\ndescription: performance fixture\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 45; i++ {
		name := filepath.Join(sourceSkill, fmt.Sprintf("file-%02d.txt", i))
		if err := os.WriteFile(name, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "fixture")
	gitTest(t, seed, "push", "-u", "origin", "HEAD")

	installedRoot := filepath.Join(dir, "installed")
	installedSkill := filepath.Join(installedRoot, "performance-skill")
	copyTestDirectory(t, sourceSkill, installedSkill)
	lockedTree := gitTest(t, seed, "rev-parse", "HEAD:skills/performance-skill")
	lock := vercelLock{Version: 3, Skills: map[string]vercelLockEntry{
		"performance-skill": {
			SourceType:      "github",
			SourceURL:       remote,
			SkillPath:       "skills/performance-skill/SKILL.md",
			SkillFolderHash: lockedTree,
		},
	}}
	lockContent, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	if err := os.WriteFile(lockPath, lockContent, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(installedRoot), filepath.ToSlash(lockPath), filepath.ToSlash(installedRoot))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	started := time.Now()
	code := run([]string{"check", "--config", configPath, "performance-skill"}, &stdout, &stderr)
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("provider check took %s, want less than 10s", elapsed.Round(time.Millisecond))
	}
}

func TestUnmanagedSkillsCompleteWithinTenSeconds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	root := filepath.Join(dir, "skills")
	for i := 0; i < 40; i++ {
		skillDir := filepath.Join(root, fmt.Sprintf("unmanaged-%02d", i))
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: unmanaged-%02d\ndescription: performance fixture\n---\n", i)
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	started := time.Now()
	code := run([]string{"check", "--path", root}, &stdout, &stderr)
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("unmanaged check took %s, want less than 10s", elapsed.Round(time.Millisecond))
	}
}
