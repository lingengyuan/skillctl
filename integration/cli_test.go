package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
	buildOut  []byte
)

func TestCheckCreatesDefaultConfigAndFindsRecursiveSkill(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, ".codex", "skills", "nested", "demo-skill")
	mustMkdirAll(t, skillDir)
	mustWrite(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: demo-skill\ndescription: Demo skill\n---\n")

	result := runSkillctl(t, home, "check")
	if result.exitCode != 0 {
		t.Fatalf("check exit code = %d, output:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "demo-skill [local-authoring, user]: local/untracked (no update source)") {
		t.Fatalf("output does not report the discovered skill:\n%s", result.output)
	}

	configPath := filepath.Join(userConfigDir(home), "skillctl", "config.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("default config was not created: %v", err)
	}
	if !strings.Contains(string(content), `"~/.codex/skills"`) {
		t.Fatalf("default config does not contain the Codex path:\n%s", content)
	}
}

func TestCheckAndUpdateAgainstLocalRemote(t *testing.T) {
	home := t.TempDir()
	remote, seed, local := createRepository(t, home, "demo-skill", "sibling-skill")

	mustWrite(t, filepath.Join(seed, "change.txt"), "remote change")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "remote change")
	git(t, seed, "push")

	check := runSkillctl(t, home, "check", "--path", local)
	if check.exitCode != 0 || !strings.Contains(check.output, "demo-skill [git-worktree, repository]: update available (behind 1 commits)") || !strings.Contains(check.output, "sibling-skill [git-worktree, repository]: update available (behind 1 commits)") {
		t.Fatalf("unexpected check result (%d):\n%s", check.exitCode, check.output)
	}

	oldHead := git(t, local, "rev-parse", "--short", "HEAD")
	update := runSkillctl(t, home, "update", "--path", local)
	newHead := git(t, local, "rev-parse", "--short", "HEAD")
	if update.exitCode != 0 || oldHead == newHead || strings.Count(update.output, "updated ("+oldHead+" -> "+newHead+")") != 2 {
		t.Fatalf("unexpected update result (%d):\n%s", update.exitCode, update.output)
	}
	_ = remote
}

func TestCheckSafelySkipsNoUpstreamAheadAndDivergedRepositories(t *testing.T) {
	t.Run("no upstream", func(t *testing.T) {
		home := t.TempDir()
		repo := filepath.Join(home, "skills")
		mustMkdirAll(t, filepath.Join(repo, "local-skill"))
		git(t, repo, "init")
		git(t, repo, "config", "user.email", "skillctl@example.test")
		git(t, repo, "config", "user.name", "Skillctl Test")
		mustWrite(t, filepath.Join(repo, "local-skill", "SKILL.md"), "---\nname: local-skill\ndescription: Local skill\n---\n")
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "initial")

		result := runSkillctl(t, home, "check", "--path", repo)
		if result.exitCode != 0 || !strings.Contains(result.output, "local-skill [git-worktree, repository]: skipped (no upstream)") {
			t.Fatalf("unexpected no-upstream result (%d):\n%s", result.exitCode, result.output)
		}
	})

	t.Run("ahead", func(t *testing.T) {
		home := t.TempDir()
		_, _, local := createRepository(t, home, "ahead-skill")
		git(t, local, "config", "user.email", "skillctl@example.test")
		git(t, local, "config", "user.name", "Skillctl Test")
		mustWrite(t, filepath.Join(local, "ahead.txt"), "ahead")
		git(t, local, "add", ".")
		git(t, local, "commit", "-m", "ahead")

		result := runSkillctl(t, home, "update", "--path", local)
		if result.exitCode != 0 || !strings.Contains(result.output, "ahead-skill [git-worktree, repository]: skipped (ahead by 1 commits)") {
			t.Fatalf("unexpected ahead result (%d):\n%s", result.exitCode, result.output)
		}
	})

	t.Run("diverged", func(t *testing.T) {
		home := t.TempDir()
		_, seed, local := createRepository(t, home, "diverged-skill")
		git(t, local, "config", "user.email", "skillctl@example.test")
		git(t, local, "config", "user.name", "Skillctl Test")
		mustWrite(t, filepath.Join(local, "local.txt"), "local")
		git(t, local, "add", ".")
		git(t, local, "commit", "-m", "local")
		mustWrite(t, filepath.Join(seed, "remote.txt"), "remote")
		git(t, seed, "add", ".")
		git(t, seed, "commit", "-m", "remote")
		git(t, seed, "push")

		result := runSkillctl(t, home, "update", "--path", local)
		if result.exitCode != 0 || !strings.Contains(result.output, "diverged-skill [git-worktree, repository]: skipped (branch has diverged)") {
			t.Fatalf("unexpected diverged result (%d):\n%s", result.exitCode, result.output)
		}
	})
}

func TestTrackedCopiedSkillCanCheckAndUpdate(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "copied-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "copied-skill")
	copyDirForTest(t, filepath.Join(checkout, "copied-skill"), installedSkill)

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "copied-skill")
	if track.exitCode != 0 || !strings.Contains(track.output, "copied-skill: tracked") {
		t.Fatalf("unexpected track result (%d):\n%s", track.exitCode, track.output)
	}

	mustWrite(t, filepath.Join(seed, "copied-skill", "new.txt"), "remote content")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "update copied skill")
	git(t, seed, "push")

	check := runSkillctl(t, home, "check", "--path", installedRoot)
	if check.exitCode != 0 || !strings.Contains(check.output, "copied-skill [skillctl-track-v1, skillctl]: update available") {
		t.Fatalf("unexpected copied check result (%d):\n%s", check.exitCode, check.output)
	}

	update := runSkillctl(t, home, "update", "--path", installedRoot)
	if update.exitCode != 0 || !strings.Contains(update.output, "copied-skill [skillctl-track-v1, skillctl]: updated") {
		t.Fatalf("unexpected copied update result (%d):\n%s", update.exitCode, update.output)
	}
	if content, err := os.ReadFile(filepath.Join(installedSkill, "new.txt")); err != nil || string(content) != "remote content" {
		t.Fatalf("copied skill content was not updated: %q, %v", content, err)
	}
}

func TestTrackedCopiedSkillSourceOverridesEnclosingRepository(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "nested-copy-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "skills", "nested-copy-skill")
	copyDirForTest(t, filepath.Join(checkout, "nested-copy-skill"), installedSkill)

	git(t, installedRoot, "init")
	git(t, installedRoot, "config", "user.email", "skillctl@example.test")
	git(t, installedRoot, "config", "user.name", "Skillctl Test")
	git(t, installedRoot, "add", ".")
	git(t, installedRoot, "commit", "-m", "unrelated parent repository")

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "nested-copy-skill")
	if track.exitCode != 0 {
		t.Fatalf("track failed (%d):\n%s", track.exitCode, track.output)
	}

	mustWrite(t, filepath.Join(seed, "nested-copy-skill", "new.txt"), "remote content")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "update copied skill")
	git(t, seed, "push")

	result := runSkillctl(t, home, "check", "--path", installedRoot)
	if result.exitCode != 0 || !strings.Contains(result.output, "nested-copy-skill [skillctl-track-v1, skillctl]: update available") {
		t.Fatalf("tracked source did not override the enclosing repository (%d):\n%s", result.exitCode, result.output)
	}
}

func TestTrackRejectsAChangedLocalCopy(t *testing.T) {
	home := t.TempDir()
	remote, _, checkout := createRepository(t, home, "changed-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "changed-skill")
	copyDirForTest(t, filepath.Join(checkout, "changed-skill"), installedSkill)
	mustWrite(t, filepath.Join(installedSkill, "local.txt"), "custom")

	result := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "changed-skill")
	if result.exitCode != 1 || !strings.Contains(result.output, "local content does not match the source") {
		t.Fatalf("unexpected changed-copy track result (%d):\n%s", result.exitCode, result.output)
	}
}

func TestTrackedUpdateRejectsChangedSourceIdentity(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "identity-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "identity-skill")
	copyDirForTest(t, filepath.Join(checkout, "identity-skill"), installedSkill)
	original, err := os.ReadFile(filepath.Join(installedSkill, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "identity-skill")
	if track.exitCode != 0 {
		t.Fatalf("track failed (%d):\n%s", track.exitCode, track.output)
	}

	mustWrite(t, filepath.Join(seed, "identity-skill", "SKILL.md"), "---\nname: replacement-skill\ndescription: Different skill\n---\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "replace source identity")
	git(t, seed, "push")

	result := runSkillctl(t, home, "update", "--path", installedRoot)
	if result.exitCode != 1 || !strings.Contains(result.output, `source path does not contain skill "identity-skill"`) {
		t.Fatalf("changed source identity was not rejected (%d):\n%s", result.exitCode, result.output)
	}
	after, err := os.ReadFile(filepath.Join(installedSkill, "SKILL.md"))
	if err != nil || string(after) != string(original) {
		t.Fatalf("installed skill changed after identity rejection: %q, %v", after, err)
	}
}

func TestTrackRecognizesAnOlderExactSourceVersion(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "older-skill")
	installedRoot := filepath.Join(home, "installed")
	copyDirForTest(t, filepath.Join(checkout, "older-skill"), filepath.Join(installedRoot, "older-skill"))
	mustWrite(t, filepath.Join(seed, "older-skill", "new.txt"), "new")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "new version")
	git(t, seed, "push")

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "older-skill")
	if track.exitCode != 0 || !strings.Contains(track.output, "older-skill: tracked") {
		t.Fatalf("unexpected historical track result (%d):\n%s", track.exitCode, track.output)
	}
	check := runSkillctl(t, home, "check", "--path", installedRoot)
	if check.exitCode != 0 || !strings.Contains(check.output, "older-skill [skillctl-track-v1, skillctl]: update available") {
		t.Fatalf("unexpected historical check result (%d):\n%s", check.exitCode, check.output)
	}
}

func TestTrackedCopiedSkillFollowsExplicitBranchRef(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "branch-ref-skill")
	branch := git(t, seed, "branch", "--show-current")
	installedRoot := filepath.Join(home, "installed")
	copyDirForTest(t, filepath.Join(checkout, "branch-ref-skill"), filepath.Join(installedRoot, "branch-ref-skill"))

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "--ref", branch, "branch-ref-skill")
	if track.exitCode != 0 {
		t.Fatalf("track failed (%d):\n%s", track.exitCode, track.output)
	}

	mustWrite(t, filepath.Join(seed, "branch-ref-skill", "new.txt"), "new version")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "advance explicit branch")
	git(t, seed, "push")

	result := runSkillctl(t, home, "check", "--path", installedRoot)
	if result.exitCode != 0 || !strings.Contains(result.output, "branch-ref-skill [skillctl-track-v1, skillctl]: update available") {
		t.Fatalf("explicit branch ref did not follow the remote branch (%d):\n%s", result.exitCode, result.output)
	}
}

func TestTrackedCopiedSkillDoesNotOverwriteLocalChanges(t *testing.T) {
	home := t.TempDir()
	remote, seed, checkout := createRepository(t, home, "protected-skill")
	installedRoot := filepath.Join(home, "installed")
	installedSkill := filepath.Join(installedRoot, "protected-skill")
	copyDirForTest(t, filepath.Join(checkout, "protected-skill"), installedSkill)

	track := runSkillctl(t, home, "track", "--path", installedRoot, "--source", remote, "--skill-path", "protected-skill", "protected-skill")
	if track.exitCode != 0 {
		t.Fatalf("track failed (%d):\n%s", track.exitCode, track.output)
	}
	mustWrite(t, filepath.Join(installedSkill, "local.txt"), "keep me")
	mustWrite(t, filepath.Join(seed, "protected-skill", "remote.txt"), "remote")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "remote update")
	git(t, seed, "push")

	result := runSkillctl(t, home, "update", "--path", installedRoot)
	if result.exitCode != 0 || !strings.Contains(result.output, "update available, skipped (local files were modified)") {
		t.Fatalf("unexpected protected update result (%d):\n%s", result.exitCode, result.output)
	}
	if content, err := os.ReadFile(filepath.Join(installedSkill, "local.txt")); err != nil || string(content) != "keep me" {
		t.Fatalf("local content was overwritten: %q, %v", content, err)
	}
}

func TestUpdateSkipsDirtyRepositoryWithAvailableUpdate(t *testing.T) {
	home := t.TempDir()
	_, seed, local := createRepository(t, home, "dirty-skill")
	mustWrite(t, filepath.Join(seed, "remote.txt"), "remote")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "remote")
	git(t, seed, "push")
	mustWrite(t, filepath.Join(local, "local.txt"), "dirty")

	result := runSkillctl(t, home, "update", "--path", local, "dirty-skill")
	if result.exitCode != 0 || !strings.Contains(result.output, "update available (behind 1 commits), skipped (working tree is dirty)") {
		t.Fatalf("unexpected dirty update result (%d):\n%s", result.exitCode, result.output)
	}
	if _, err := os.Stat(filepath.Join(local, "remote.txt")); !os.IsNotExist(err) {
		t.Fatalf("dirty repository was unexpectedly updated")
	}
}

func TestExplicitConfigRejectsUnknownFields(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "team.toml")
	mustWrite(t, config, "pahts = []\n")

	result := runSkillctl(t, home, "check", "--config", config)
	if result.exitCode != 2 || !strings.Contains(result.output, "unknown field") {
		t.Fatalf("unexpected invalid config result (%d):\n%s", result.exitCode, result.output)
	}
}

func TestSkillNameMayDifferFromInstallDirectory(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "skills")
	dir := filepath.Join(root, "react-best-practices")
	mustMkdirAll(t, dir)
	mustWrite(t, filepath.Join(dir, "SKILL.md"), "---\nname: vercel-react-best-practices\ndescription: React guidance\n---\n")

	result := runSkillctl(t, home, "check", "--path", root)
	if result.exitCode != 0 || !strings.Contains(result.output, "vercel-react-best-practices [local-authoring, user]: local/untracked (no update source)") {
		t.Fatalf("unexpected renamed skill result (%d):\n%s", result.exitCode, result.output)
	}
}

func TestScannerFollowsDirectorySymlinkWhenPermitted(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "target", "linked-skill")
	mustMkdirAll(t, target)
	mustWrite(t, filepath.Join(target, "SKILL.md"), "---\nname: linked-skill\ndescription: Linked skill\n---\n")
	root := filepath.Join(home, "scan")
	mustMkdirAll(t, root)
	if err := os.Symlink(target, filepath.Join(root, "linked-skill")); err != nil {
		t.Skipf("directory symlink is unavailable without elevated privileges: %v", err)
	}

	result := runSkillctl(t, home, "check", "--path", root)
	if result.exitCode != 0 || !strings.Contains(result.output, "linked-skill [local-authoring, user]: local/untracked (no update source)") {
		t.Fatalf("unexpected linked skill result (%d):\n%s", result.exitCode, result.output)
	}
}

type commandResult struct {
	exitCode int
	output   string
}

func runSkillctl(t *testing.T, home string, args ...string) commandResult {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "skillctl-test-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(dir, "skillctl")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		build := exec.Command("go", "build", "-o", binary, ".")
		build.Dir = root
		buildOut, buildErr = build.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("build skillctl: %v\n%s", buildErr, buildOut)
	}

	cmd := exec.Command(binary, args...)
	cmd.Env = testEnv(home)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run skillctl: %v", err)
		}
	}
	return commandResult{exitCode: exitCode, output: string(output)}
}

func createRepository(t *testing.T, home string, skillNames ...string) (remote, seed, local string) {
	t.Helper()
	remote = filepath.Join(home, "remote.git")
	seed = filepath.Join(home, "seed")
	local = filepath.Join(home, "skills")
	git(t, home, "init", "--bare", remote)
	git(t, home, "clone", remote, seed)
	git(t, seed, "config", "user.email", "skillctl@example.test")
	git(t, seed, "config", "user.name", "Skillctl Test")
	for _, skillName := range skillNames {
		skillDir := filepath.Join(seed, skillName)
		mustMkdirAll(t, skillDir)
		mustWrite(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: "+skillName+"\ndescription: Test skill\n---\n")
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "initial")
	git(t, seed, "push", "-u", "origin", "HEAD")
	git(t, home, "clone", remote, local)
	return remote, seed, local
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func testEnv(home string) []string {
	env := os.Environ()
	env = append(env, "HOME="+home, "USERPROFILE="+home)
	if runtime.GOOS == "windows" {
		env = append(env,
			"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
		)
	} else if runtime.GOOS == "darwin" {
		env = append(env, "XDG_CONFIG_HOME=")
	} else {
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	}
	return env
}

func userConfigDir(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Roaming")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(home, ".config")
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyDirForTest(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
