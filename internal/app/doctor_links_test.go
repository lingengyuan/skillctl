package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBrokenLinkSkill creates a dangling skill link under home/<host>/skills.
func writeBrokenLinkSkill(t *testing.T, home, host, name, target string) string {
	t.Helper()
	dir := filepath.Join(home, "."+host, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, name)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestDoctorFixesDanglingLink(t *testing.T) {
	home := setTestHome(t)
	state := t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	// Target parent exists, leaf is gone: content was deleted, so the link can
	// be repaired.
	shared := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	link := writeBrokenLinkSkill(t, home, "kiro", "demo", filepath.Join(shared, "demo"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"doctor", "--path", filepath.Join(home, ".kiro", "skills")}, &stdout, &stderr); code == 0 && strings.Contains(stdout.String(), "healthy") {
		t.Fatalf("dangling link was not reported: %s", stdout.String())
	}
	if !strings.Contains(stderr.String()+stdout.String(), "broken_link") {
		t.Fatalf("missing broken_link finding: %s %s", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"doctor", "--fix", "--path", filepath.Join(home, ".kiro", "skills")}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor --fix failed (%d): %s %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("dangling link was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "skills")); err != nil {
		t.Fatalf("containing directory was removed: %v", err)
	}
}

// A dangling link whose target parent is also missing may point at an
// unmounted volume. Repair must keep it, and the command must still fail
// because a real problem remains.
func TestDoctorKeepsLinkWhenTargetParentMissing(t *testing.T) {
	home := setTestHome(t)
	state := t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	link := writeBrokenLinkSkill(t, home, "kiro", "demo", filepath.Join(home, "mnt", "volume", "skills", "demo"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "--fix", "--path", filepath.Join(home, ".kiro", "skills")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("unrepaired link should still fail, got exit %d: %s %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("link to unavailable volume was removed: %v", err)
	}
	text := stdout.String()
	if !strings.Contains(text, "broken_link_kept") {
		t.Fatalf("held-back link was not explained: %s", text)
	}
	if !strings.Contains(text, "broken_link") {
		t.Fatalf("held-back link was not reported: %s", text)
	}
}

// --dry-run must never modify the filesystem.
func TestDoctorFixDryRunChangesNothing(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	shared := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	link := writeBrokenLinkSkill(t, home, "kiro", "demo", filepath.Join(shared, "demo"))

	var stdout, stderr bytes.Buffer
	run([]string{"doctor", "--fix", "--dry-run", "--path", filepath.Join(home, ".kiro", "skills")}, &stdout, &stderr)
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("--dry-run removed a link: %v", err)
	}
}

// The same link seen through several roots must be reported once, not repeated
// for every Agent that shares the directory.
func TestDoctorReportsEachDanglingLinkOnce(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	shared := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBrokenLinkSkill(t, home, "kiro", "demo", filepath.Join(shared, "demo"))

	var stdout, stderr bytes.Buffer
	run([]string{"doctor", "--path", filepath.Join(home, ".kiro", "skills")}, &stdout, &stderr)
	if count := strings.Count(stdout.String(), "broken_link:"); count != 1 {
		t.Fatalf("expected exactly one broken_link finding, got %d:\n%s", count, stdout.String())
	}
}

// Optional roots are summarized so real findings stay visible, and --quiet
// drops informational levels entirely while JSON keeps full detail.
func TestDoctorSummarizesOptionalRootsAndQuiet(t *testing.T) {
	setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())

	// The default configuration names every supported Agent, so most roots are
	// absent on any given machine.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"doctor"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor failed (%d): %s %s", code, stdout.String(), stderr.String())
	}
	plain := stdout.String()
	if count := strings.Count(plain, "optional_root_missing"); count != 1 {
		t.Fatalf("expected one summary line for optional roots, got %d:\n%s", count, plain)
	}
	if !strings.Contains(plain, "optional skill root(s) not present") {
		t.Fatalf("missing summary line:\n%s", plain)
	}

	stdout.Reset()
	run([]string{"doctor", "--quiet"}, &stdout, &stderr)
	if strings.Contains(stdout.String(), "INFO") {
		t.Fatalf("--quiet still printed INFO:\n%s", stdout.String())
	}

	stdout.Reset()
	run([]string{"doctor", "--json"}, &stdout, &stderr)
	var payload struct {
		Findings []doctorFinding `json:"findings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var optional int
	for _, finding := range payload.Findings {
		if finding.Code == "optional_root_missing" {
			optional++
		}
	}
	if optional == 0 {
		t.Fatal("JSON lost the per-root detail")
	}
}

// Listing reports what exists; --strict restores a failing exit code for CI.
func TestListQueryExitCodes(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	root := filepath.Join(home, "skills")
	writeTestSkill(t, filepath.Join(root, "broken"), "Bad_Name", "invalid")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"list", "--path", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("query exit=%d, want 0: %s", code, stderr.String())
	}
	if code := run([]string{"list", "--strict", "--path", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("strict exit=%d, want 1", code)
	}
	if code := run([]string{"list", "--json-version", "2", "--strict", "--path", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("strict v2 exit=%d, want 1", code)
	}
}

// A skill consumed by several Agents must list all of them, and the printed
// path must be one an Agent actually reads.
func TestListShowsEveryHostAndVisiblePath(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	shared := filepath.Join(home, ".agents", "skills")
	writeTestSkill(t, filepath.Join(shared, "demo"), "demo", "body")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"list", "--path", shared}, &stdout, &stderr); code != 0 {
		t.Fatalf("list failed (%d): %s", code, stderr.String())
	}
	line := strings.TrimSpace(stdout.String())
	if !strings.Contains(line, "demo [") {
		t.Fatalf("unexpected line: %s", line)
	}
	if !strings.HasSuffix(line, filepath.Join(shared, "demo")) &&
		!strings.Contains(line, filepath.Join(shared, "demo")) {
		t.Fatalf("listed path is not agent-visible: %s", line)
	}
}

// Deleting installed content must leave a way out: the skill can be removed
// and installed again afterwards.
func TestRemovingSkillWithMissingContentAndReinstalling(t *testing.T) {
	home := setTestHome(t)
	state := t.TempDir()
	t.Setenv("SKILLCTL_HOME", state)
	source := filepath.Join(home, "src", "demo")
	writeTestSkill(t, source, "demo", "body")
	config := filepath.Join(home, "config.toml")
	root := filepath.Join(home, "skills")
	content := fmt.Sprintf("[[roots]]\npath=%q\nhost='codex'\nscope='user'\n", filepath.ToSlash(root))
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	run([]string{"install", source, "--host", "codex", "--config", config}, io.Discard, io.Discard)

	// Delete the shared content, leaving recorded bindings behind.
	store := filepath.Join(state, "store")
	matches, err := filepath.Glob(filepath.Join(store, "*", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no stored content to delete: %v %v", matches, err)
	}
	for _, match := range matches {
		if err := os.RemoveAll(match); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"remove", "demo", "--config", config}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove failed on missing content (%d): %s %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"install", source, "--host", "codex", "--config", config}, &stdout, &stderr); code != 0 {
		t.Fatalf("reinstall failed after removal (%d): %s %s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	run([]string{"list", "--config", config}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "demo") {
		t.Fatalf("skill missing after reinstall: %s", stdout.String())
	}
}
