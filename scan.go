package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// skill is one installed instance.  Path is kept as the canonical path for
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

func scan(roots []scanRoot, ignoreMissing bool, stderr io.Writer) ([]skill, bool) {
	seen := map[string]int{}
	visitedDirs := map[string]string{}
	var skills []skill
	failed := false
	for _, rootSpec := range roots {
		root := rootSpec.Path
		_, err := os.Stat(root)
		if err != nil {
			if ignoreMissing && errors.Is(err, os.ErrNotExist) {
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
		err = walkFollowingLinks(root, visitedDirs, func(path, real string) error {
			dir := filepath.Dir(path)
			name, err := readSkill(path)
			if err != nil {
				fmt.Fprintf(stderr, "%s: skipped (%v)\n", path, err)
				return nil
			}
			key := canonicalPathKey(real)
			if index, ok := seen[key]; ok {
				skills[index].Aliases = appendUnique(skills[index].Aliases, dir)
				return nil
			}
			seen[key] = len(skills)
			skills = append(skills, skill{Name: name, Path: real, Aliases: []string{dir}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope})
			return nil
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

func walkFollowingLinks(dir string, visited map[string]string, visitSkill func(string, string) error, visitBroken func(string, string), visitAlias func(string, string)) error {
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
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
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
			return err
		}
		if info.IsDir() {
			if err := walkFollowingLinks(path, visited, visitSkill, visitBroken, visitAlias); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					visitBroken(path, "")
					continue
				}
				return err
			}
			continue
		}
		if entry.Name() == "SKILL.md" {
			if err := visitSkill(path, canonical); err != nil {
				return err
			}
		}
	}
	return nil
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func readSkill(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read skill: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read front matter: %w", err)
		}
		return "", errors.New("missing YAML front matter")
	}
	if scanner.Text() != "---" {
		return "", errors.New("missing YAML front matter")
	}
	values := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			name := values["name"]
			if name == "" {
				return "", errors.New("missing name")
			}
			if len(name) > 64 {
				return "", errors.New("name exceeds 64 characters")
			}
			if !skillName.MatchString(name) {
				return "", fmt.Errorf("invalid name %q: use lowercase letters, numbers, and hyphens", name)
			}
			if values["description"] == "" {
				return "", errors.New("missing description")
			}
			return name, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "name" || key == "description" {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read front matter: %w", err)
	}
	return "", errors.New("unterminated YAML front matter")
}
