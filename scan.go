package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// skill is one installed instance. Path is kept as the canonical path for
// compatibility with the v0.1 explicit-track state.
type skill struct {
	Name       string
	Path       string
	Aliases    []string
	ScanRoot   string
	Host       string
	Scope      string
	Broken     bool
	LinkTarget string
}

func scan(roots []scanRoot, _ bool, stderr io.Writer) ([]skill, bool) {
	seen := map[string]int{}
	visitedDirs := map[string]string{}
	var skills []skill
	failed := false
	for _, rootSpec := range roots {
		root := rootSpec.Path
		if shouldIgnoreScanEntry(filepath.Base(filepath.Clean(root))) {
			continue
		}
		_, err := os.Stat(root)
		if err != nil {
			if !rootSpec.Required && errors.Is(err, os.ErrNotExist) {
				continue
			}
			fmt.Fprintf(stderr, "%s: %v\n", root, err)
			failed = true
			continue
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			realRoot = root
		}
		if absolute, absErr := filepath.Abs(realRoot); absErr == nil {
			realRoot = absolute
		}
		err = walkFollowingLinks(root, visitedDirs, func(path, real string) (bool, error) {
			dir := filepath.Dir(path)
			name, err := readSkill(path)
			if err != nil {
				fmt.Fprintf(stderr, "%s: skipped (%v)\n", path, err)
				return false, nil
			}
			key := canonicalPathKey(real)
			if index, ok := seen[key]; ok {
				skills[index].Aliases = appendUnique(skills[index].Aliases, dir)
				return true, nil
			}
			seen[key] = len(skills)
			skills = append(skills, skill{Name: name, Path: real, Aliases: []string{dir}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope})
			return true, nil
		}, func(path, target string) {
			name := filepath.Base(path)
			skills = append(skills, skill{Name: name, Path: path, Aliases: []string{path}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope, Broken: true, LinkTarget: target})
		}, func(alias, canonical string) {
			addAliasesForVisitedDir(skills, alias, canonical)
		})
		if err != nil {
			fmt.Fprintf(stderr, "%s: scan failed: %v\n", root, err)
			failed = true
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			return skills[i].Path < skills[j].Path
		}
		return skills[i].Name < skills[j].Name
	})
	return skills, failed
}

func uniqueSkillCount(skills []skill) int {
	seen := make(map[string]struct{}, len(skills))
	for _, item := range skills {
		seen[item.Name] = struct{}{}
	}
	return len(seen)
}

func addAliasesForVisitedDir(skills []skill, alias, canonical string) {
	for i := range skills {
		if !within(canonical, skills[i].Path) {
			continue
		}
		rel, err := filepath.Rel(canonical, skills[i].Path)
		if err == nil {
			skills[i].Aliases = appendUnique(skills[i].Aliases, filepath.Join(alias, rel))
		}
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if samePath(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func canonicalPathKey(path string) string {
	clean := filepath.Clean(path)
	if filepath.Separator == '\\' {
		return strings.ToLower(clean)
	}
	return clean
}

func shouldIgnoreScanEntry(name string) bool {
	return shouldIgnoreSkillContent(name)
}

// walkFollowingLinks finds the nearest skill roots while following directory
// links safely. Once a valid SKILL.md is found, its content directory is not
// traversed again: references, assets, node_modules, and other skill payloads
// cannot contain separate installations from the scanner's point of view.
func walkFollowingLinks(dir string, visited map[string]string, visitSkill func(string, string) (bool, error), visitBroken func(string, string), visitAlias func(string, string)) error {
	key, canonical, err := identifyDirectory(dir)
	if err != nil {
		return err
	}
	if existing, found := visited[key]; found {
		visitAlias(dir, existing)
		return nil
	}
	visited[key] = canonical
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// Inspect the marker before descending so a large installed skill is treated
	// as one unit instead of an additional recursive search root.
	for _, entry := range entries {
		if entry.Name() != "SKILL.md" || shouldIgnoreScanEntry(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Stat(path)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				target := ""
				if link, linkErr := os.Readlink(path); linkErr == nil {
					target = link
					if !filepath.IsAbs(target) {
						target = filepath.Join(dir, target)
					}
				}
				visitBroken(path, target)
				continue
			}
			return statErr
		}
		if info.IsDir() {
			continue
		}
		found, visitErr := visitSkill(path, canonical)
		if visitErr != nil {
			return visitErr
		}
		if found {
			return nil
		}
	}

	for _, entry := range entries {
		if shouldIgnoreScanEntry(entry.Name()) || entry.Name() == "SKILL.md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		isDir := entry.IsDir()
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					target := ""
					if link, linkErr := os.Readlink(path); linkErr == nil {
						target = link
						if !filepath.IsAbs(target) {
							target = filepath.Join(dir, target)
						}
					}
					visitBroken(path, target)
					continue
				}
				return statErr
			}
			isDir = info.IsDir()
		}
		if !isDir {
			continue
		}
		if err := walkFollowingLinks(path, visited, visitSkill, visitBroken, visitAlias); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				visitBroken(path, "")
				continue
			}
			return err
		}
	}
	return nil
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func readSkill(path string) (string, error) {
	document, err := readSkillDocument(path)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(document.Name)
	if name == "" {
		return "", errors.New("missing name")
	}
	if len(name) > 64 {
		return "", errors.New("name exceeds 64 characters")
	}
	if !skillName.MatchString(name) {
		return "", fmt.Errorf("invalid name %q: use lowercase letters, numbers, and hyphens", name)
	}
	if strings.TrimSpace(document.Description) == "" {
		return "", errors.New("missing description")
	}
	return name, nil
}
