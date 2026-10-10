package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reinstallFixture installs one skill, then deletes the stored content so the
// recorded install survives without it.
func reinstallFixture(t *testing.T) (config, source, state string) {
	t.Helper()
	home := setTestHome(t)
	state = t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	source = filepath.Join(home, "src", "demo")
	writeTestSkill(t, source, "demo", "body")
	root := filepath.Join(home, "skills")
	config = filepath.Join(home, "config.toml")
	content := fmt.Sprintf("[[roots]]\npath=%q\nhost='codex'\nscope='user'\n", filepath.ToSlash(root))
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := run([]string{"install", source, "--host", "codex", "--config", config}, &out, &errout); code != 0 {
		t.Fatalf("install failed (%d): %s %s", code, out.String(), errout.String())
	}
	store := filepath.Join(state, "store")
	entries, err := os.ReadDir(store)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no stored content: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(store, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return config, source, state
}

// A skill whose recorded source is known must be installed again instead of
// only having its dangling links cleared.
func TestDoctorReinstallRestoresMissingContent(t *testing.T) {
	config, _, _ := reinstallFixture(t)

	var out, errout bytes.Buffer
	if code := run([]string{"doctor", "--fix", "--reinstall", "--config", config}, &out, &errout); code != 0 {
		t.Fatalf("doctor failed (%d): %s %s", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "skill_reinstalled") {
		t.Fatalf("skill was not reinstalled: %s", out.String())
	}
	// The binding must resolve again and hold the original content.
	var list bytes.Buffer
	if code := run([]string{"list", "--config", config}, &list, &errout); code != 0 {
		t.Fatalf("list failed (%d): %s", code, errout.String())
	}
	if !strings.Contains(list.String(), "demo") {
		t.Fatalf("skill missing after reinstall: %s", list.String())
	}
}

// --reinstall without --fix must be refused rather than silently ignored.
func TestReinstallRequiresFix(t *testing.T) {
	setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	var out, errout bytes.Buffer
	if code := run([]string{"doctor", "--reinstall"}, &out, &errout); code == 0 {
		t.Fatal("--reinstall without --fix was accepted")
	}
	if !strings.Contains(errout.String(), "--reinstall requires --fix") {
		t.Fatalf("unhelpful error: %s", errout.String())
	}
}

// --reinstall is only meaningful for doctor.
func TestReinstallOnlyValidWithDoctor(t *testing.T) {
	setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	var out, errout bytes.Buffer
	if code := run([]string{"list", "--fix", "--reinstall"}, &out, &errout); code == 0 {
		t.Fatal("--reinstall was accepted for list")
	}
}

// A dry run must not install anything, so a preview of the repair is safe.
func TestDoctorReinstallDryRunInstallsNothing(t *testing.T) {
	config, _, state := reinstallFixture(t)
	before, err := os.ReadDir(filepath.Join(state, "store"))
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	run([]string{"doctor", "--fix", "--reinstall", "--dry-run", "--config", config}, &out, &errout)
	after, err := os.ReadDir(filepath.Join(state, "store"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("--dry-run changed stored content: %d -> %d", len(before), len(after))
	}
}

// A skill with no recorded source must be reported, never guessed at.
func TestDoctorReinstallReportsMissingSource(t *testing.T) {
	home := setTestHome(t)
	state := t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	// A dangling link with no install record behind it.
	shared := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".kiro", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "orphan"), filepath.Join(home, ".kiro", "skills", "orphan")); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	run([]string{"doctor", "--fix", "--reinstall", "--path", filepath.Join(home, ".kiro", "skills")}, &out, &errout)
	if !strings.Contains(out.String(), "reinstall_source_missing") {
		t.Fatalf("missing source was not reported: %s", out.String())
	}
}

// Several Agents sharing one physical path must not produce overlapping
// operations when the skill is restored.
func TestDoctorReinstallDeduplicatesSharedPaths(t *testing.T) {
	home := setTestHome(t)
	state := t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	shared := filepath.Join(home, ".agents", "skills")
	writeTestSkill(t, filepath.Join(shared, "demo"), "demo", "body")
	config := filepath.Join(home, "config.toml")
	// Two hosts pointing at the same directory, as a shared root does.
	content := fmt.Sprintf("[[roots]]\npath=%q\nhost='codex'\nscope='user'\n[[roots]]\npath=%q\nhost='cursor'\nscope='user'\n",
		filepath.ToSlash(shared), filepath.ToSlash(shared))
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	run([]string{"doctor", "--fix", "--config", config}, &out, &errout)
	if strings.Contains(errout.String(), "overlapping operation paths") {
		t.Fatalf("shared path produced overlapping operations: %s", errout.String())
	}
}
