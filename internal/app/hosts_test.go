package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Recorded installs contribute discovery roots that point at individual skills
// (for example ~/.kiro/skills/tdd). Those must never be treated as install
// targets, or every Agent holding existing installs becomes ambiguous and
// cannot receive a new skill.
func TestInstallTargetIgnoresRecordedSkillRoots(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	kiro := filepath.Join(home, ".kiro", "skills")
	if err := os.MkdirAll(kiro, 0o755); err != nil {
		t.Fatal(err)
	}
	// One existing install, expressed the way the scanner records it.
	writeTestSkill(t, filepath.Join(kiro, "tdd"), "tdd", "body")

	roots := []scanRoot{
		{Path: kiro, Host: "kiro", Scope: "user"},
		{Path: filepath.Join(kiro, "tdd"), Host: "kiro", Scope: "user", Required: true},
	}
	bindings, err := targetBindings(options{Hosts: []string{"kiro"}}, roots, "new-skill")
	if err != nil {
		t.Fatalf("a recorded skill directory broke target selection: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Path != filepath.Join(kiro, "new-skill") {
		t.Fatalf("unexpected binding: %#v", bindings)
	}
}

// A skill directory is recognised by its SKILL.md; a container directory is
// recognised by the absence of one.
func TestIsAgentRootDirectory(t *testing.T) {
	base := t.TempDir()
	container := filepath.Join(base, "skills")
	skill := filepath.Join(container, "demo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: demo\ndescription: d\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !isAgentRootDirectory(container) {
		t.Fatal("container directory was not recognised as an Agent root")
	}
	if isAgentRootDirectory(skill) {
		t.Fatal("skill directory was mistaken for an Agent root")
	}
	if isAgentRootDirectory("") {
		t.Fatal("empty path accepted")
	}
}

// Every Agent listed by config hosts must accept a user- and project-scope
// install, so the list never advertises something that cannot be used.
func TestListedHostsAllAcceptInstalls(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	// Use the same root pipeline as a real command, so the extended Agents are
	// present exactly as they are in production.
	configured, _, _, _, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	roots := augmentRoots(configured, options{Project: project})
	rows := installedHostRows()
	if len(rows) < 10 {
		t.Fatalf("expected the extended Agents to be listed, got %d", len(rows))
	}
	for _, row := range rows {
		if row.Name == "universal" {
			t.Fatal("shared-directory alias was listed as a target")
		}
		if strings.TrimSpace(row.ProjectPath) == "" {
			t.Fatalf("host %s has no project path", row.Name)
		}
		if _, err := targetBindings(options{Hosts: []string{row.Name}}, roots, "demo"); err != nil {
			t.Fatalf("host %s cannot receive a user install: %v", row.Name, err)
		}
		if _, err := targetBindings(options{Hosts: []string{row.Name}, Scopes: []string{"project"}, Project: project}, roots, "demo"); err != nil {
			t.Fatalf("host %s cannot receive a project install: %v", row.Name, err)
		}
	}
}

// config hosts must print a project path for every Agent it lists, since an
// empty column means the Agent silently cannot do a project install.
func TestConfigHostsReportsProjectPaths(t *testing.T) {
	setTestHome(t)
	t.Setenv("SKILLCTL_HOME", t.TempDir())
	for _, row := range installedHostRows() {
		if strings.TrimSpace(row.ProjectPath) == "" {
			t.Fatalf("host %s has no project path", row.Name)
		}
	}
}
