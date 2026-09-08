package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestCheckDoesNotRecoverInterruptedInstallation(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	root := filepath.Join(home, "installed")
	directory := filepath.Join(root, "demo")
	writeTestSkill(t, directory, "demo", "before")
	file := filepath.Join(directory, "SKILL.md")
	change, err := mutation(file, "file")
	if err != nil {
		t.Fatal(err)
	}
	change.Data = []byte("replacement")
	operation, err := prepareOperation("fixture interrupted update", []pathMutation{change}, []string{directory})
	if err != nil {
		t.Fatal(err)
	}
	operation.State = "applying"
	if err := operation.save(); err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, directory, "demo", "partial state")
	before, _ := os.ReadFile(file)
	var out, errout strings.Builder
	if code := run([]string{"check", "--path", root, "--no-history", "--json-version", "2"}, &out, &errout); code != 1 {
		t.Fatalf("code=%d %s %s", code, &out, &errout)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) || !strings.Contains(out.String(), "recovery_required") || !strings.Contains(out.String(), "indeterminate") {
		t.Fatalf("check recovered or misreported partial content: %s", &out)
	}
	history, err := readOperations()
	if err != nil || len(history) != 1 || history[0].State != "applying" {
		t.Fatalf("check altered journal: %+v %v", history, err)
	}
}

func TestCheckObservationDoesNotOverwriteConcurrentSource(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	directory := filepath.Join(home, "installed", "demo")
	writeTestSkill(t, directory, "demo", "content")
	opt := options{}
	opt.Paths = []string{filepath.Dir(directory)}
	view, err := loadCheckInventory(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := fsutil.HashDirectory(directory)
	view.state.put(trackedEntry{Path: directory, Source: "https://example.invalid/observed.git", SkillPath: "demo", InstalledHash: digest})
	concurrent, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	concurrent.put(trackedEntry{Path: directory, Source: "https://example.invalid/newer.git", SkillPath: "demo", InstalledHash: digest})
	if err := concurrent.save(); err != nil {
		t.Fatal(err)
	}
	diagnostics := commitCheckObservations(t.Context(), view)
	current, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := current.find(directory)
	if entry.Source != "https://example.invalid/newer.git" || len(diagnostics) == 0 {
		t.Fatalf("overwrote newer state: %+v %v", entry, diagnostics)
	}
}

func TestSourceSnapshotSurvivesHEADChangeAndHistoryVerification(t *testing.T) {
	source := newTestSourceRepository(t)
	directory := filepath.Join(source, "demo")
	writeTestSkill(t, directory, "demo", "old")
	commitTestSource(t, source, "old")
	oldDigest, _ := fsutil.HashDirectory(directory)
	session := newSourceSession(t.Context(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	old, err := session.gitObject(source, "HEAD:demo")
	if err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, directory, "demo", "new")
	commitTestSource(t, source, "new")
	current, err := session.gitObject(source, "HEAD:demo")
	if err != nil || current.Hash != old.Hash {
		t.Fatalf("snapshot changed: %s %s %v", old.Hash, current.Hash, err)
	}
	head := runHardeningGit(t, source, "rev-parse", "HEAD")
	matched, err := matchesSourceHistory(source, "demo", oldDigest)
	if err != nil || !matched {
		t.Fatalf("historical copy not verified: %v %v", matched, err)
	}
	if after := runHardeningGit(t, source, "rev-parse", "HEAD"); after != head {
		t.Fatal("history verification changed HEAD")
	}
	data, _ := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if !strings.Contains(string(data), "new") {
		t.Fatal("history verification changed worktree")
	}
}

func TestGitObjectReadHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	session := newSourceSession(ctx, defaultNetworkTimeout, io.Discard)
	defer session.close()
	if _, err := session.gitObject("unused", "HEAD:demo"); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}
