package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectSkillsRejectsAmbiguousName(t *testing.T) {
	root := t.TempDir()
	all := []skill{
		{Name: "shared", Path: filepath.Join(root, "one")},
		{Name: "shared", Path: filepath.Join(root, "two")},
	}
	if _, err := selectSkills(all, []string{"shared"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	selected, err := selectSkillsWithMode(all, []string{"shared"}, true)
	if err != nil || len(selected) != 2 {
		t.Fatalf("all-matches selection = %#v, %v", selected, err)
	}
}

func TestReadSkillUsesOnlyTopLevelFrontMatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	content := `---
name: correct-name
description: valid
metadata:
  name: wrong-name
  description: wrong
---
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := readSkill(path)
	if err != nil || name != "correct-name" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestScanRootRequiredSemantics(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	var stderr bytes.Buffer
	if skills, failed := scan([]scanRoot{{Path: missing, Host: "test", Scope: "user"}}, false, &stderr); failed || len(skills) != 0 {
		t.Fatalf("optional root failed=%v skills=%#v stderr=%q", failed, skills, stderr.String())
	}
	stderr.Reset()
	if _, failed := scan([]scanRoot{{Path: missing, Host: "test", Scope: "user", Required: true}}, false, &stderr); !failed {
		t.Fatalf("required root unexpectedly succeeded: %q", stderr.String())
	}
}

func TestGHSkillUpdateArgsTargetsOneSkillFromParent(t *testing.T) {
	dir := filepath.Join("tmp", "skills", "demo")
	args := ghSkillUpdateArgs(ghSkillUpdateRequest{Name: "demo", Directory: dir})
	want := []string{"skill", "update", "demo", "--all", "--dir", filepath.Dir(dir)}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q want=%q", args, want)
	}
}

func TestHashAndCopyIgnoreRepositoryMetadata(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	writeTestSkill(t, source, "root-skill", "root")
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

	before, err := hashDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := copyDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".skillctl-stage-old")); !os.IsNotExist(err) {
		t.Fatalf("skillctl transaction directory was copied: %v", err)
	}
	after, err := hashDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("hash changed after safe copy: %s != %s", before, after)
	}
}

func TestMergedReportPreservesInstallationDetails(t *testing.T) {
	root := t.TempDir()
	reports := []report{
		reportFor(skill{Name: "shared", Path: filepath.Join(root, "one"), Host: "codex", Scope: "user"}, "git-worktree", "repository", nil, "clean", "up to date", false, "git-ff-only", ""),
		reportFor(skill{Name: "shared", Path: filepath.Join(root, "two"), Host: "claude", Scope: "user"}, "skillctl-track-v1", "skillctl", nil, "clean", "update available", true, "staged-replacement", ""),
	}
	merged := finalizeReports(mergeReportsByIdentity(reports))
	if len(merged) != 1 || len(merged[0].Installations) != 2 {
		t.Fatalf("merged report lost installations: %#v", merged)
	}
	if merged[0].SchemaVersion != 1 || merged[0].State != "outdated" || merged[0].ReasonCode != "upstream_changed" {
		t.Fatalf("unexpected typed state: %#v", merged[0])
	}
}

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

func TestLoadConfigUsesXDGStateHomeForDefaultVercelLock(t *testing.T) {
	home := setTestHome(t)
	stateHome := filepath.Join(home, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	root := filepath.Join(home, "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	config := `[[roots]]
path = "skills"
host = "test"
scope = "user"

[[manifests]]
kind = "vercel-skills-lock-v3"
path = "~/.agents/.skill-lock.json"
install_root = "~/.agents/skills"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	_, manifests, _, _, _, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, "skills", ".skill-lock.json")
	if len(manifests) != 1 || !samePath(manifests[0].Path, want) {
		t.Fatalf("manifest=%#v want=%s", manifests, want)
	}
}

func TestValidateDryRunAndTrackOptions(t *testing.T) {
	if err := validateOptions("check", options{DryRun: true}); err == nil {
		t.Fatal("--dry-run was accepted for check")
	}
	if err := validateOptions("check", options{Source: "repo"}); err == nil {
		t.Fatal("track-only source option was accepted for check")
	}
	if err := validateOptions("update", options{DryRun: true}); err != nil {
		t.Fatalf("valid update --dry-run rejected: %v", err)
	}
}

func TestDoctorFixRemovesStaleTrackedEntry(t *testing.T) {
	dir := t.TempDir()
	state := &trackedState{
		Version: 1,
		path:    filepath.Join(dir, "sources.json"),
		Skills:  []trackedEntry{{Path: filepath.Join(dir, "missing"), Source: "repo", SkillPath: "skill"}},
	}
	findings, fixed, failed := diagnose(nil, nil, state, nil, true)
	if failed || fixed != 1 || len(state.Skills) != 0 {
		t.Fatalf("failed=%v fixed=%d state=%#v findings=%#v", failed, fixed, state, findings)
	}
	content, err := os.ReadFile(state.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved trackedState
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Skills) != 0 {
		t.Fatalf("stale source remained on disk: %#v", saved)
	}
}

func TestDoctorFixDoesNotReportItsOwnOperationLock(t *testing.T) {
	setTestHome(t)
	lock, err := acquireCommandLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()

	findings, _, _ := diagnose(nil, nil, nil, nil, true)
	for _, finding := range findings {
		if finding.Code == "active_operation_lock" {
			t.Fatalf("doctor --fix reported its own lock: %#v", findings)
		}
	}
}

func TestGitTreeComparisonIsPathAware(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runHardeningGit(t, root, "init")
	runHardeningGit(t, root, "config", "user.name", "skillctl test")
	runHardeningGit(t, root, "config", "user.email", "skillctl@example.invalid")
	skillPath := filepath.Join(root, "skills", "demo")
	writeTestSkill(t, skillPath, "demo", "first")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.md"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHardeningGit(t, root, "add", ".")
	runHardeningGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.md"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHardeningGit(t, root, "add", ".")
	runHardeningGit(t, root, "commit", "-m", "docs only")
	before, err := gitTreeAtRevision(root, skillPath, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := gitTreeAtRevision(root, skillPath, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("unrelated commit changed skill tree: %s != %s", before, after)
	}
	writeTestSkill(t, skillPath, "demo", "second")
	runHardeningGit(t, root, "add", ".")
	runHardeningGit(t, root, "commit", "-m", "skill change")
	latest, err := gitTreeAtRevision(root, skillPath, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if latest == after {
		t.Fatal("skill content change did not change tree hash")
	}
}

func runHardeningGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
