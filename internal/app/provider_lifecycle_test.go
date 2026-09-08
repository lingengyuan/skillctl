package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestExistingProviderCopiesUpdateAndRollback(t *testing.T) {
	home, state, config := lifecycleFixture(t)
	root := filepath.Join(home, "codex")
	installed := filepath.Join(root, "demo")
	writeTestSkill(t, installed, "demo", "original")
	oldDigest := "sha256:" + strings.Repeat("a", 64)
	archives := map[string][]byte{"demo": makeWellKnownArchive(t, "demo", "updated")}
	server, _ := newWellKnownServer(t, archives)
	originalConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	lock, _ := writeWellKnownTestConfig(t, home, root, server.URL, map[string]string{"demo": oldDigest})
	content := string(originalConfig) + fmt.Sprintf("[[manifests]]\nkind='vercel-skills-lock-v3'\npath=%q\ninstall_root=%q\n", filepath.ToSlash(lock), filepath.ToSlash(root))
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	seedWellKnownBaselines(t, root, map[string]string{"demo": oldDigest})
	runV2(t, config, "enable", "demo", "--host", "claude", "--copy")
	before := runV2(t, config, "list").Items[0]
	if before.Owner != "provider" || before.Provider != "vercel-skills-lock-v3" || len(before.Bindings) != 2 {
		t.Fatalf("owner changed: %#v", before)
	}
	originalUpdater := runWellKnownUpdater
	t.Cleanup(func() { runWellKnownUpdater = originalUpdater })
	calls := 0
	runWellKnownUpdater = func(_ context.Context, _ wellKnownUpdateRequest, _ io.Writer) (string, error) {
		calls++
		writeTestSkill(t, installed, "demo", "updated")
		updateWellKnownTestLock(t, lock, server.URL, archives)
		return "updated", nil
	}
	runV2(t, config, "pin", "demo")
	runV2(t, config, "update", "demo")
	if calls != 0 {
		t.Fatal("pin did not protect native source")
	}
	runV2(t, config, "unpin", "demo")
	preview := runV2(t, config, "update", "demo", "--dry-run")
	if preview.Plan == nil || len(preview.Plan.Changes) == 0 || len(preview.Plan.Affected) < 3 {
		t.Fatalf("provider preview omitted scope: %#v", preview.Plan)
	}
	updated := runV2(t, config, "update", "demo")
	if calls != 1 || updated.Operation == nil || updated.Operation.State != "committed" || len(updated.Operations) != 1 {
		t.Fatalf("operation result missing: %#v", updated)
	}
	if len(updated.Items) != 1 || updated.Items[0].Digest == before.Digest || updated.Items[0].Owner != "provider" {
		t.Fatalf("stale or changed owner: %#v", updated.Items)
	}
	for _, host := range []string{"codex", "claude"} {
		data, err := os.ReadFile(filepath.Join(home, host, "demo", "SKILL.md"))
		if err != nil || !strings.Contains(string(data), "updated") {
			t.Fatalf("%s stale: %s %v", host, data, err)
		}
	}
	runV2(t, config, "rollback", updated.Operation.ID)
	for _, host := range []string{"codex", "claude"} {
		hash, err := fsutil.HashDirectory(filepath.Join(home, host, "demo"))
		if err != nil || hash != before.Digest {
			t.Fatalf("%s rollback failed: %s %v", host, hash, err)
		}
	}
	writeTestSkill(t, filepath.Join(home, "claude", "demo"), "demo", "local work")
	beforeState, _ := fingerprint(filepath.Join(state, "inventory.json"))
	var out, errout strings.Builder
	if code := run([]string{"update", "demo", "--config", config, "--json-version", "2"}, &out, &errout); code != 1 {
		t.Fatalf("modified copy accepted: %d %s %s", code, &out, &errout)
	}
	afterState, _ := fingerprint(filepath.Join(state, "inventory.json"))
	if calls != 1 || beforeState != afterState {
		t.Fatal("blocked update changed provider or catalog")
	}
}

func TestProviderRemovalPreservesOtherMetadataAndRollback(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	root := filepath.Join(home, "codex")
	writeTestSkill(t, filepath.Join(root, "demo"), "demo", "original")
	originalConfig, _ := os.ReadFile(config)
	lock, _ := writeWellKnownTestConfig(t, home, root, "https://example.test", map[string]string{"demo": "sha256:" + strings.Repeat("a", 64), "other": "sha256:" + strings.Repeat("b", 64)})
	var object map[string]any
	data, _ := os.ReadFile(lock)
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["futureMetadata"] = map[string]any{"keep": "yes"}
	data, _ = json.Marshal(object)
	if err := os.WriteFile(lock, data, 0600); err != nil {
		t.Fatal(err)
	}
	original, _ := fingerprint(lock)
	content := string(originalConfig) + fmt.Sprintf("[[manifests]]\nkind='vercel-skills-lock-v3'\npath=%q\ninstall_root=%q\n", filepath.ToSlash(lock), filepath.ToSlash(root))
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	removed := runV2(t, config, "remove", "demo")
	data, _ = os.ReadFile(lock)
	object = nil
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	entries := object["skills"].(map[string]any)
	if entries["demo"] != nil || entries["other"] == nil || object["futureMetadata"] == nil {
		t.Fatalf("incorrect provider mutation: %s", data)
	}
	runV2(t, config, "rollback", removed.Operation.ID)
	restored, _ := fingerprint(lock)
	if restored != original {
		t.Fatal("provider metadata not restored exactly")
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorPreservesDisabledTrackedSource(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	installed := filepath.Join(home, "codex", "demo")
	writeTestSkill(t, installed, "demo", "local")
	digest, _ := fsutil.HashDirectory(installed)
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	state.Skills = []trackedEntry{{Path: installed, Source: "https://example.test/source.git", SkillPath: "demo", InstalledHash: digest}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	runV2(t, config, "disable", "demo")
	runV2(t, config, "doctor", "--fix")
	saved, err := loadTrackedState()
	if err != nil || len(saved.Skills) != 1 {
		t.Fatalf("disabled source deleted: %#v %v", saved, err)
	}
	runV2(t, config, "remove", "demo")
	saved, err = loadTrackedState()
	if err != nil || len(saved.Skills) != 0 {
		t.Fatalf("removed source retained: %#v %v", saved, err)
	}
}
