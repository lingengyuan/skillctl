#!/usr/bin/env bash
set -euo pipefail

python3 - <<'PY'
from pathlib import Path
import re

scan = Path('scan.go')
text = scan.read_text().replace('\t"bufio"\n', '')
pattern = re.compile(r'func readSkill\(path string\) \(string, error\) \{.*?\n\}', re.S)
replacement = '''func readSkill(path string) (string, error) {
\tdocument, err := readSkillDocument(path)
\tif err != nil {
\t\treturn "", err
\t}
\tname := document.Name
\tif name == "" {
\t\treturn "", errors.New("missing name")
\t}
\tif len(name) > 64 {
\t\treturn "", errors.New("name exceeds 64 characters")
\t}
\tif !skillName.MatchString(name) {
\t\treturn "", fmt.Errorf("invalid name %q: use lowercase letters, numbers, and hyphens", name)
\t}
\tif strings.TrimSpace(document.Description) == "" {
\t\treturn "", errors.New("missing description")
\t}
\treturn name, nil
}'''
text, count = pattern.subn(replacement, text, count=1)
if count != 1:
    raise SystemExit('could not replace readSkill')
scan.write_text(text)

gh = Path('gh_provider.go')
text = gh.read_text().replace('\t"bufio"\n', '')
pattern = re.compile(r'func readGHSkillClaim\(item skill\) ghSkillClaimResult \{.*?\n\}\n\nfunc validGitHubTreeSHA', re.S)
replacement = '''func readGHSkillClaim(item skill) ghSkillClaimResult {
\tdocument, err := readSkillDocument(filepath.Join(item.Path, "SKILL.md"))
\tif err != nil {
\t\tif errors.Is(err, errMissingFrontMatter) {
\t\t\treturn ghSkillClaimResult{}
\t\t}
\t\treturn ghSkillClaimResult{Err: err}
\t}
\tif len(document.Metadata) == 0 {
\t\treturn ghSkillClaimResult{}
\t}
\tclaim := ghSkillClaim{
\t\tRepository: metadataString(document.Metadata, "github-repo"),
\t\tRef:        metadataString(document.Metadata, "github-ref"),
\t\tTreeSHA:    metadataString(document.Metadata, "github-tree-sha"),
\t\tSkillPath:  metadataString(document.Metadata, "github-path"),
\t\tLocalPath:  metadataString(document.Metadata, "local-path"),
\t\tPinned:     metadataBool(document.Metadata, "github-pinned"),
\t}
\tif claim.Repository == "" && claim.Ref == "" && claim.TreeSHA == "" && claim.SkillPath == "" && claim.LocalPath == "" && !claim.Pinned {
\t\treturn ghSkillClaimResult{}
\t}
\tif claim.LocalPath != "" && claim.Repository == "" && claim.Ref == "" && claim.TreeSHA == "" && claim.SkillPath == "" {
\t\treturn ghSkillClaimResult{Claim: claim, Found: true}
\t}
\tif !githubRepository.MatchString(claim.Repository) || claim.Ref == "" || !validGitHubTreeSHA(claim.TreeSHA) {
\t\treturn ghSkillClaimResult{Claim: claim, Found: true, Err: fmt.Errorf("incomplete GitHub skill metadata")}
\t}
\tif _, err := githubSkillFolder(claim.SkillPath); err != nil {
\t\treturn ghSkillClaimResult{Claim: claim, Found: true, Err: fmt.Errorf("invalid github-path")}
\t}
\treturn ghSkillClaimResult{Claim: claim, Found: true}
}

func validGitHubTreeSHA'''
text, count = pattern.subn(replacement, text, count=1)
if count != 1:
    raise SystemExit('could not replace readGHSkillClaim')
if '\t"errors"\n' not in text:
    text = text.replace('\t"context"\n', '\t"context"\n\t"errors"\n')
gh.write_text(text)
PY

cat > skill_document.go <<'EOF'
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var errMissingFrontMatter = errors.New("missing YAML front matter")

type skillDocument struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
}

func readSkillDocument(path string) (skillDocument, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return skillDocument{}, fmt.Errorf("read skill: %w", err)
	}
	frontMatter, err := extractFrontMatter(content)
	if err != nil {
		return skillDocument{}, err
	}
	var document skillDocument
	decoder := yaml.NewDecoder(bytes.NewReader(frontMatter))
	if err := decoder.Decode(&document); err != nil {
		return skillDocument{}, fmt.Errorf("invalid YAML front matter: %w", err)
	}
	return document, nil
}

func extractFrontMatter(content []byte) ([]byte, error) {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	if len(lines) == 0 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return nil, errMissingFrontMatter
	}
	for index := 1; index < len(lines); index++ {
		if bytes.Equal(bytes.TrimSpace(lines[index]), []byte("---")) {
			return bytes.Join(lines[1:index], []byte("\n")), nil
		}
	}
	return nil, errors.New("unterminated YAML front matter")
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return ""
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}
EOF

cat > skill_document_test.go <<'EOF'
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkillDocumentSupportsYAMLFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
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
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := readSkillDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.Name != "yaml-skill" || document.Description != "first line second line" {
		t.Fatalf("unexpected document: %#v", document)
	}
	if !metadataBool(document.Metadata, "github-pinned") {
		t.Fatal("boolean metadata was not parsed")
	}
	if name, err := readSkill(path); err != nil || name != "yaml-skill" {
		t.Fatalf("readSkill = %q, %v", name, err)
	}
	claim := readGHSkillClaim(skill{Name: "yaml-skill", Path: dir})
	if !claim.Found || claim.Err != nil || !claim.Claim.Pinned || claim.Claim.Repository != "example/skills" {
		t.Fatalf("unexpected GitHub claim: %#v", claim)
	}
}

func TestReadSkillDocumentRejectsMalformedFrontMatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSkill(path); err == nil {
		t.Fatal("malformed YAML unexpectedly succeeded")
	}
}
EOF

go get gopkg.in/yaml.v3@v3.0.1
gofmt -w -- *.go
go mod tidy

go test ./... -count=1 -timeout 180s
go test -race ./... -count=1 -timeout 240s
go test -tags=integration . -run '^TestIntegration' -count=1 -timeout 180s
go vet ./...
sh -n scripts/install.sh

rm -f .github/workflows/pr-hardening-apply.yml
rm -f .github/workflows/pr-hardening-apply-push.yml
rm -f .github/pr-hardening-apply.sh

git add -- go.mod go.sum scan.go gh_provider.go skill_document.go skill_document_test.go hardening_test.go
git add -u -- .github/workflows/pr-hardening-apply.yml .github/workflows/pr-hardening-apply-push.yml .github/pr-hardening-apply.sh
git diff --cached --check

if ! git diff --cached --quiet; then
	git config user.name 'Hugh Lin'
	git config user.email '1062740012@qq.com'
	git commit -m 'refactor: parse skill metadata as YAML'
	git push origin HEAD:feat/v0.3.9-hardening
fi
