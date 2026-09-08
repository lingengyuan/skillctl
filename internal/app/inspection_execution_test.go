package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestMixedUpdateCommitsHealthySourceAndReportsFailure(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	source := newTestSourceRepository(t)
	writeTestSkill(t, filepath.Join(source, "healthy"), "healthy", "old")
	commitTestSource(t, source, "old")
	installed := filepath.Join(home, "codex", "healthy")
	if err := fsutil.CopyDirectory(filepath.Join(source, "healthy"), installed); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(home, "codex", "broken")
	writeTestSkill(t, broken, "broken", "unchanged")
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"healthy": installed, "broken": broken} {
		digest, _ := fsutil.HashDirectory(dir)
		state.put(trackedEntry{Path: dir, Source: "https://example.invalid/" + name + ".git", SkillPath: name, InstalledHash: digest})
	}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, filepath.Join(source, "healthy"), "healthy", "new")
	commitTestSource(t, source, "new")
	original := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = original })
	syncSourceForSession = func(_ context.Context, url, _ string) (string, error) {
		if strings.Contains(url, "broken") {
			return "", fmt.Errorf("fixture source unavailable")
		}
		return source, nil
	}
	var out, errout strings.Builder
	code := run([]string{"update", "--config", config, "--no-history", "--json-version", "2"}, &out, &errout)
	var result commandResult
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err, &out, &errout)
	}
	if code != 1 || len(result.Operations) != 1 || result.Operations[0].State != "committed" {
		t.Fatalf("partial result lost: code=%d %+v %s", code, result, &errout)
	}
	data, _ := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if !strings.Contains(string(data), "new") {
		t.Fatal("healthy source was not updated")
	}
	data, _ = os.ReadFile(filepath.Join(broken, "SKILL.md"))
	if !strings.Contains(string(data), "unchanged") {
		t.Fatal("failed source was changed")
	}
	states := map[string]string{}
	for _, item := range result.Items {
		states[item.Name] = item.State
	}
	if states["healthy"] != "current" || states["broken"] != "error" {
		t.Fatal(states)
	}
}

func TestGitPackageScopeFiltersBeforeDuplicateNameResolution(t *testing.T) {
	source := newTestSourceRepository(t)
	for _, folder := range []string{"chosen/demo", "other/demo"} {
		writeTestSkill(t, filepath.Join(source, folder), "demo", "content")
	}
	commitTestSource(t, source, "fixture")
	original := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = original })
	syncSourceForSession = func(context.Context, string, string) (string, error) { return source, nil }
	packages, cleanup, err := prepareGitPackages(t.Context(), sourceSpec{Kind: "git", URL: source, SkillPath: "chosen"}, []string{"demo"}, false)
	defer cleanup()
	if err != nil || len(packages) != 1 || packages[0].Source.SkillPath != "chosen/demo" {
		t.Fatalf("scoped source: %+v %v", packages, err)
	}
}
