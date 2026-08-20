package main

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

	document, err := readSkillDocument(file)
	if err != nil {
		t.Fatal(err)
	}
	if document.Name != "yaml-skill" || document.Description != "first line second line" {
		t.Fatalf("unexpected document: %#v", document)
	}
	if name, err := readSkill(file); err != nil || name != "yaml-skill" {
		t.Fatalf("readSkill = %q, %v", name, err)
	}
	claim := readGHSkillClaim(skill{Name: "yaml-skill", Path: dir})
	if !claim.Found || claim.Err != nil || !claim.Claim.Pinned || claim.Claim.Repository != "example/skills" {
		t.Fatalf("unexpected GitHub claim: %#v", claim)
	}
}

func TestReadSkillDocumentRejectsMalformedFrontMatter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(file, []byte("---\nname: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSkill(file); err == nil {
		t.Fatal("malformed YAML unexpectedly succeeded")
	}
}

func TestReadSkillDocumentKeepsIndentedYAMLDelimiter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\nname: yaml-skill\ndescription: |\n  ---\n---\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := readSkillDocument(file)
	if err != nil {
		t.Fatal(err)
	}
	if document.Description != "---" {
		t.Fatalf("description=%q", document.Description)
	}
}
