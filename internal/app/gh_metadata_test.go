package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGHSkillClaimPreservesYAMLProviderMetadata(t *testing.T) {
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

	claim := readGHSkillClaim(skill{Name: "yaml-skill", Path: dir})
	if !claim.Found || claim.Err != nil || !claim.Claim.Pinned || claim.Claim.Repository != "example/skills" {
		t.Fatalf("unexpected GitHub claim: %#v", claim)
	}
}
