package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckFindsRecursiveLocalSkill(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	skillDir := filepath.Join(root, "nested", "install-name")
	writeTestSkill(t, skillDir, "declared-name", "local skill")
	writeTestSkill(t, filepath.Join(root, "invalid"), "invalid_name", "invalid skill")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--json", "--path", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(reports) != 1 || reports[0].Identity != "declared-name" || reports[0].Provider != "local-authoring" || reports[0].Status != "local/untracked (no update source)" {
		t.Fatalf("unexpected report: %#v", reports)
	}
	if !strings.Contains(stderr.String(), `invalid name "invalid_name"`) {
		t.Fatalf("missing invalid name reason: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check", "--path", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("text check failed (%d): %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "skillctl track --source SOURCE_URL declared-name") {
		t.Fatalf("missing track hint: %s", stdout.String())
	}
}

func TestHelpListsCommandsAndOptions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help failed (%d): %s", code, stderr.String())
	}
	for _, expected := range []string{"skillctl check", "skillctl update", "skillctl track", "--json", "--source", "Examples:"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help is missing %q: %s", expected, stdout.String())
		}
	}
}

func TestCheckRecognizesProviderMetadata(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	writeTestSkill(t, filepath.Join(root, "vercel-skill"), "vercel-skill", "vercel")
	ghDir := filepath.Join(root, "gh-skill")
	if err := os.MkdirAll(ghDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghContent := "---\nname: gh-skill\ndescription: gh\nmetadata:\n  local-path: ../source/gh-skill\n---\n"
	if err := os.WriteFile(filepath.Join(ghDir, "SKILL.md"), []byte(ghContent), 0o600); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(home, "lock.json")
	lock := `{"version":3,"skills":{"vercel-skill":{"sourceType":"local","skillPath":"vercel-skill/SKILL.md","skillFolderHash":"x"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--json", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	byName := map[string]report{}
	for _, item := range reports {
		byName[item.Identity] = item
	}
	if got := byName["vercel-skill"]; got.Provider != "vercel-skills-lock-v3" || got.Status != "unsupported source type: local" {
		t.Fatalf("unexpected Vercel report: %#v", got)
	}
	if got := byName["gh-skill"]; got.Provider != "gh-skill" || got.Status != "managed from local path" {
		t.Fatalf("unexpected GitHub CLI report: %#v", got)
	}
}

func TestCheckRecognizesCodexCuratedMetadataOnlyAfterContentVerification(t *testing.T) {
	home := setTestHome(t)
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	installedRoot := filepath.Join(codexHome, "skills")
	installed := filepath.Join(installedRoot, "curated-skill")
	source := filepath.Join(codexHome, "vendor_imports", "skills", "skills", ".curated", "curated-skill")
	writeTestSkill(t, installed, "curated-skill", "same content")
	writeTestSkill(t, source, "curated-skill", "same content")
	manifestPath := filepath.Join(codexHome, "vendor_imports", "skills-curated-cache.json")
	manifest := `{"skills":[{"name":"curated-skill","repoPath":"skills/.curated/curated-skill"}]}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"codex\"\nscope = \"user\"\n", filepath.ToSlash(installedRoot))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	check := func() report {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"check", "--json", "--config", configPath}, &stdout, &stderr); code != 0 {
			t.Fatalf("check failed (%d): %s", code, stderr.String())
		}
		var reports []report
		if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
			t.Fatal(err)
		}
		if len(reports) != 1 {
			t.Fatalf("reports = %#v", reports)
		}
		return reports[0]
	}

	if got := check(); got.Provider != "codex-curated-cache" || got.Status != "managed by codex" || got.Drift != "clean" {
		t.Fatalf("unexpected verified report: %#v", got)
	}
	writeTestSkill(t, installed, "curated-skill", "local modification")
	if got := check(); got.Provider != "local-authoring" || got.Status != "local/untracked (no update source)" {
		t.Fatalf("modified copy was claimed from metadata: %#v", got)
	}
}

func TestCheckDoesNotGuessCodexCuratedSourceFromName(t *testing.T) {
	home := setTestHome(t)
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	installedRoot := filepath.Join(codexHome, "skills")
	writeTestSkill(t, filepath.Join(installedRoot, "same-name"), "same-name", "same content")
	writeTestSkill(t, filepath.Join(codexHome, "vendor_imports", "skills", "skills", ".curated", "same-name"), "same-name", "same content")
	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"codex\"\nscope = \"user\"\n", filepath.ToSlash(installedRoot))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--json", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): %s", code, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Provider != "local-authoring" {
		t.Fatalf("source was guessed without metadata: %#v", reports)
	}
}

func TestRepositoryDecision(t *testing.T) {
	tests := []struct {
		name           string
		action         string
		allowed, dirty bool
		ahead, behind  int
		message        string
		pull           bool
	}{
		{name: "latest", action: "check", allowed: true, message: "up to date"},
		{name: "check update", action: "check", allowed: true, behind: 2, message: "update available (behind 2 commits)"},
		{name: "update", action: "update", allowed: true, behind: 1, pull: true},
		{name: "outside root", action: "update", behind: 1, message: "skipped (repository root is outside the scan path)"},
		{name: "dirty", action: "update", allowed: true, dirty: true, message: "skipped (working tree is dirty)"},
		{name: "dirty update", action: "update", allowed: true, dirty: true, behind: 1, message: "update available (behind 1 commits), skipped (working tree is dirty)"},
		{name: "ahead", action: "update", allowed: true, ahead: 1, message: "skipped (ahead by 1 commits)"},
		{name: "diverged", action: "update", allowed: true, ahead: 1, behind: 1, message: "skipped (branch has diverged)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideRepository(tt.action, tt.allowed, tt.dirty, tt.ahead, tt.behind)
			if got.message != tt.message || got.pull != tt.pull {
				t.Fatalf("decision = %#v, want message=%q pull=%v", got, tt.message, tt.pull)
			}
		})
	}
}

func TestSourceSessionDeduplicatesAndRunsConcurrently(t *testing.T) {
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	syncSourceForSession = func(_ context.Context, source, _ string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return source, nil
	}

	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	done := make(chan struct{})
	go func() {
		session.prefetch([]sourceRequest{{Source: "one"}, {Source: "two"}, {Source: "one"}})
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("source checks did not run concurrently")
		}
	}
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("sync calls = %d, want 2 unique sources", calls)
	}
}

func TestSourceSessionAppliesTimeoutPerSource(t *testing.T) {
	original := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = original })
	syncSourceForSession = func(ctx context.Context, source, _ string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			return "", errors.New("source check has no deadline")
		}
		timer := time.NewTimer(350 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return source, nil
		}
	}

	session := newSourceSession(context.Background(), 500*time.Millisecond, io.Discard)
	for _, source := range []string{"first", "second"} {
		cache, err := session.source(source, "")
		if err != nil {
			t.Fatalf("%s source check failed: %v", source, err)
		}
		if cache != source {
			t.Fatalf("%s cache = %q", source, cache)
		}
	}
}

func TestCheckReportsProgressAndHonorsTimeout(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	writeTestSkill(t, filepath.Join(root, "slow-skill"), "slow-skill", "slow")
	lockPath := filepath.Join(home, "lock.json")
	lock := `{"version":3,"skills":{"slow-skill":{"sourceType":"github","sourceUrl":"https://example.invalid/slow.git","skillPath":"skills/slow-skill/SKILL.md","skillFolderHash":"0000000000000000000000000000000000000000"}}}`
	if err := os.WriteFile(lockPath, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()
	syncSourceForSession = func(ctx context.Context, _, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	var stdout, stderr bytes.Buffer
	started := time.Now()
	code := run([]string{"check", "--timeout", "25ms", "--config", configPath}, &stdout, &stderr)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout returned after %s", elapsed)
	}
	if code != 1 || !strings.Contains(stderr.String(), "Checking remote source") || !strings.Contains(stdout.String()+stderr.String(), "timeout") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestUpdateAppliesTimeoutPerProviderOperation(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	sourceRoot := filepath.Join(home, "source")
	installed := map[string]string{}
	remoteHashes := map[string]string{}
	lock := vercelLock{Version: 3, Skills: map[string]vercelLockEntry{}}
	for _, name := range []string{"first-skill", "second-skill"} {
		installed[name] = filepath.Join(root, name)
		writeTestSkill(t, installed[name], name, "old")
		installedHash, err := hashDirectory(installed[name])
		if err != nil {
			t.Fatal(err)
		}
		remote := filepath.Join(sourceRoot, name)
		writeTestSkill(t, remote, name, "new")
		remoteHashes[name], err = hashDirectory(remote)
		if err != nil {
			t.Fatal(err)
		}
		lock.Skills[name] = vercelLockEntry{
			SourceType:      "git",
			SourceURL:       "fixture",
			SkillPath:       filepath.ToSlash(filepath.Join(name, "SKILL.md")),
			SkillFolderHash: installedHash,
		}
	}

	lockPath := filepath.Join(home, ".agents", ".skill-lock.json")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLock := func(value vercelLock) error {
		content, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(lockPath, content, 0o600)
	}
	if err := writeLock(lock); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	originalSync := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = originalSync })
	syncSourceForSession = func(context.Context, string, string) (string, error) {
		return sourceRoot, nil
	}
	originalUpdater := runVercelUpdater
	t.Cleanup(func() { runVercelUpdater = originalUpdater })
	runVercelUpdater = func(ctx context.Context, request vercelUpdateRequest, _ io.Writer) (string, error) {
		timer := time.NewTimer(350 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
		writeTestSkill(t, installed[request.Name], request.Name, "new")
		content, err := os.ReadFile(lockPath)
		if err != nil {
			return "", err
		}
		var current vercelLock
		if err := json.Unmarshal(content, &current); err != nil {
			return "", err
		}
		entry := current.Skills[request.Name]
		entry.SkillFolderHash = remoteHashes[request.Name]
		current.Skills[request.Name] = entry
		if err := writeLock(current); err != nil {
			return "", err
		}
		return "updated", nil
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--timeout", "500ms", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("update failed (%d):\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	for name, installedPath := range installed {
		got, err := hashDirectory(installedPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != remoteHashes[name] {
			t.Fatalf("%s was not updated", name)
		}
	}
}

func TestTrackedUpdateSafety(t *testing.T) {
	t.Run("updates clean copy", func(t *testing.T) {
		item, state, session, installed := newTrackedFixture(t)
		var stdout, stderr bytes.Buffer
		if processTracked("update", []skill{item}, state, session, &stdout, &stderr) {
			t.Fatalf("update failed: %s", stderr.String())
		}
		if _, err := os.Stat(filepath.Join(installed, "new.txt")); err != nil || !strings.Contains(stdout.String(), "updated") {
			t.Fatalf("clean copy was not updated: %v, %s", err, stdout.String())
		}
	})

	t.Run("keeps local changes", func(t *testing.T) {
		item, state, session, installed := newTrackedFixture(t)
		if err := os.WriteFile(filepath.Join(installed, "local.txt"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if processTracked("update", []skill{item}, state, session, &stdout, &stderr) {
			t.Fatalf("update failed: %s", stderr.String())
		}
		if _, err := os.Stat(filepath.Join(installed, "new.txt")); !os.IsNotExist(err) || !strings.Contains(stdout.String(), "local files were modified") {
			t.Fatalf("local copy was overwritten: %v, %s", err, stdout.String())
		}
	})

	t.Run("rolls back when state cannot be saved", func(t *testing.T) {
		item, state, session, installed := newTrackedFixture(t)
		state.path = filepath.Join(filepath.Dir(state.path), "state-directory")
		if err := os.MkdirAll(state.path, 0o755); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if !processTracked("update", []skill{item}, state, session, &stdout, &stderr) {
			t.Fatal("update unexpectedly succeeded")
		}
		if _, err := os.Stat(filepath.Join(installed, "new.txt")); !os.IsNotExist(err) || !strings.Contains(stderr.String(), "save source state") {
			t.Fatalf("failed update was not rolled back: %v, %s", err, stderr.String())
		}
	})
}

func TestVercelUpdateRollsBackOnProviderFailure(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "demo")
	writeTestSkill(t, installed, "demo", "original")
	originalSkill, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "lock.json")
	originalLock := []byte(`{"version":3,"skills":{"demo":{"sourceType":"github"}}}`)
	if err := os.WriteFile(manifestPath, originalLock, 0o600); err != nil {
		t.Fatal(err)
	}

	originalUpdater := runVercelUpdater
	t.Cleanup(func() { runVercelUpdater = originalUpdater })
	runVercelUpdater = func(context.Context, vercelUpdateRequest, io.Writer) (string, error) {
		if err := os.RemoveAll(installed); err != nil {
			return "", err
		}
		if err := os.WriteFile(manifestPath, []byte("changed"), 0o600); err != nil {
			return "", err
		}
		return "", errors.New("provider failed")
	}

	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	claim := vercelClaim{Name: "demo", ManifestPath: manifestPath, Entry: vercelLockEntry{SourceType: "github"}}
	if _, err := updateVercelProvider(context.Background(), session, skill{Name: "demo", Path: installed}, claim, io.Discard); err == nil {
		t.Fatal("provider failure unexpectedly succeeded")
	}
	assertFileContent(t, filepath.Join(installed, "SKILL.md"), originalSkill)
	assertFileContent(t, manifestPath, originalLock)
}

func TestGHSkillUpdateRollsBackOnProviderFailure(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "demo")
	writeTestSkill(t, installed, "demo", "original")
	originalSkill, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	originalUpdater := runGHSkillUpdater
	t.Cleanup(func() { runGHSkillUpdater = originalUpdater })
	runGHSkillUpdater = func(context.Context, ghSkillUpdateRequest, io.Writer) (string, error) {
		if err := os.RemoveAll(installed); err != nil {
			return "", err
		}
		return "", errors.New("provider failed")
	}

	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	if _, err := updateGHSkillProvider(context.Background(), session, skill{Name: "demo", Path: installed}, ghSkillClaim{}, io.Discard); err == nil {
		t.Fatal("provider failure unexpectedly succeeded")
	}
	assertFileContent(t, filepath.Join(installed, "SKILL.md"), originalSkill)
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("%s = %q, want %q", path, actual, expected)
	}
}

func TestExplicitConfigRejectsUnknownFields(t *testing.T) {
	home := setTestHome(t)
	path := filepath.Join(home, "bad.toml")
	if err := os.WriteFile(path, []byte("pahts = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--config", path}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func BenchmarkScanUnmanagedSkills(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 100; i++ {
		writeBenchmarkSkill(b, filepath.Join(root, fmt.Sprintf("skill-%03d", i)), fmt.Sprintf("skill-%03d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items, failed := scan([]scanRoot{{Path: root}}, false, io.Discard)
		if failed || len(items) != 100 {
			b.Fatalf("items=%d failed=%v", len(items), failed)
		}
	}
}

func newTrackedFixture(t *testing.T) (skill, *trackedState, *sourceSession, string) {
	t.Helper()
	dir := t.TempDir()
	installed := filepath.Join(dir, "installed", "demo")
	writeTestSkill(t, installed, "demo", "old")
	installedHash, err := hashDirectory(installed)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(dir, "source")
	remoteSkill := filepath.Join(sourceRoot, "demo")
	writeTestSkill(t, remoteSkill, "demo", "new")
	if err := os.WriteFile(filepath.Join(remoteSkill, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	item := skill{Name: "demo", Path: installed}
	state := &trackedState{
		Version: 1,
		path:    filepath.Join(dir, "sources.json"),
		Skills: []trackedEntry{{
			Path:          installed,
			Source:        "fixture",
			SkillPath:     "demo",
			InstalledHash: installedHash,
		}},
	}
	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	session.caches[sourceKey("fixture", "")] = sourceRoot
	return item, state, session, installed
}

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func writeTestSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: test\n---\n%s\n", name, body)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBenchmarkSkill(b *testing.B, dir, name string) {
	b.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: benchmark\n---\n", name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		b.Fatal(err)
	}
}
