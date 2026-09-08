package app

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSharedSkillKeepsEveryHostAndScope(t *testing.T) {
	base := t.TempDir()
	shared, codex, project := filepath.Join(base, "shared"), filepath.Join(base, "codex"), filepath.Join(base, "project")
	writeTestSkill(t, filepath.Join(shared, "demo"), "demo", "same")
	for _, path := range []string{codex, project} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(shared, "demo"), filepath.Join(path, "demo")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	roots := []scanRoot{{Path: shared, Host: "universal", Scope: "user"}, {Path: codex, Host: "codex", Scope: "user"}, {Path: project, Host: "claude", Scope: "project", Project: project}}
	var stderr bytes.Buffer
	items, failed := scan(roots, &stderr)
	if failed || len(items) != 1 || len(items[0].Bindings) != 3 {
		t.Fatalf("items=%#v failed=%v stderr=%s", items, failed, &stderr)
	}
	if got := filterSkills(items, []string{"codex"}, []string{"user"}); len(got) != 1 || len(got[0].Bindings) != 3 {
		t.Fatalf("codex binding lost: %#v", got)
	}
	if got := filterSkills(items, []string{"codex"}, []string{"project"}); len(got) != 0 {
		t.Fatalf("host and scope matched different bindings: %#v", got)
	}
	if got := filterSkills(items, []string{"claude"}, []string{"project"}); len(got) != 1 {
		t.Fatalf("project binding lost: %#v", got)
	}
}

func TestInvalidSkillRemainsInJSON(t *testing.T) {
	base := setTestHome(t)
	root := filepath.Join(base, "skills")
	writeTestSkill(t, filepath.Join(root, "broken"), "Bad_Name", "invalid")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"list", "--json", "--path", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit=%d stderr=%s", code, &stderr)
	}
	var result struct {
		Items []skillListEntry `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ReasonCode != "invalid_skill" || result.Items[0].Error == "" {
		t.Fatalf("invalid item omitted: %#v", result)
	}
}

func TestReadCommandsDoNotInitializeConfiguration(t *testing.T) {
	setTestHome(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("SKILLCTL_HOME", stateDir)
	root := t.TempDir()
	for _, args := range [][]string{{"list", "--path", root}, {"doctor", "--path", root}, {"update", "--dry-run", "--path", root}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, &stderr)
		}
		if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
			t.Fatalf("%v initialized state: %v", args, err)
		}
	}
}

func TestWellKnownDryRunDoesNotSaveBaseline(t *testing.T) {
	base := setTestHome(t)
	root := filepath.Join(base, "skills")
	writeTestSkill(t, filepath.Join(root, "demo"), "demo", "current")
	archive := makeWellKnownArchive(t, "demo", "current")
	server, _ := newWellKnownServer(t, map[string][]byte{"demo": archive})
	_, cfg := writeWellKnownTestConfig(t, base, root, server.URL, map[string]string{"demo": digestWellKnownArchive(archive)})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--config", cfg, "--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ProviderBaselines) != 0 {
		t.Fatalf("dry run saved baseline: %#v", state)
	}
	if _, err := os.Stat(state.path); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote sources.json: %v", err)
	}
}

func TestOperationLockReleasedAfterProcessExit(t *testing.T) {
	if os.Getenv("SKILLCTL_TEST_LOCK_CHILD") == "1" {
		if _, err := acquireCommandLock(); err != nil {
			os.Exit(19)
		}
		os.Exit(0) // deliberately bypass release
	}
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestOperationLockReleasedAfterProcessExit$")
	cmd.Env = append(os.Environ(), "SKILLCTL_TEST_LOCK_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	lock, err := acquireCommandLock()
	if err != nil {
		t.Fatalf("crashed owner left stale lock: %v", err)
	}
	defer lock.release()
	if next, err := acquireCommandLock(); err == nil {
		next.release()
		t.Fatal("two writers acquired lock")
	}
}

func TestGlobalSharedDirectoryListsImplicitConsumers(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	writeTestSkill(t, filepath.Join(home, ".agents", "skills", "demo"), "demo", "shared")
	for _, host := range []string{"codex", "cursor", "copilot", "gemini", "opencode"} {
		result := runV2(t, "", "list", "demo", "--host", host)
		if len(result.Items) != 1 || len(result.Items[0].Bindings) < 5 {
			t.Fatalf("%s missing shared consumers: %#v", host, result.Items)
		}
	}
}

func TestDoctorReportsCorruptStateWithoutChangingIt(t *testing.T) {
	home, state, config := lifecycleFixture(t)
	writeTestSkill(t, filepath.Join(home, "codex", "demo"), "demo", "valid")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sources.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := fingerprint(path)
	var out, errout bytes.Buffer
	if code := run([]string{"doctor", "--config", config, "--json-version", "2"}, &out, &errout); code != 1 {
		t.Fatalf("wrong diagnostic exit: %d %s %s", code, &out, &errout)
	}
	var result commandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || len(result.Diagnostics) == 0 {
		t.Fatalf("lost inventory on corrupt source: %#v", result)
	}
	after, _ := fingerprint(path)
	if after != before {
		t.Fatal("doctor rewrote corrupt state")
	}
}

func TestVersionAndCompletionUseJSONV2(t *testing.T) {
	setTestHome(t)
	for _, args := range [][]string{{"version"}, {"completion", "bash"}} {
		result := runV2(t, "", args...)
		if result.SchemaVersion != 2 || result.Result == nil {
			t.Fatalf("not v2: %#v", result)
		}
	}
}
