package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	Bindings   []skillBinding
	Invalid    string
	IssueCode  string
}

// A binding is a host-visible path, independent of the physical content it
// exposes. Several hosts may use one directory, and one host may use several
// versions in different projects.
type skillBinding struct {
	Path    string `json:"path"`
	Host    string `json:"host"`
	Scope   string `json:"scope"`
	Project string `json:"project,omitempty"`
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
}

func scan(roots []scanRoot, stderr io.Writer) ([]skill, bool) {
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
			skills = append(skills, skill{Name: filepath.Base(root), Path: root, ScanRoot: root, Host: rootSpec.Host, Scope: rootSpec.Scope, Invalid: err.Error(), IssueCode: "root_unavailable"})
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
			invalid := ""
			if err != nil {
				fmt.Fprintf(stderr, "%s: invalid (%v)\n", path, err)
				name, invalid, failed = filepath.Base(dir), err.Error(), true
			}
			key := canonicalPathKey(real)
			if index, ok := seen[key]; ok {
				skills[index].Aliases = appendUnique(skills[index].Aliases, dir)
				addSkillBinding(&skills[index], dir, rootSpec)
				return true, nil
			}
			seen[key] = len(skills)
			item := skill{Name: name, Path: real, Aliases: []string{dir}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope, Invalid: invalid}
			if invalid != "" {
				item.IssueCode = "invalid_skill"
			}
			addSkillBinding(&item, dir, rootSpec)
			skills = append(skills, item)
			return true, nil
		}, func(path, target string) {
			name := filepath.Base(path)
			item := skill{Name: name, Path: path, Aliases: []string{path}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope, Broken: true, LinkTarget: target}
			addSkillBinding(&item, path, rootSpec)
			skills = append(skills, item)
		}, func(alias, canonical string) {
			for i := range skills {
				if within(canonical, skills[i].Path) {
					rel, relErr := filepath.Rel(canonical, skills[i].Path)
					if relErr == nil {
						path := filepath.Join(alias, rel)
						skills[i].Aliases = appendUnique(skills[i].Aliases, path)
						addSkillBinding(&skills[i], path, rootSpec)
					}
				}
			}
		})
		if err != nil {
			fmt.Fprintf(stderr, "%s: scan failed: %v\n", root, err)
			failed = true
			skills = append(skills, skill{Name: filepath.Base(root), Path: root, ScanRoot: root, Host: rootSpec.Host, Scope: rootSpec.Scope, Invalid: err.Error(), IssueCode: "root_unreadable"})
		}
	}
	slices.SortFunc(skills, func(a, b skill) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	return skills, failed
}

func addSkillBinding(item *skill, path string, root scanRoot) {
	for _, binding := range item.Bindings {
		if samePath(binding.Path, path) && binding.Host == root.Host && binding.Scope == root.Scope && binding.Project == root.Project {
			return
		}
	}
	mode := "directory"
	if !samePath(path, item.Path) {
		mode = "link"
	}
	item.Bindings = append(item.Bindings, skillBinding{Path: path, Host: root.Host, Scope: root.Scope, Project: root.Project, Enabled: true, Mode: mode})
}

func uniqueSkillCount(skills []skill) int {
	seen := make(map[string]struct{}, len(skills))
	for _, item := range skills {
		seen[item.Name] = struct{}{}
	}
	return len(seen)
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

// Resolve aliases in existing parents while retaining the identity of the leaf.
// Two Agent symlinks remain distinct bindings even when they share a target.
func canonicalLocation(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	parent := filepath.Dir(absolute)
	tail := []string{filepath.Base(absolute)}
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved
		}
		next := filepath.Dir(parent)
		if next == parent {
			return absolute
		}
		tail = append(tail, filepath.Base(parent))
		parent = next
	}
}

func physicalLocation(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return canonicalLocation(path)
}
