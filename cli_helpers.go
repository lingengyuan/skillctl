package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func printSkills(w io.Writer, skills []skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.set(skills, message)
		return
	}
	for _, item := range skills {
		fmt.Fprintf(w, "%s: %s\n", item.Name, message)
	}
}

func reportFailure(w io.Writer, item skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.failure(item, message)
		return
	}
	fmt.Fprintf(w, "%s: failed (%s)\n", item.Name, message)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func filterSkills(all []skill, hosts, scopes []string) []skill {
	if len(hosts) == 0 && len(scopes) == 0 {
		return all
	}
	hostSet := stringSet(hosts)
	scopeSet := stringSet(scopes)
	filtered := make([]skill, 0, len(all))
	for _, item := range all {
		if len(hostSet) > 0 && !hostSet[strings.ToLower(item.Host)] {
			continue
		}
		if len(scopeSet) > 0 && !scopeSet[strings.ToLower(item.Scope)] {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = true
	}
	return result
}

func selectSkills(all []skill, names []string) ([]skill, error) {
	return selectSkillsWithMode(all, names, false)
}

func selectSkillsWithMode(all []skill, names []string, allMatches bool) ([]skill, error) {
	byName := map[string][]skill{}
	for _, item := range all {
		byName[item.Name] = append(byName[item.Name], item)
	}
	var selected []skill
	seenPaths := map[string]bool{}
	for _, name := range names {
		matches := byName[name]
		if len(matches) == 0 {
			return nil, fmt.Errorf("skill not found: %s", name)
		}
		if len(matches) > 1 && !allMatches {
			paths := make([]string, 0, len(matches))
			for _, match := range matches {
				paths = append(paths, match.Path)
			}
			return nil, fmt.Errorf("skill name %q is ambiguous across %d installations: %s; narrow the scan with --path/--host/--scope or pass --all-matches", name, len(matches), strings.Join(paths, ", "))
		}
		for _, match := range matches {
			key := canonicalPathKey(match.Path)
			if !seenPaths[key] {
				selected = append(selected, match)
				seenPaths[key] = true
			}
		}
	}
	return selected, nil
}
