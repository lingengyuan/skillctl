//go:build integration

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationGitLifecycle(t *testing.T) {
	home := setTestHome(t)
	remote := filepath.Join(home, "remote.git")
	seed := filepath.Join(home, "seed")
	worktree := filepath.Join(home, "worktree")
	runTestGit(t, home, "init", "--bare", remote)
	runTestGit(t, home, "clone", remote, seed)
	writeTestSkill(t, filepath.Join(seed, "repo-skill"), "repo-skill", "old")
	writeTestSkill(t, filepath.Join(seed, "tracked-skill"), "tracked-skill", "old")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, seed, "push", "-u", "origin", "HEAD")
	runTestGit(t, home, "clone", remote, worktree)

	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "tracked-skill")
	if err := copyDirectory(filepath.Join(worktree, "tracked-skill"), installedSkill); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	trackArgs := []string{"track", "--timeout", "60s", "--path", installedRoot, "--source", remote, "--skill-path", "tracked-skill", "tracked-skill"}
	if code := run(trackArgs, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "tracked-skill: tracked") {
		t.Fatalf("track failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	if err := os.WriteFile(filepath.Join(seed, "repo-skill", "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "tracked-skill", "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "update")
	runTestGit(t, seed, "push")

	stdout.Reset()
	stderr.Reset()
	paths := []string{"--path", worktree, "--path", installedRoot}
	checkArgs := append([]string{"check", "--timeout", "60s"}, paths...)
	if code := run(checkArgs, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "repo-skill [git-worktree, repository]: update available") || !strings.Contains(stdout.String(), "[skillctl-track-v1, skillctl]: update available") {
		t.Fatalf("check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	updateArgs := append([]string{"update", "--timeout", "60s"}, paths...)
	if code := run(updateArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("update failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, path := range []string{filepath.Join(worktree, "repo-skill", "new.txt"), filepath.Join(installedSkill, "new.txt")} {
		if content, err := os.ReadFile(path); err != nil || string(content) != "new" {
			t.Fatalf("updated file %s: %q, %v", path, content, err)
		}
	}
}

func TestIntegrationHistoryLifecycle(t *testing.T) {
	home := setTestHome(t)
	remote := filepath.Join(home, "history-remote.git")
	seed := filepath.Join(home, "history-seed")
	runTestGit(t, home, "init", "--bare", remote)
	runTestGit(t, home, "clone", remote, seed)
	writeTestSkill(t, filepath.Join(seed, "skills", "history-skill"), "history-skill", "old")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, seed, "push", "-u", "origin", "HEAD")

	gitConfig := filepath.Join(home, "gitconfig")
	runTestGit(t, home, "config", "--file", gitConfig, "url."+remote+".insteadOf", "https://github.com/test/history.git")
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "history-skill")
	if err := copyDirectory(filepath.Join(seed, "skills", "history-skill"), installedSkill); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(home, ".claude", "projects", "fixture")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"python3 /tmp/install-skill-from-github.py --repo test/history --path skills/history-skill"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "session.jsonl"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"track", "--timeout", "60s", "--path", installedRoot, "--from-history", "history-skill"}
	if code := run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "tracked from install history") {
		t.Fatalf("history track failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	if err := os.WriteFile(filepath.Join(seed, "skills", "history-skill", "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "update")
	runTestGit(t, seed, "push")

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check", "--timeout", "60s", "--path", installedRoot, "history-skill"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "update available") {
		t.Fatalf("history check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"update", "--timeout", "60s", "--path", installedRoot, "history-skill"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("history update failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if content, err := os.ReadFile(filepath.Join(installedSkill, "new.txt")); err != nil || string(content) != "new" {
		t.Fatalf("history skill was not updated: %q, %v", content, err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=skillctl test",
		"GIT_AUTHOR_EMAIL=skillctl@example.invalid",
		"GIT_COMMITTER_NAME=skillctl test",
		"GIT_COMMITTER_EMAIL=skillctl@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
