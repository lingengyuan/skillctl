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
  github-pinned: v1.2.0
  name: must-not-override
---
body
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	claim := readGHSkillClaim(skill{Name: "yaml-skill", Path: dir})
	if !claim.Found || claim.Err != nil || claim.Claim.Pinned != "v1.2.0" || claim.Claim.Repository != "example/skills" {
		t.Fatalf("unexpected GitHub claim: %#v", claim)
	}
}

// A pinned ref must not be reported as updatable, and a boolean true must keep
// working: both spellings pin the skill, only the ref names which revision.
func TestGHSkillClaimPinnedRefShapes(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"ref string", "v1.2.0", "v1.2.0"},
		{"branch-like ref", "release/2.x", "release/2.x"},
		{"boolean true", "true", "true"},
		{"boolean false", "false", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			content := "---\nname: pinned-skill\ndescription: d\nmetadata:\n  github-repo: example/skills\n  github-ref: main\n  github-tree-sha: \"0123456789012345678901234567890123456789\"\n  github-path: skills/pinned-skill/SKILL.md\n  github-pinned: " + tc.value + "\n---\nbody\n"
			file := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			claim := readGHSkillClaim(skill{Name: "pinned-skill", Path: dir})
			if claim.Err != nil {
				t.Fatalf("unexpected error: %v", claim.Err)
			}
			if claim.Claim.Pinned != tc.want {
				t.Fatalf("github-pinned %q: got %q, want %q", tc.value, claim.Claim.Pinned, tc.want)
			}
		})
	}
}
