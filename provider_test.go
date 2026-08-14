package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVercelV3ClaimBindsConfiguredInstallRoot(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".skill-lock.json")
	content := `{"version":3,"skills":{"demo":{"source":"org/repo","sourceType":"git","sourceUrl":"https://example.test/repo.git","ref":"main","skillPath":"skills/demo/SKILL.md","skillFolderHash":"0123456789012345678901234567890123456789"}}}`
	if err := os.WriteFile(lockPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	locks, _ := loadVercelLocks([]manifest{{Kind: "vercel-skills-lock-v3", Path: lockPath, InstallRoot: filepath.Join(dir, "skills")}})
	claim, evidence, ok := locks.claim(skill{Name: "demo", Path: filepath.Join(dir, "skills", "demo")})
	if !ok || claim.Name != "demo" || claim.Entry.SkillPath != "skills/demo/SKILL.md" || len(evidence) != 1 {
		t.Fatalf("expected precise v3 claim: %#v %#v %v", claim, evidence, ok)
	}
	if _, _, ok := locks.claim(skill{Name: "demo", Path: filepath.Join(dir, "other", "demo")}); ok {
		t.Fatal("lock claimed same-name instance outside configured install root")
	}
}

func TestReportJSONIsStructuredAndSilentByConstruction(t *testing.T) {
	r := reportFor(skill{Name: "demo", Path: "C:/skills/demo", Aliases: []string{"C:/alias/demo"}}, "codex-host", "host", []string{"managed root"}, "none", "managed by codex", false, "report-only", "")
	data, err := json.Marshal([]report{r})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0]["provider"] != "codex-host" || decoded[0]["executor"] != "report-only" {
		t.Fatalf("missing normalized report fields: %s", data)
	}
}

func TestStructuredConfigManagedRootAndJSONDoNotWriteNormalOutput(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "system", "demo")
	if err := os.MkdirAll(system, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(system, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	config := "[[roots]]\npath = \"system\"\nhost = \"codex\"\nscope = \"system\"\n[[managed_roots]]\npath = \"system\"\nowner = \"codex\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	oldConfig := os.Getenv("APPDATA")
	_ = os.Setenv("APPDATA", dir)
	defer os.Setenv("APPDATA", oldConfig)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--config", configPath, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("stdout was not pure JSON: %q (%v)", stdout.String(), err)
	}
	if len(reports) != 1 || reports[0].Owner != "host" || reports[0].Executor != "report-only" {
		t.Fatalf("unexpected managed report: %#v", reports)
	}
}

func TestBrokenSymlinkIsReturnedAsDiagnosticWhenSupported(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "kami")
	if err := os.Symlink(filepath.Join(dir, "missing"), broken); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	items, failed := scan([]scanRoot{{Path: root, Host: "codex", Scope: "user"}}, false, io.Discard)
	if failed || len(items) != 1 || !items[0].Broken || items[0].LinkTarget == "" {
		t.Fatalf("broken link was not retained: %#v, failed=%v", items, failed)
	}
}

func TestVercelUnsupportedCheckIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "installed", "demo")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(install, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	lock := `{"version":3,"skills":{"demo":{"source":"file","sourceType":"local","sourceUrl":"file:///not-supported","skillPath":"demo/SKILL.md","skillFolderHash":"x"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	config := "[[roots]]\npath = \"installed\"\nhost = \"universal\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = \"lock.json\"\ninstall_root = \"installed\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeLock, _ := os.ReadFile(lockPath)
	beforeSkill, _ := os.ReadFile(skillFile)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--config", configPath, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Provider != "vercel-skills-lock-v3" || reports[0].Status != "unsupported source type: local" {
		t.Fatalf("unexpected report: %#v", reports)
	}
	afterLock, _ := os.ReadFile(lockPath)
	afterSkill, _ := os.ReadFile(skillFile)
	if !bytes.Equal(beforeLock, afterLock) || !bytes.Equal(beforeSkill, afterSkill) {
		t.Fatal("read-only check changed provider or skill state")
	}
}

func TestVercelGitLockCheckLatestUpdateAndDrift(t *testing.T) {
	dir := t.TempDir()
	remote, seed := filepath.Join(dir, "remote.git"), filepath.Join(dir, "seed")
	gitTest(t, dir, "init", "--bare", remote)
	gitTest(t, dir, "clone", remote, seed)
	gitTest(t, seed, "config", "user.email", "test@example.invalid")
	gitTest(t, seed, "config", "user.name", "test")
	sourceSkill := filepath.Join(seed, "skills", "demo")
	if err := os.MkdirAll(sourceSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "initial")
	gitTest(t, seed, "push", "-u", "origin", "HEAD")
	expected := gitTest(t, seed, "rev-parse", "HEAD:skills/demo")
	install := filepath.Join(dir, "installed", "demo")
	copyTestDirectory(t, sourceSkill, install)
	entry := vercelLockEntry{SourceType: "github", SourceURL: remote, SkillPath: "skills/demo/SKILL.md", SkillFolderHash: expected}
	available, drift, err := checkVercelEntryTest(entry, install)
	if err != nil || available || drift != "clean" {
		t.Fatalf("latest check = available=%v drift=%q err=%v", available, drift, err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "update")
	gitTest(t, seed, "push")
	available, drift, err = checkVercelEntryTest(entry, install)
	if err != nil || !available || drift != "clean" {
		t.Fatalf("update check = available=%v drift=%q err=%v", available, drift, err)
	}
	if err := os.WriteFile(filepath.Join(install, "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, drift, err = checkVercelEntryTest(entry, install)
	if err != nil || drift != "modified" {
		t.Fatalf("drift check = %q, %v", drift, err)
	}
}

func TestVercelProviderUpdateVerifiesLockAndInstalledContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	gitTest(t, dir, "init", "--bare", remote)
	gitTest(t, dir, "clone", remote, seed)
	gitTest(t, seed, "config", "user.email", "test@example.invalid")
	gitTest(t, seed, "config", "user.name", "test")
	sourceSkill := filepath.Join(seed, "skills", "demo")
	if err := os.MkdirAll(sourceSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: provider update fixture\n---\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "old")
	gitTest(t, seed, "push", "-u", "origin", "HEAD")
	oldTree := gitTest(t, seed, "rev-parse", "HEAD:skills/demo")

	installRoot := filepath.Join(dir, "installed")
	installed := filepath.Join(installRoot, "demo")
	copyTestDirectory(t, sourceSkill, installed)
	lockPath := filepath.Join(dir, ".skill-lock.json")
	lock := vercelLock{Version: 3, Skills: map[string]vercelLockEntry{"demo": {
		Source: "example/demo", SourceType: "github", SourceURL: remote,
		SkillPath: "skills/demo/SKILL.md", SkillFolderHash: oldTree,
	}}}
	writeJSONTestFile(t, lockPath, lock)
	configPath := filepath.Join(dir, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"universal\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(installRoot), filepath.ToSlash(lockPath), filepath.ToSlash(installRoot))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: provider update fixture\n---\nnew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "new")
	gitTest(t, seed, "push")
	newTree := gitTest(t, seed, "rev-parse", "HEAD:skills/demo")

	original := runVercelUpdater
	defer func() { runVercelUpdater = original }()
	requested := ""
	runVercelUpdater = func(_ context.Context, request vercelUpdateRequest, _ io.Writer) (string, error) {
		requested = request.Name
		replacement, err := beginDirectoryReplacement(installed, sourceSkill)
		if err != nil {
			return "", err
		}
		if err := replacement.commit(); err != nil {
			return "", err
		}
		lock.Skills["demo"] = vercelLockEntry{
			Source: "example/demo", SourceType: "github", SourceURL: remote,
			SkillPath: "skills/demo/SKILL.md", SkillFolderHash: newTree,
		}
		writeJSONTestFile(t, lockPath, lock)
		return "updated demo", nil
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--timeout", "30s", "--config", configPath, "demo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("update failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if requested != "demo" || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("provider request=%q output=%q", requested, stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil || !strings.Contains(string(content), "new") {
		t.Fatalf("installed content was not updated: %v %q", err, content)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check", "--timeout", "30s", "--config", configPath, "demo"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("post-update check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	beforeSkill, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	beforeLock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: provider update fixture\n---\nprovider-partial-write\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "provider partial write")
	gitTest(t, seed, "push")
	runVercelUpdater = func(_ context.Context, _ vercelUpdateRequest, _ io.Writer) (string, error) {
		replacement, err := beginDirectoryReplacement(installed, sourceSkill)
		if err != nil {
			return "", err
		}
		return "provider exited successfully without updating its lock", replacement.commit()
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"update", "--timeout", "30s", "--config", configPath, "demo"}, &stdout, &stderr); code != 1 {
		t.Fatalf("partial provider update exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	afterSkill, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil || !bytes.Equal(afterSkill, beforeSkill) {
		t.Fatalf("installed skill was not rolled back: %v before=%q after=%q", err, beforeSkill, afterSkill)
	}
	afterLock, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(afterLock, beforeLock) {
		t.Fatalf("provider lock was not rolled back: %v before=%q after=%q", err, beforeLock, afterLock)
	}
}

func TestGHSkillMetadataCheckAndUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	gitTest(t, dir, "init", "--bare", remote)
	gitTest(t, dir, "clone", remote, seed)
	gitTest(t, seed, "config", "user.email", "test@example.invalid")
	gitTest(t, seed, "config", "user.name", "test")
	branch := gitTest(t, seed, "branch", "--show-current")
	sourceSkill := filepath.Join(seed, "skills", "demo")
	if err := os.MkdirAll(sourceSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: gh adapter fixture\n---\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "old")
	gitTest(t, seed, "push", "-u", "origin", "HEAD")
	oldTree := gitTest(t, seed, "rev-parse", "HEAD:skills/demo")

	root := filepath.Join(dir, "installed")
	installed := filepath.Join(root, "demo")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGHSkillTestFile(t, filepath.Join(installed, "SKILL.md"), branch, oldTree, "old")

	if err := os.WriteFile(filepath.Join(sourceSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: gh adapter fixture\n---\nnew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "new")
	gitTest(t, seed, "push")
	newTree := gitTest(t, seed, "rev-parse", "HEAD:skills/demo")

	originalURL := ghRepositoryURL
	originalUpdater := runGHSkillUpdater
	defer func() {
		ghRepositoryURL = originalURL
		runGHSkillUpdater = originalUpdater
	}()
	ghRepositoryURL = func(string) string { return remote }
	requested := ""
	runGHSkillUpdater = func(_ context.Context, request ghSkillUpdateRequest, _ io.Writer) (string, error) {
		requested = request.Name
		writeGHSkillTestFile(t, filepath.Join(installed, "SKILL.md"), branch, newTree, "new")
		return "updated demo", nil
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--timeout", "30s", "--path", root, "demo"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "update available") || !strings.Contains(stdout.String(), "gh-skill") {
		t.Fatalf("gh check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"update", "--timeout", "30s", "--path", root, "demo"}, &stdout, &stderr); code != 0 || requested != "demo" || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("gh update failed (%d, request=%q): stdout=%q stderr=%q", code, requested, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check", "--timeout", "30s", "--path", root, "demo"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("gh post-check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func writeGHSkillTestFile(t *testing.T, path, ref, tree, body string) {
	t.Helper()
	content := fmt.Sprintf("---\nname: demo\ndescription: gh adapter fixture\nmetadata:\n  github-repo: example/demo\n  github-ref: %s\n  github-tree-sha: %s\n  github-path: skills/demo\n---\n%s\n", ref, tree, body)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGHLocalPathMetadataIsNotReportedAsUntracked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	skillDir := filepath.Join(dir, "skills", "local-demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: local-demo\ndescription: local gh fixture\nmetadata:\n  local-path: ../source/local-demo\n---\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--path", filepath.Dir(skillDir), "local-demo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "gh-skill") || !strings.Contains(stdout.String(), "managed from local path") || strings.Contains(stdout.String(), "track --source") {
		t.Fatalf("local-path metadata was not classified: %q", stdout.String())
	}
}

func writeJSONTestFile(t *testing.T, path string, value any) {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(bytes.TrimSpace(output))
}

func checkVercelEntryTest(entry vercelLockEntry, installed string) (bool, string, error) {
	session := newSourceSession(context.Background(), io.Discard)
	defer session.close()
	return checkVercelEntry(session, entry, installed)
}

func copyTestDirectory(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitClaimRequiresTrackedSkillFile(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "test@example.invalid")
	gitTest(t, dir, "config", "user.name", "test")
	skillDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gitTracks(dir, "demo/SKILL.md") {
		t.Fatal("untracked SKILL.md must not be a Git claim")
	}
	gitTest(t, dir, "add", "demo/SKILL.md")
	gitTest(t, dir, "commit", "-m", "track skill")
	if !gitTracks(dir, "demo/SKILL.md") {
		t.Fatal("tracked SKILL.md was not accepted")
	}
}

func TestProviderClaimWinsAndExplicitConflictFailsClosed(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "skills", "demo")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	lock := `{"version":3,"skills":{"demo":{"sourceType":"local","skillPath":"demo/SKILL.md","skillFolderHash":"x"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	item := skill{Name: "demo", Path: install, ScanRoot: filepath.Join(dir, "skills")}
	manifests := []manifest{{Kind: "vercel-skills-lock-v3", Path: lockPath, InstallRoot: filepath.Join(dir, "skills")}}
	reports, failed := inspect(context.Background(), "check", []skill{item}, &trackedState{Version: 1}, manifests, nil, io.Discard, io.Discard)
	if failed || len(reports) != 1 || reports[0].Provider != "vercel-skills-lock-v3" {
		t.Fatalf("provider did not win: %#v", reports)
	}
	state := &trackedState{Version: 1, Skills: []trackedEntry{{Path: install, Source: "elsewhere", SkillPath: "demo", InstalledHash: "x"}}}
	reports, _ = inspect(context.Background(), "check", []skill{item}, state, manifests, nil, io.Discard, io.Discard)
	if reports[0].Status != "ambiguous provenance" || reports[0].Error == "" {
		t.Fatalf("conflict did not fail closed: %#v", reports[0])
	}
}

func TestSourceSessionFetchesEachNormalizedSourceRefOnce(t *testing.T) {
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()
	count := 0
	syncSourceForSession = func(_ context.Context, source, ref string) (string, error) { count++; return source + "-" + ref, nil }
	session := sourceSession{ctx: context.Background(), caches: map[string]string{}}
	if _, err := session.source("source", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.source("source", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.source("source", "release"); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("sync count = %d, want one per normalized (source, ref)", count)
	}
}

func TestSourceSessionPrefetchesIndependentSourcesConcurrently(t *testing.T) {
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()
	started := make(chan string, 3)
	release := make(chan struct{})
	syncSourceForSession = func(_ context.Context, source, ref string) (string, error) {
		started <- source
		<-release
		return source, nil
	}
	session := newSourceSession(context.Background(), io.Discard)
	done := make(chan struct{})
	go func() {
		session.prefetch([]sourceRequest{{Source: "one"}, {Source: "two"}, {Source: "three"}})
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("independent source checks did not start concurrently")
		}
	}
	close(release)
	<-done
}

func TestCheckReportsProgressBeforeRemoteWait(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	root := filepath.Join(dir, "skills")
	installed := filepath.Join(root, "slow-skill")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), []byte("---\nname: slow-skill\ndescription: progress fixture\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	lock := `{"version":3,"skills":{"slow-skill":{"sourceType":"github","sourceUrl":"https://example.invalid/slow.git","skillPath":"skills/slow-skill/SKILL.md","skillFolderHash":"0000000000000000000000000000000000000000"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()
	entered := make(chan struct{})
	release := make(chan struct{})
	syncSourceForSession = func(_ context.Context, source, ref string) (string, error) {
		close(entered)
		<-release
		return "", fmt.Errorf("fixture stopped")
	}

	var stdout bytes.Buffer
	progress := newSignalWriter()
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"check", "--config", configPath, "slow-skill"}, &stdout, progress)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("remote check did not start within one second")
	}
	select {
	case <-progress.wrote:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("no progress was written while the remote check was waiting")
	}
	close(release)
	if code := <-done; code != 1 {
		t.Fatalf("exit code = %d, want provider failure", code)
	}
	if !strings.Contains(progress.String(), "Checking") {
		t.Fatalf("progress output = %q", progress.String())
	}
}

func TestCheckStopsAtNetworkTimeout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	root := filepath.Join(dir, "skills")
	installed := filepath.Join(root, "slow-skill")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), []byte("---\nname: slow-skill\ndescription: timeout fixture\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	lock := `{"version":3,"skills":{"slow-skill":{"sourceType":"github","sourceUrl":"https://example.invalid/slow.git","skillPath":"skills/slow-skill/SKILL.md","skillFolderHash":"0000000000000000000000000000000000000000"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()
	syncSourceForSession = func(ctx context.Context, source, ref string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	for _, tc := range []struct {
		name   string
		prefix string
		args   []string
	}{
		{name: "command line", args: []string{"--timeout", "25ms"}},
		{name: "config file", prefix: "network_timeout = \"25ms\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".toml")
			config := tc.prefix + fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			args := append([]string{"check"}, tc.args...)
			args = append(args, "--config", configPath, "slow-skill")
			started := time.Now()
			code := run(args, &stdout, &stderr)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("timeout returned after %s, want less than one second", elapsed)
			}
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), "timeout") {
				t.Fatalf("timeout was not reported: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

type signalWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	wrote chan struct{}
}

func newSignalWriter() *signalWriter {
	return &signalWriter{wrote: make(chan struct{})}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestReportSinkAssociatesSameNameByCanonicalPath(t *testing.T) {
	a := skill{Name: "same", Path: "C:/one"}
	b := skill{Name: "same", Path: "C:/two"}
	state := &trackedState{Version: 1, Skills: []trackedEntry{{Path: b.Path, Source: "https://example.test/source.git"}}}
	s := newReportSink([]skill{a, b}, state)
	s.set([]skill{a}, "update available")
	s.failure(b, "hash local skill: denied")
	s.markGit([]skill{a}, "C:/repo")
	if s.reports[0].Provider != "git-worktree" || s.reports[0].Status != "update available" || s.reports[1].Provider != "skillctl-track-v1" || s.reports[1].Owner != "skillctl" || s.reports[1].Executor != "staged-replacement" || s.reports[1].Error == "" || len(s.reports[1].Evidence) != 1 {
		t.Fatalf("incorrect structured reports: %#v", s.reports)
	}
}

func TestAuthoritativeClaimConflictsFailClosed(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "skills", "demo")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "lock.json")
	if err := os.WriteFile(lockPath, []byte(`{"version":3,"skills":{"demo":{"sourceType":"local","skillPath":"demo/SKILL.md","skillFolderHash":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	item := skill{Name: "demo", Path: install}
	manifests := []manifest{{Kind: "vercel-skills-lock-v3", Path: lockPath, InstallRoot: filepath.Join(dir, "skills")}}
	managed := []managedRoot{{Path: filepath.Join(dir, "skills"), Owner: "codex"}}
	for _, state := range []*trackedState{{Version: 1}, {Version: 1, Skills: []trackedEntry{{Path: install, Source: "source"}}}} {
		reports, failed := inspect(context.Background(), "check", []skill{item}, state, manifests, managed, io.Discard, io.Discard)
		if !failed || reports[0].Status != "ambiguous provenance" || len(reports[0].Evidence) < 2 {
			t.Fatalf("conflict: %#v", reports)
		}
	}
	reports, failed := inspect(context.Background(), "check", []skill{item}, &trackedState{Version: 1, Skills: []trackedEntry{{Path: install, Source: "source"}}}, manifests, nil, io.Discard, io.Discard)
	if !failed || reports[0].Status != "ambiguous provenance" {
		t.Fatalf("provider explicit: %#v", reports)
	}
}

func TestLegacyDefaultConfigMigratesButCustomPathsDoesNot(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.toml")
	if err := os.WriteFile(old, []byte(legacyDefaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, manifests, managed, _, _, err := loadConfig(old)
	if err != nil || len(manifests) == 0 || len(managed) == 0 {
		t.Fatalf("migration: %v %#v %#v", err, manifests, managed)
	}
	migrated, _ := os.ReadFile(old)
	if string(migrated) != defaultConfig {
		t.Fatal("old default not atomically replaced")
	}
	custom := filepath.Join(dir, "custom.toml")
	text := "paths = [\"custom\"]\n"
	if err := os.WriteFile(custom, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _, _, _, _, err := loadConfig(custom)
	if err != nil || len(roots) != 1 {
		t.Fatalf("custom load: %v %#v", err, roots)
	}
	after, _ := os.ReadFile(custom)
	if string(after) != text {
		t.Fatal("custom legacy config was rewritten")
	}
}

func TestDefaultConfigHasUniqueStructuredRoots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(defaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _, _, _, _, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range roots {
		if r.Host == "" || r.Scope == "" || seen[r.Path] {
			t.Fatalf("bad root %#v", r)
		}
		seen[r.Path] = true
	}
	if len(roots) != 8 {
		t.Fatalf("roots=%d, want 8", len(roots))
	}
}

func TestManagedRootMatchesAlias(t *testing.T) {
	item := skill{Path: "C:/real/skill", Aliases: []string{"C:/managed/skill"}}
	owner, _ := managedOwner(item, []managedRoot{{Path: "C:/managed", Owner: "codex"}})
	if owner != "codex" {
		t.Fatal(owner)
	}
}

func TestExplicitTrackMatchesAlias(t *testing.T) {
	item := skill{Path: "C:/real/skill", Aliases: []string{"C:/linked/skill"}}
	state := &trackedState{Skills: []trackedEntry{{Path: "C:/linked/skill", Source: "source"}}}
	entry, ok := state.findSkill(item)
	if !ok || entry.Source != "source" {
		t.Fatalf("explicit track alias was not matched: %#v, %v", entry, ok)
	}
}

func TestScannerRetainsNormalSymlinkAliasWhenSupported(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	target := filepath.Join(root, "target", "demo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: demo\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	items, failed := scan([]scanRoot{{Path: root, Host: "test", Scope: "user"}}, false, io.Discard)
	if failed || len(items) != 1 {
		t.Fatalf("items=%#v failed=%v", items, failed)
	}
	found := false
	for _, p := range items[0].Aliases {
		if samePath(p, alias) {
			found = true
		}
	}
	if !found {
		t.Fatalf("alias missing: %#v", items[0])
	}
}

func TestScannerMapsAliasedRootPerSkillWhenSupported(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "canonical")
	for _, n := range []string{"one", "nested/two"} {
		d := filepath.Join(canonical, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+filepath.Base(n)+"\ndescription: x\n---\n"), 0o600)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Skip(err)
	}
	items, failed := scan([]scanRoot{{Path: canonical}, {Path: alias}}, false, io.Discard)
	if failed || len(items) != 2 {
		t.Fatalf("%#v", items)
	}
	for _, item := range items {
		want := filepath.Join(alias, mustRel(t, canonical, item.Path))
		found := false
		for _, a := range item.Aliases {
			if samePath(a, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s aliases=%#v want=%s", item.Name, item.Aliases, want)
		}
	}
}

func TestScannerMapsInternalAncestorAliasPerSkillWhenSupported(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	target := filepath.Join(root, "target")
	for _, n := range []string{"one", "nested/two"} {
		d := filepath.Join(target, n)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+filepath.Base(n)+"\ndescription: x\n---\n"), 0o600)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skip(err)
	}
	items, _ := scan([]scanRoot{{Path: root}}, false, io.Discard)
	if len(items) != 2 {
		t.Fatalf("%#v", items)
	}
	for _, item := range items {
		want := filepath.Join(alias, mustRel(t, target, item.Path))
		found := false
		for _, a := range item.Aliases {
			if samePath(a, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s %#v", item.Name, item.Aliases)
		}
	}
}
func mustRel(t *testing.T, a, b string) string {
	t.Helper()
	r, e := filepath.Rel(a, b)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestInspectKeepsEarlierAmbiguousFailureAndRendersToBytesBuffer(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "skills")
	for _, n := range []string{"a", "b"} {
		d := filepath.Join(root, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+n+"\ndescription: test\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lock := filepath.Join(dir, "lock.json")
	if err := os.WriteFile(lock, []byte(`{"version":3,"skills":{"a":{"sourceType":"local","skillPath":"a/SKILL.md","skillFolderHash":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _ := scan([]scanRoot{{Path: root, Host: "x", Scope: "user"}}, false, io.Discard)
	var out bytes.Buffer
	reports, failed := inspect(context.Background(), "check", items, &trackedState{Version: 1, Skills: []trackedEntry{{Path: filepath.Join(root, "a"), Source: "x"}}}, []manifest{{Kind: "vercel-skills-lock-v3", Path: lock, InstallRoot: root}}, nil, &out, &out)
	if !failed || len(reports) != 2 || !strings.Contains(out.String(), "ambiguous provenance") {
		t.Fatalf("failed=%v reports=%#v output=%q", failed, reports, out.String())
	}
}

func TestReportSinkUntrackedStaysLocalAndTrackedGetsGitEvidence(t *testing.T) {
	a := skill{Name: "a", Path: "C:/a"}
	s := newReportSink([]skill{a}, &trackedState{Version: 1})
	if s.reports[0].Provider != "local-authoring" || s.reports[0].Owner != "user" || s.reports[0].Executor != "report-only" {
		t.Fatalf("untracked=%#v", s.reports[0])
	}
	s.markGit([]skill{a}, "C:/repo")
	if s.reports[0].Provider != "git-worktree" || s.reports[0].Owner != "repository" || len(s.reports[0].Evidence) != 1 || s.reports[0].Evidence[0] != "C:/repo" {
		t.Fatalf("tracked=%#v", s.reports[0])
	}
}

func TestVercelClaimUsesLockKeyAndAlias(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock.json")
	if err := os.WriteFile(lock, []byte(`{"version":3,"skills":{"folder-name":{"sourceType":"local","skillPath":"x/SKILL.md","skillFolderHash":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	locks, _ := loadVercelLocks([]manifest{{Kind: "vercel-skills-lock-v3", Path: lock, InstallRoot: filepath.Join(dir, "skills")}})
	if _, _, ok := locks.claim(skill{Name: "declared-name", Path: filepath.Join(dir, "other"), Aliases: []string{filepath.Join(dir, "skills", "folder-name")}}); !ok {
		t.Fatal("alias/key claim missing")
	}
}

func TestVercelGitContentHashSemantics(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "skill")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := hashDirectory(skill)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("empty hash")
	}
}

func TestVercelGitDirectoryHashLatestUpdateAndDrift(t *testing.T) {
	dir := t.TempDir()
	remote, seed := filepath.Join(dir, "r.git"), filepath.Join(dir, "seed")
	gitTest(t, dir, "init", "--bare", remote)
	gitTest(t, dir, "clone", remote, seed)
	gitTest(t, seed, "config", "user.email", "a@b.c")
	gitTest(t, seed, "config", "user.name", "a")
	source := filepath.Join(seed, "skills", "demo")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "a")
	gitTest(t, seed, "push", "-u", "origin", "HEAD")
	h, _ := hashDirectory(source)
	installed := filepath.Join(dir, "installed")
	copyTestDirectory(t, source, installed)
	entry := vercelLockEntry{SourceType: "git", SourceURL: remote, SkillPath: "skills/demo/SKILL.md", SkillFolderHash: h}
	available, drift, err := checkVercelEntryTest(entry, installed)
	if err != nil || available || drift != "clean" {
		t.Fatalf("latest %v %s %v", available, drift, err)
	}
	if err := os.WriteFile(filepath.Join(source, "new"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "commit", "-m", "b")
	gitTest(t, seed, "push")
	available, drift, err = checkVercelEntryTest(entry, installed)
	if err != nil || !available || drift != "clean" {
		t.Fatalf("update %v %s %v", available, drift, err)
	}
	if err := os.WriteFile(filepath.Join(installed, "local"), []byte("l"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, drift, err = checkVercelEntryTest(entry, installed)
	if err != nil || drift != "modified" {
		t.Fatalf("drift %s %v", drift, err)
	}
}

func TestManifestErrorsAreStableAndBrokenWins(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "b.json"), filepath.Join(dir, "a.json")
	os.WriteFile(a, []byte("{"), 0o600)
	os.WriteFile(b, []byte(`{"version":2}`), 0o600)
	ms := []manifest{{Kind: "vercel-skills-lock-v3", Path: a, InstallRoot: dir}, {Kind: "vercel-skills-lock-v3", Path: b, InstallRoot: dir}}
	_, errs := loadVercelLocks(ms)
	if errs[a] != "invalid JSON" || errs[b] != "unsupported schema version: 2" {
		t.Fatalf("errors=%#v", errs)
	}
	reports, failed := inspect(context.Background(), "check", []skill{{Name: "x", Path: filepath.Join(dir, "x"), Broken: true, LinkTarget: "missing"}}, &trackedState{Version: 1}, ms, nil, io.Discard, io.Discard)
	if failed || reports[0].Provider != "filesystem" {
		t.Fatalf("broken=%#v failed=%v", reports, failed)
	}
}

func TestTextIncludesProviderOwnerAndLocalStatus(t *testing.T) {
	var out bytes.Buffer
	printReport(&out, report{Identity: "demo", Provider: "local-authoring", Owner: "user", Status: "local/untracked (no update source)"}, false)
	if !strings.Contains(out.String(), "[local-authoring, user]") || !strings.Contains(out.String(), "local/untracked") {
		t.Fatal(out.String())
	}
}

func TestVercelModifiedStatusIsVisible(t *testing.T) {
	var out bytes.Buffer
	printReport(&out, report{Identity: "demo", Provider: "vercel-skills-lock-v3", Owner: "provider", Drift: "modified", Status: "up to date, local files were modified"}, false)
	if !strings.Contains(out.String(), "modified") {
		t.Fatal(out.String())
	}
}

func TestVercelStatusModifiedMatrix(t *testing.T) {
	cases := []struct {
		action    string
		available bool
		want      string
	}{{"check", true, "update available, local files were modified"}, {"update", true, "update available, skipped (local files were modified)"}, {"check", false, "up to date, local files were modified"}, {"update", false, "up to date, skipped (local files were modified)"}}
	for _, c := range cases {
		if got := vercelStatus(c.action, c.available, "modified"); got != c.want {
			t.Fatalf("%#v got %q", c, got)
		}
	}
}

func TestJSONEncodingHasNoTextNoise(t *testing.T) {
	data, err := json.Marshal([]report{{Identity: "demo", Provider: "local-authoring", Owner: "user", Status: "local/untracked (no update source)"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []report
	if err := json.Unmarshal(data, &got); err != nil || len(got) != 1 {
		t.Fatalf("%s %v", data, err)
	}
}

func TestJSONCheckSuppressesProgress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	skillDir := filepath.Join(dir, "skills", "json-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: json-skill\ndescription: JSON fixture\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--json", "--path", filepath.Dir(skillDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON progress leaked to stderr: %q", stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil || len(reports) != 1 {
		t.Fatalf("invalid JSON output: %v, %q", err, stdout.String())
	}
}

func TestUntrackedCheckShowsTrackRepairCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(dir, "app-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local-app-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	skillDir := filepath.Join(dir, "skills", "repair-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: repair-skill\ndescription: Repair fixture\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--path", filepath.Dir(skillDir), "repair-skill"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	want := "skillctl track --source SOURCE_URL repair-skill"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("track repair command missing from output:\n%s", stdout.String())
	}
}

func TestReportSinkSetsDriftForGitDirtyAndTrackModified(t *testing.T) {
	a := skill{Name: "a", Path: "a"}
	b := skill{Name: "b", Path: "b"}
	s := newReportSink([]skill{a, b}, &trackedState{Version: 1, Skills: []trackedEntry{{Path: "b", Source: "s"}}})
	s.markGit([]skill{a}, ".")
	s.set([]skill{a}, "skipped (working tree is dirty)")
	s.set([]skill{b}, "update available, skipped (local files were modified)")
	if s.reports[0].Drift != "modified" || s.reports[1].Drift != "modified" {
		t.Fatalf("%#v", s.reports)
	}
}

func TestReportSinkDefaultsDriftByOwnership(t *testing.T) {
	a := skill{Path: "a"}
	b := skill{Path: "b"}
	s := newReportSink([]skill{a, b}, &trackedState{Version: 1, Skills: []trackedEntry{{Path: "b", Source: "s"}}})
	if s.reports[0].Drift != "unknown" || s.reports[1].Drift != "clean" {
		t.Fatalf("%#v", s.reports)
	}
	s.markGit([]skill{a}, ".")
	if s.reports[0].Drift != "clean" {
		t.Fatalf("%#v", s.reports[0])
	}
	s.set([]skill{a}, "up to date")
	if s.reports[0].Drift != "clean" {
		t.Fatal(s.reports[0])
	}
}
