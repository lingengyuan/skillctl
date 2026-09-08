package skilldoc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkillDocumentSupportsYAMLFeatures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "SKILL.md")
	content := `---
name: yaml-skill
description: >-
  first line
  second line
metadata:
  github-repo: example/skills
  github-ref: main
  github-tree-sha: "0123456789012345678901234567890123456789"
  github-path: skills/yaml-skill/SKILL.md
  github-pinned: true
  name: must-not-override
---
body
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	document, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if document.Name != "yaml-skill" || document.Description != "first line second line" {
		t.Fatalf("unexpected document: %#v", document)
	}
	if name, err := ReadName(file); err != nil || name != "yaml-skill" {
		t.Fatalf("readSkill = %q, %v", name, err)
	}

}

func TestReadSkillDocumentRejectsMalformedFrontMatter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(file, []byte("---\nname: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadName(file); err == nil {
		t.Fatal("malformed YAML unexpectedly succeeded")
	}
}

func TestReadSkillDocumentKeepsIndentedYAMLDelimiter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\nname: yaml-skill\ndescription: |\n  ---\n---\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if document.Description != "---" {
		t.Fatalf("description=%q", document.Description)
	}
}

func TestReadSkillUsesOnlyTopLevelFrontMatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	content := `---
name: correct-name
description: valid
metadata:
  name: wrong-name
  description: wrong
---
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := ReadName(path)
	if err != nil || name != "correct-name" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}
