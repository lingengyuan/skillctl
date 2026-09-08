package app

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestSelectSkillsRejectsAmbiguousName(t *testing.T) {
	root := t.TempDir()
	all := []skill{
		{Name: "shared", Path: filepath.Join(root, "one")},
		{Name: "shared", Path: filepath.Join(root, "two")},
	}
	if _, err := selectSkillsWithMode(all, []string{"shared"}, false); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	selected, err := selectSkillsWithMode(all, []string{"shared"}, true)
	if err != nil || len(selected) != 2 {
		t.Fatalf("all-matches selection = %#v, %v", selected, err)
	}
}

func TestScanRootRequiredSemantics(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	var stderr bytes.Buffer
	if skills, failed := scan([]scanRoot{{Path: missing, Host: "test", Scope: "user"}}, &stderr); failed || len(skills) != 0 {
		t.Fatalf("optional root failed=%v skills=%#v stderr=%q", failed, skills, stderr.String())
	}
	stderr.Reset()
	if _, failed := scan([]scanRoot{{Path: missing, Host: "test", Scope: "user", Required: true}}, &stderr); !failed {
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

	_, manifests, _, _, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, "skills", ".skill-lock.json")
	if len(manifests) != 1 || !fsutil.SamePath(manifests[0].Path, want) {
		t.Fatalf("manifest=%#v want=%s", manifests, want)
	}
}

func TestDoctorFixRemovesStaleTrackedEntry(t *testing.T) {
	dir := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(dir, "state"))
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

func TestRepositorySkillChangesIsPathAware(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runHardeningGit(t, root, "init")
	runHardeningGit(t, root, "config", "user.name", "skillctl test")
	runHardeningGit(t, root, "config", "user.email", "skillctl@example.invalid")
	skillPath := filepath.Join(root, "skills", "demo")
	writeTestSkill(t, skillPath, "demo", "first")
	otherSkillPath := filepath.Join(root, "skills", "other")
	writeTestSkill(t, otherSkillPath, "other", "first")
	skills := []skill{{Name: "demo", Path: skillPath}, {Name: "other", Path: otherSkillPath}}
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
	changed, err := repositorySkillChanges(root, skills, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if changed[fsutil.PathKey(skillPath)] || changed[fsutil.PathKey(otherSkillPath)] {
		t.Fatalf("unrelated commit changed skill: %#v", changed)
	}
	writeTestSkill(t, skillPath, "demo", "second")
	runHardeningGit(t, root, "add", ".")
	runHardeningGit(t, root, "commit", "-m", "skill change")
	changed, err = repositorySkillChanges(root, skills, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !changed[fsutil.PathKey(skillPath)] || changed[fsutil.PathKey(otherSkillPath)] {
		t.Fatalf("skill changes = %#v", changed)
	}
	if err := os.RemoveAll(otherSkillPath); err != nil {
		t.Fatal(err)
	}
	runHardeningGit(t, root, "add", ".")
	runHardeningGit(t, root, "commit", "-m", "remove other skill")
	changed, err = repositorySkillChanges(root, skills, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if changed[fsutil.PathKey(skillPath)] || !changed[fsutil.PathKey(otherSkillPath)] {
		t.Fatalf("removed skill changes = %#v", changed)
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
