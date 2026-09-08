package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func printSkills(w io.Writer, skills []skill, message, state, reason string, available bool) {
	if sink, ok := w.(*reportSink); ok {
		sink.set(skills, message, state, reason, available)
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
		bindings := item.Bindings
		if len(bindings) == 0 {
			bindings = []skillBinding{{Host: item.Host, Scope: item.Scope}}
		}
		for _, binding := range bindings {
			if (len(hostSet) == 0 || hostSet[strings.ToLower(binding.Host)]) && (len(scopeSet) == 0 || scopeSet[strings.ToLower(binding.Scope)]) {
				item.Host, item.Scope = binding.Host, binding.Scope
				filtered = append(filtered, item)
				break
			}
		}
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
			key := fsutil.PathKey(match.Path)
			if !seenPaths[key] {
				selected = append(selected, match)
				seenPaths[key] = true
			}
		}
	}
	return selected, nil
}
