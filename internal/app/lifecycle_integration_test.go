package app

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func lifecycleFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := setTestHome(t)
	state := filepath.Join(home, "state")
	t.Setenv("SKILLCTL_HOME", state)
	config := filepath.Join(home, "config.toml")
	content := fmt.Sprintf("[[roots]]\npath=%q\nhost='codex'\nscope='user'\n[[roots]]\npath=%q\nhost='claude'\nscope='user'\n", filepath.ToSlash(filepath.Join(home, "codex")), filepath.ToSlash(filepath.Join(home, "claude")))
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return home, state, config
}

func runV2(t *testing.T, config string, args ...string) commandResult {
	t.Helper()
	args = append(args, "--json-version", "2")
	if config != "" {
		args = append(args, "--config", config)
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("%v: code=%d\n%s\n%s", args, code, &stdout, &stderr)
	}
	var result commandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, &stdout)
	}
	return result
}

func TestLifecycleMultipleAgentsUpdateDisableRollback(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "first revision")
	installed := runV2(t, config, "install", source, "--host", "codex", "--host", "claude")
	if installed.Operation == nil || installed.Operation.State != "committed" {
		t.Fatalf("not committed: %#v", installed)
	}
	listed := runV2(t, config, "list")
	if len(listed.Items) != 1 || len(listed.Items[0].Bindings) != 2 {
		t.Fatalf("incorrect inventory: %#v", listed.Items)
	}
	first := listed.Items[0]
	firstHash, err := fsutil.HashDirectory(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	disabled := runV2(t, config, "disable", "demo", "--host", "claude")
	if len(disabled.Items) != 1 {
		t.Fatalf("missing operation inventory: %#v", disabled)
	}
	for _, binding := range disabled.Items[0].Bindings {
		if binding.Host == "claude" && binding.Enabled {
			t.Fatal("operation returned stale enabled state")
		}
	}
	if _, err := os.Lstat(filepath.Join(home, "claude", "demo")); !os.IsNotExist(err) {
		t.Fatalf("disable left binding: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "codex", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	runV2(t, config, "enable", "demo", "--host", "claude")
	runV2(t, config, "pin", "demo")
	writeTestSkill(t, source, "demo", "second revision")
	pinned := runV2(t, config, "update", "demo")
	if len(pinned.Plan.Changes) != 0 {
		t.Fatalf("pin changed content: %#v", pinned)
	}
	runV2(t, config, "unpin", "demo")
	updated := runV2(t, config, "update", "demo")
	if updated.Operation == nil {
		t.Fatalf("update has no operation: %#v", updated)
	}
	for _, host := range []string{"codex", "claude"} {
		data, err := os.ReadFile(filepath.Join(home, host, "demo", "SKILL.md"))
		if err != nil || !strings.Contains(string(data), "second revision") {
			t.Fatalf("%s not updated: %s %v", host, data, err)
		}
	}
	runV2(t, config, "rollback", updated.Operation.ID)
	for _, host := range []string{"codex", "claude"} {
		digest, err := fsutil.HashDirectory(filepath.Join(home, host, "demo"))
		if err != nil || digest != firstHash {
			t.Fatalf("%s rollback digest: %s %v", host, digest, err)
		}
	}
}

func TestLifecycleCopyProtectsLocalModifications(t *testing.T) {
	home, state, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "original")
	runV2(t, config, "install", source, "--host", "codex", "--copy")
	path := filepath.Join(home, "codex", "demo")
	writeTestSkill(t, path, "demo", "local work")
	writeTestSkill(t, source, "demo", "remote changed")
	before, _ := fingerprint(filepath.Join(state, "inventory.json"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "demo", "--config", config, "--json-version", "2"}, &stdout, &stderr); code != 1 {
		t.Fatalf("modified copy accepted: %d %s %s", code, &stdout, &stderr)
	}
	after, _ := fingerprint(filepath.Join(state, "inventory.json"))
	if before != after {
		t.Fatal("blocked update changed management state")
	}
	data, _ := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if !strings.Contains(string(data), "local work") {
		t.Fatal("local work overwritten")
	}
}

func TestInstallDryRunDoesNotWriteStateOrTargets(t *testing.T) {
	home, state, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "original")
	planned := runV2(t, config, "install", source, "--host", "codex", "--dry-run")
	if len(planned.Plan.Changes) == 0 || planned.Operation != nil {
		t.Fatalf("invalid preview: %#v", planned)
	}
	for _, path := range []string{state, filepath.Join(home, "codex")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preview wrote %s: %v", path, err)
		}
	}
}

func TestExportFrozenSyncInTwoCleanEnvironments(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "reproducible")
	runV2(t, config, "install", source, "--host", "claude", "--copy")
	output := filepath.Join(home, "shared", "skillctl.toml")
	runV2(t, config, "export", "demo", "--output", output)
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), home) {
		t.Fatalf("absolute source leaked: %s", content)
	}
	manifest, locked, err := loadEnvironment(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Skills) != 1 || len(locked.Skills) != 1 {
		t.Fatalf("incomplete export")
	}
	for _, environment := range []string{"clean-one", "clean-two"} {
		root := filepath.Join(home, environment)
		if err := copySnapshot(filepath.Dir(output), root); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SKILLCTL_HOME", filepath.Join(home, environment+"-state"))
		path := filepath.Join(root, "skillctl.toml")
		synced := runV2(t, config, "sync", "--frozen", "--offline", "--file", path)
		if synced.Operation == nil {
			t.Fatalf("first sync did not apply: %#v", synced)
		}
		local := filepath.Join(root, ".claude", "skills", "demo")
		digest, err := fsutil.HashDirectory(local)
		if err != nil || digest != locked.Skills[0].Digest {
			t.Fatalf("wrong restored version: %s %v", digest, err)
		}
		again := runV2(t, config, "sync", "--frozen", "--offline", "--file", path)
		if again.Operation != nil || len(again.Plan.Changes) > 0 {
			t.Fatalf("sync not idempotent: %#v", again.Plan)
		}
	}
}

func TestProfilePreservesManualBinding(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	project := filepath.Join(home, "project")
	source := filepath.Join(project, "source")
	writeTestSkill(t, source, "demo", "one")
	manifest := filepath.Join(project, "skillctl.toml")
	data := "version=1\n[[skills]]\nid='demo'\nname='demo'\nsource='./source'\nkind='local'\nhosts=['claude']\nscope='project'\n[profiles.full]\nskills=['demo']\n[profiles.empty]\nskills=[]\n"
	if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	runV2(t, config, "sync", "--file", manifest, "--profile", "full")
	runV2(t, config, "profile", "use", "empty", "--file", manifest)
	if _, err := os.Lstat(filepath.Join(project, ".claude", "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("profile did not disable: %v", err)
	}
	runV2(t, config, "profile", "use", "full", "--file", manifest, "--offline")
	runV2(t, config, "install", source, "--host", "claude", "--project", project)
	runV2(t, config, "profile", "use", "empty", "--file", manifest)
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills", "demo")); err != nil {
		t.Fatalf("profile disabled manually requested binding: %v", err)
	}
}

func TestSameNameSourcesStayDistinctAndRequireSelection(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	one, two := filepath.Join(home, "one"), filepath.Join(home, "two")
	writeTestSkill(t, one, "shared", "one")
	writeTestSkill(t, two, "shared", "two")
	runV2(t, config, "install", one, "--host", "codex", "--copy")
	runV2(t, config, "install", two, "--host", "claude", "--copy")
	inventory := runV2(t, config, "list")
	if len(inventory.Items) != 2 || inventory.Items[0].ID == inventory.Items[1].ID {
		t.Fatalf("different sources merged: %#v", inventory.Items)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"remove", "shared", "--config", config}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "ambiguous") {
		t.Fatalf("ambiguous remove accepted: %d %s %s", code, &stdout, &stderr)
	}
	runV2(t, config, "remove", inventory.Items[0].ID)
	left := runV2(t, config, "list")
	if len(left.Items) != 1 {
		t.Fatalf("removed unselected source: %#v", left.Items)
	}
}

func TestSharedProjectBindingCannotDisableOnlyOneConsumer(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	project := filepath.Join(home, "project")
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "shared")
	runV2(t, config, "install", source, "--host", "codex", "--project", project)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"disable", "demo", "--host", "codex", "--project", project, "--config", config}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "shared") {
		t.Fatalf("pretended to disable one consumer: %d %s %s", code, &stdout, &stderr)
	}
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")); err != nil {
		t.Fatal("shared content was removed")
	}
}

func TestProfileSwitchesExclusiveSourcesAndSharedBindings(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	project := filepath.Join(home, "project")
	writeTestSkill(t, filepath.Join(project, "one"), "demo", "one")
	writeTestSkill(t, filepath.Join(project, "two"), "demo", "two")
	file := filepath.Join(project, "skillctl.toml")
	manifest := "version=1\n[[skills]]\nid='one'\nname='demo'\nsource='./one'\nkind='local'\nhosts=['codex']\nscope='project'\n[[skills]]\nid='two'\nname='demo'\nsource='./two'\nkind='local'\nhosts=['codex']\nscope='project'\n[profiles.one]\nskills=['one']\n[profiles.two]\nskills=['two']\n[profiles.empty]\nskills=[]\n"
	if err := os.WriteFile(file, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	runV2(t, config, "sync", "--file", file, "--profile", "one")
	runV2(t, config, "profile", "use", "two", "--file", file)
	data, err := os.ReadFile(filepath.Join(project, ".agents", "skills", "demo", "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "two") {
		t.Fatalf("profile did not switch source: %s %v", data, err)
	}
	runV2(t, config, "profile", "use", "empty", "--file", file)
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("shared profile binding not disabled: %v", err)
	}
}
