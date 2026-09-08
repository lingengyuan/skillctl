//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
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
	if err := fsutil.CopyDirectory(filepath.Join(worktree, "tracked-skill"), installedSkill); err != nil {
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
	if code := run(checkArgs, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "repo-skill [git-worktree, repository]: update available") || !strings.Contains(stdout.String(), "tracked-skill [multiple, multiple]: update available (multiple installations)") {
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
	if err := fsutil.CopyDirectory(filepath.Join(seed, "skills", "history-skill"), installedSkill); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(home, ".claude", "projects", "fixture")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("npx --yes skills add test/history --skill history-skill --dir %q -y", installedRoot)
	recordData, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Bash", "id": "install-claude", "input": map[string]string{"command": command}}}}})
	record := string(recordData) + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"install-claude","content":"Process exited with code 0"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "session.jsonl"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	codexSessions := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(codexSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf("const r = await tools.shell_command({command:%q}); text(r)", command)
	codexData, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "install-codex", "input": input}})
	codexRecord := string(codexData) + "\n" + `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"install-codex","output":"{\"exit_code\":0}"}}` + "\n"
	if err := os.WriteFile(filepath.Join(codexSessions, "session.jsonl"), []byte(codexRecord), 0o600); err != nil {
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

func TestIntegrationGitPreviewLeavesInstalledRepositoryUnchanged(t *testing.T) {
	home := setTestHome(t)
	remote, seed, installed := filepath.Join(home, "remote.git"), filepath.Join(home, "seed"), filepath.Join(home, "installed")
	runTestGit(t, home, "init", "--bare", remote)
	runTestGit(t, home, "clone", remote, seed)
	writeTestSkill(t, seed, "demo", "old")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "old")
	runTestGit(t, seed, "push", "-u", "origin", "HEAD")
	runTestGit(t, home, "clone", remote, installed)
	writeTestSkill(t, seed, "demo", "new")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "new")
	runTestGit(t, seed, "push")
	before, err := fingerprint(installed)
	if err != nil {
		t.Fatal(err)
	}
	result := runV2(t, "", "update", "--dry-run", "--path", installed)
	if len(result.Items) != 1 || result.Items[0].State != "outdated" {
		t.Fatalf("preview missed update: %#v", result)
	}
	after, err := fingerprint(installed)
	if err != nil || before != after {
		t.Fatalf("preview changed repository: %v", err)
	}
}

func TestIntegrationManifestGitLockAndExplicitUpdate(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	remote, seed := filepath.Join(project, "remote.git"), filepath.Join(home, "seed")
	runTestGit(t, home, "init", "--bare", remote)
	runTestGit(t, home, "clone", remote, seed)
	writeTestSkill(t, seed, "demo", "old")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "old")
	runTestGit(t, seed, "push", "-u", "origin", "HEAD")
	manifest := filepath.Join(project, "skillctl.toml")
	data := "version=1\n[[skills]]\nid='demo'\nname='demo'\nsource='./remote.git'\nkind='git'\nname='demo'\nhosts=['claude']\nscope='project'\n"
	data = strings.Replace(data, "kind='git'\nname='demo'", "kind='git'", 1)
	if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	runV2(t, "", "sync", "--file", manifest)
	_, first, err := loadEnvironment(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, seed, "demo", "new")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "new")
	runTestGit(t, seed, "push")
	runV2(t, "", "sync", "--frozen", "--file", manifest)
	_, still, err := loadEnvironment(manifest)
	if err != nil || first.Skills[0].Revision != still.Skills[0].Revision {
		t.Fatalf("frozen sync advanced: %v", err)
	}
	updated := runV2(t, "", "update", "--file", manifest)
	if updated.Operation == nil {
		t.Fatal("update did not advance lock")
	}
	_, last, err := loadEnvironment(manifest)
	if err != nil || last.Skills[0].Revision == first.Skills[0].Revision {
		t.Fatalf("update kept old lock: %v", err)
	}
	again := runV2(t, "", "sync", "--frozen", "--offline", "--file", manifest)
	if len(again.Plan.Changes) > 0 {
		t.Fatalf("frozen sync not idempotent: %#v", again.Plan)
	}
}

func TestIntegrationExistingTrackedCopyBindingsAndRollback(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	remote, seed := filepath.Join(home, "remote.git"), filepath.Join(home, "seed")
	runTestGit(t, home, "init", "--bare", remote)
	runTestGit(t, home, "clone", remote, seed)
	writeTestSkill(t, filepath.Join(seed, "demo"), "demo", "original")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, seed, "push", "-u", "origin", "HEAD")
	installed := filepath.Join(home, "codex", "demo")
	if err := fsutil.CopyDirectory(filepath.Join(seed, "demo"), installed); err != nil {
		t.Fatal(err)
	}
	runV2(t, config, "track", "demo", "--source", remote, "--skill-path", "demo")
	runV2(t, config, "enable", "demo", "--host", "claude", "--copy")
	before := runV2(t, config, "list").Items[0]
	writeTestSkill(t, filepath.Join(seed, "demo"), "demo", "new revision")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-m", "update")
	runTestGit(t, seed, "push")
	updated := runV2(t, config, "update", "demo")
	if updated.Operation == nil || updated.Items[0].Revision == before.Revision {
		t.Fatalf("missing operation/revision: %#v", updated)
	}
	for _, host := range []string{"codex", "claude"} {
		data, err := os.ReadFile(filepath.Join(home, host, "demo", "SKILL.md"))
		if err != nil || !strings.Contains(string(data), "new revision") {
			t.Fatalf("%s stale: %s %v", host, data, err)
		}
	}
	runV2(t, config, "rollback", updated.Operation.ID)
	for _, host := range []string{"codex", "claude"} {
		digest, err := fsutil.HashDirectory(filepath.Join(home, host, "demo"))
		if err != nil || digest != before.Digest {
			t.Fatalf("%s rollback: %s %v", host, digest, err)
		}
	}
}

func TestIntegrationRepositoryRollbackRestoresActualHead(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "linked"}[linked], func(t *testing.T) {
			home, _, _ := lifecycleFixture(t)
			remote, seed, checkout := filepath.Join(home, "remote.git"), filepath.Join(home, "seed"), filepath.Join(home, "checkout")
			runTestGit(t, home, "init", "--bare", remote)
			runTestGit(t, home, "clone", remote, seed)
			writeTestSkill(t, filepath.Join(seed, "demo"), "demo", "original")
			runTestGit(t, seed, "add", ".")
			runTestGit(t, seed, "commit", "-m", "initial")
			runTestGit(t, seed, "push", "-u", "origin", "HEAD")
			runTestGit(t, home, "clone", remote, checkout)
			work := checkout
			if linked {
				work = filepath.Join(home, "linked")
				runTestGit(t, checkout, "worktree", "add", "-b", "fixture", work)
				runTestGit(t, work, "branch", "--set-upstream-to=origin/HEAD")
			}
			before := runTestGit(t, work, "rev-parse", "HEAD")
			writeTestSkill(t, filepath.Join(seed, "demo"), "demo", "new")
			if err := os.WriteFile(filepath.Join(seed, "unrelated.txt"), []byte("same update unit"), 0600); err != nil {
				t.Fatal(err)
			}
			runTestGit(t, seed, "add", ".")
			runTestGit(t, seed, "commit", "-m", "update")
			runTestGit(t, seed, "push")
			updated := runV2(t, "", "update", "--path", work)
			if updated.Operation == nil || runTestGit(t, work, "rev-parse", "HEAD") == before {
				t.Fatal("repository not updated")
			}
			runV2(t, "", "rollback", updated.Operation.ID)
			if runTestGit(t, work, "rev-parse", "HEAD") != before {
				t.Fatal("rollback did not restore Git HEAD")
			}
			if _, err := os.Stat(filepath.Join(work, "unrelated.txt")); !os.IsNotExist(err) {
				t.Fatalf("whole repository not restored: %v", err)
			}
			runTestGit(t, work, "fsck", "--no-reflogs")
		})
	}
}
