package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestCheckVercelGitSourceUsesWorktree(t *testing.T) {
	requireTestGit(t)
	setTestHome(t)
	source := newTestSourceRepository(t)
	remote := filepath.Join(source, "skills", "demo")
	writeTestSkill(t, remote, "demo", "current")
	commitTestSource(t, source, "initial")

	installed := filepath.Join(t.TempDir(), "demo")
	if err := fsutil.CopyDirectory(remote, installed); err != nil {
		t.Fatal(err)
	}
	wantHash, err := fsutil.HashDirectory(installed)
	if err != nil {
		t.Fatal(err)
	}

	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	available, drift, err := checkVercelEntry(session, vercelLockEntry{
		SourceType:      "git",
		SourceURL:       source,
		Ref:             "main",
		SkillPath:       "skills/demo/SKILL.md",
		SkillFolderHash: wantHash,
	}, installed)
	if err != nil {
		t.Fatal(err)
	}
	if available || drift != "clean" {
		t.Fatalf("available=%t drift=%s", available, drift)
	}
}

func TestGitTreeHashMatchesDirectoryHashForBinaryCRLF(t *testing.T) {
	requireTestGit(t)
	source := newTestSourceRepository(t)
	skillDir := filepath.Join(source, "skills", "demo")
	writeTestSkill(t, skillDir, "demo", "binary")
	if err := os.WriteFile(filepath.Join(skillDir, "asset.bin"), []byte{0xff, '\r', '\n', 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	commitTestSource(t, source, "binary")

	tree := runHardeningGit(t, source, "rev-parse", "HEAD:skills/demo")
	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	got, err := hashGitTree(session, source, tree)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fsutil.HashDirectory(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Git tree hash = %s, directory hash = %s", got, want)
	}
}

func requireTestGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func newTestSourceRepository(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runHardeningGit(t, source, "init")
	runHardeningGit(t, source, "config", "user.name", "skillctl test")
	runHardeningGit(t, source, "config", "user.email", "skillctl@example.invalid")
	runHardeningGit(t, source, "config", "core.autocrlf", "false")
	runHardeningGit(t, source, "branch", "-M", "main")
	return source
}

func commitTestSource(t *testing.T, source, message string) {
	t.Helper()
	runHardeningGit(t, source, "add", ".")
	runHardeningGit(t, source, "commit", "-m", message)
}
