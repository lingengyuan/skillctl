package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const githubInstaller = "install-skill-from-github.py"

var commandProperty = regexp.MustCompile(`(?:\b(?:cmd|command)|"(?:cmd|command)")\s*:\s*("(?:\\.|[^"\\])*")`)

type installCandidate struct {
	Name      string
	Source    string
	Ref       string
	SkillPath string
	When      time.Time
}

type historyRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
	} `json:"payload"`
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func trackFromInstallHistory(ctx context.Context, timeout time.Duration, items []skill, state *trackedState, manifests []manifest, managed []managedRoot, namesExplicit bool, stdout, stderr io.Writer) bool {
	provenance, _ := newProvenanceIndex(items, state, manifests, managed)
	rootCache := map[string]gitRootResult{}
	var pending []skill
	for _, item := range items {
		claims := provenance.claims(item)
		if claims.hasTracked {
			fmt.Fprintf(stdout, "%s: already tracked\n", item.Name)
			continue
		}
		root, gitClaim := findGitRoot(item.Path, rootCache)
		if gitClaim {
			relSkill, err := filepath.Rel(root, filepath.Join(item.Path, "SKILL.md"))
			gitClaim = err == nil && within(root, filepath.Join(item.Path, "SKILL.md")) && gitTracks(root, relSkill)
		}
		if claims.count() > 0 || gitClaim {
			fmt.Fprintf(stdout, "%s: already managed by existing metadata\n", item.Name)
			continue
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return false
	}
	candidates, err := readInstallHistory()
	if err != nil {
		fmt.Fprintf(stderr, "read install history: %s\n", oneLine(err.Error()))
		return true
	}
	failed := false
	for _, item := range pending {
		matches := append(slices.Clone(candidates[item.Name]), candidates[""]...)
		if len(matches) == 0 {
			fmt.Fprintf(stdout, "%s: no trusted install record\n", item.Name)
			if namesExplicit {
				failed = true
			}
			continue
		}
		verified := map[string]trackedEntry{}
		var lastErr error
		for _, candidate := range matches {
			operationCtx, cancel := context.WithTimeout(ctx, timeout)
			entry, err := verifyCopiedSkill(operationCtx, item, candidate.Source, candidate.Ref, candidate.SkillPath)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			verified[historyEntryKey(entry)] = entry
		}
		switch len(verified) {
		case 0:
			fmt.Fprintf(stderr, "%s: install record found but verification failed (%s)\n", item.Name, oneLine(lastErr.Error()))
			failed = true
		case 1:
			for _, entry := range verified {
				state.put(entry)
			}
			if err := state.save(); err != nil {
				fmt.Fprintf(stderr, "%s: save source state (%s)\n", item.Name, oneLine(err.Error()))
				failed = true
				continue
			}
			fmt.Fprintf(stdout, "%s: tracked from install history\n", item.Name)
		default:
			fmt.Fprintf(stderr, "%s: ambiguous verified install records\n", item.Name)
			failed = true
		}
	}
	return failed
}

func historyEntryKey(entry trackedEntry) string {
	return entry.Source + "\x00" + entry.Ref + "\x00" + entry.SkillPath
}

func readInstallHistory() (map[string][]installCandidate, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return readInstallHistoryRoots([]string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".claude", "projects"),
	})
}

func readInstallHistoryRoots(roots []string) (map[string][]installCandidate, error) {
	result := make(map[string][]installCandidate)
	seen := make(map[string]bool)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return scanHistoryFile(path, info.ModTime(), result, seen)
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	for name := range result {
		slices.SortStableFunc(result[name], func(a, b installCandidate) int {
			switch {
			case a.When.After(b.When):
				return -1
			case a.When.Before(b.When):
				return 1
			default:
				return 0
			}
		})
	}
	return result, nil
}

func scanHistoryFile(path string, modified time.Time, result map[string][]installCandidate, seen map[string]bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if !bytes.Contains(line, []byte(githubInstaller)) && !bytes.Contains(line, []byte("skills")) {
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			continue
		}
		for _, command := range trustedCommands(line) {
			for _, candidate := range parseInstallCommand(command) {
				candidate.When = modified
				key := candidate.Name + "\x00" + candidate.Source + "\x00" + candidate.Ref + "\x00" + candidate.SkillPath
				if seen[key] {
					continue
				}
				seen[key] = true
				result[candidate.Name] = append(result[candidate.Name], candidate)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func trustedCommands(line []byte) []string {
	var record historyRecord
	if json.Unmarshal(line, &record) != nil {
		return nil
	}
	var commands []string
	if record.Type == "response_item" {
		switch record.Payload.Type {
		case "function_call":
			if record.Payload.Name == "exec_command" {
				var input struct {
					Cmd string `json:"cmd"`
				}
				if json.Unmarshal([]byte(record.Payload.Arguments), &input) == nil && input.Cmd != "" {
					commands = append(commands, input.Cmd)
				}
			}
		case "custom_tool_call":
			if record.Payload.Name == "exec" {
				var input string
				if json.Unmarshal(record.Payload.Input, &input) == nil {
					for _, match := range commandProperty.FindAllStringSubmatch(input, -1) {
						var command string
						if json.Unmarshal([]byte(match[1]), &command) == nil {
							commands = append(commands, command)
						}
					}
				}
			}
		}
	}
	if record.Type == "assistant" {
		for _, content := range record.Message.Content {
			if content.Type == "tool_use" && (content.Name == "Bash" || content.Name == "bash") && content.Input.Command != "" {
				commands = append(commands, content.Input.Command)
			}
		}
	}
	return commands
}

func parseInstallCommand(command string) []installCandidate {
	var result []installCandidate
	for _, segment := range shellCommandSegments(shellWords(command)) {
		result = append(result, parseInstallerCommandWords(segment)...)
		result = append(result, parseSkillsAddWords(segment)...)
	}
	return result
}

func parseInstallerCommandWords(words []string) []installCandidate {
	var result []installCandidate
	for index, word := range words {
		if filepath.Base(strings.ReplaceAll(word, `\`, "/")) != githubInstaller {
			continue
		}
		values := make(map[string][]string)
		for i := index + 1; i < len(words); {
			if isShellOperator(words[i]) {
				break
			}
			flag := words[i]
			if !strings.HasPrefix(flag, "--") {
				i++
				continue
			}
			i++
			if _, exists := values[flag]; !exists {
				values[flag] = nil
			}
			for i < len(words) && !strings.HasPrefix(words[i], "--") && !isShellOperator(words[i]) {
				values[flag] = append(values[flag], words[i])
				i++
			}
		}
		if _, help := values["--help"]; help {
			continue
		}
		source, ref, urlPath := installerSource(first(values["--repo"]), first(values["--url"]), first(values["--ref"]))
		if source == "" {
			continue
		}
		paths := values["--path"]
		if len(paths) == 0 && urlPath != "" {
			paths = []string{urlPath}
		}
		name := first(values["--name"])
		for _, skillPath := range paths {
			candidateName := name
			if candidateName == "" {
				candidateName = filepath.Base(filepath.Clean(skillPath))
			}
			if candidateName == "." || candidateName == string(filepath.Separator) {
				continue
			}
			result = append(result, installCandidate{Name: candidateName, Source: source, Ref: ref, SkillPath: filepath.ToSlash(filepath.Clean(skillPath))})
		}
	}
	return result
}

func parseSkillsAddWords(words []string) []installCandidate {
	arguments, ok := skillsAddArguments(words)
	if !ok {
		return nil
	}
	sourceArgument, selected := skillsAddOptions(arguments)
	if sourceArgument == "" {
		return nil
	}
	if len(selected) == 0 {
		selected = sourceSkillFilter(sourceArgument)
	}
	source, ref, sourcePath := skillsSource(sourceArgument)
	if source == "" {
		return nil
	}
	if len(selected) == 0 {
		return []installCandidate{{Source: source, Ref: ref, SkillPath: sourcePath}}
	}
	if slices.Contains(selected, "*") {
		return []installCandidate{{Source: source, Ref: ref, SkillPath: sourcePath}}
	}
	result := make([]installCandidate, 0, len(selected))
	for _, rawValue := range selected {
		value := filepath.ToSlash(filepath.Clean(rawValue))
		name := filepath.Base(value)
		if name == "." || name == string(filepath.Separator) {
			continue
		}
		candidate := installCandidate{Name: name, Source: source, Ref: ref}
		if strings.Contains(value, "/") {
			candidate.SkillPath = value
		}
		result = append(result, candidate)
	}
	return result
}

func skillsAddOptions(arguments []string) (string, []string) {
	source := ""
	var selected []string
	for index := 0; index < len(arguments); index++ {
		value := arguments[index]
		if value == "--skill" || value == "-s" {
			for index+1 < len(arguments) && !strings.HasPrefix(arguments[index+1], "-") {
				if source == "" && looksLikeSkillsSource(arguments[index+1]) {
					break
				}
				selected = append(selected, arguments[index+1])
				index++
			}
			continue
		}
		if strings.HasPrefix(value, "--skill=") {
			selected = append(selected, strings.TrimPrefix(value, "--skill="))
			continue
		}
		if value == "--agent" || value == "-a" || value == "--dir" {
			continue
		}
		if strings.HasPrefix(value, "-") {
			continue
		}
		if source == "" && looksLikeSkillsSource(value) {
			source = value
		}
	}
	return source, selected
}

func looksLikeSkillsSource(value string) bool {
	return strings.Contains(value, "://") || strings.HasPrefix(value, "git@") || filepath.IsAbs(value) || strings.HasPrefix(value, ".") || strings.Count(strings.TrimSuffix(value, ".git"), "/") == 1
}

func skillsSource(value string) (source, ref, skillPath string) {
	if filepath.IsAbs(value) {
		return value, "", ""
	}
	if strings.Contains(value, "://") {
		if source, ref, skillPath = installerSource("", value, ""); source != "" {
			return source, ref, skillPath
		}
		return value, "", ""
	}
	if repository, _, found := strings.Cut(value, "@"); found && strings.Count(repository, "/") == 1 {
		return installerSource(repository, "", "")
	}
	return installerSource(value, "", "")
}

func sourceSkillFilter(value string) []string {
	repository, skill, found := strings.Cut(value, "@")
	if !found || skill == "" || strings.Count(repository, "/") != 1 {
		return nil
	}
	return []string{skill}
}

func skillsAddArguments(words []string) ([]string, bool) {
	words = skipEnvironmentPrefix(words)
	if len(words) == 0 {
		return nil, false
	}
	command := filepath.Base(words[0])
	command, _, _ = strings.Cut(command, "@")
	if command == "npx" || command == "bunx" {
		for index := 1; index+1 < len(words); index++ {
			name, _, _ := strings.Cut(filepath.Base(words[index]), "@")
			if name == "skills" && isSkillsAddVerb(words[index+1]) {
				return words[index+2:], true
			}
		}
		return nil, false
	}
	if command != "npm" || len(words) < 2 || words[1] != "exec" {
		return nil, false
	}
	for index := 2; index+2 < len(words); index++ {
		name, _, _ := strings.Cut(filepath.Base(words[index+1]), "@")
		if words[index] == "--" && name == "skills" && isSkillsAddVerb(words[index+2]) {
			return words[index+3:], true
		}
		name, _, _ = strings.Cut(filepath.Base(words[index]), "@")
		if name == "skills" && isSkillsAddVerb(words[index+2]) && words[index+1] == "--" {
			return words[index+3:], true
		}
	}
	return nil, false
}

func skipEnvironmentPrefix(words []string) []string {
	if len(words) == 0 || filepath.Base(words[0]) != "env" {
		return words
	}
	for index := 1; index < len(words); index++ {
		if strings.Contains(words[index], "=") || strings.HasPrefix(words[index], "-") {
			continue
		}
		return words[index:]
	}
	return nil
}

func isSkillsAddVerb(value string) bool {
	return value == "add" || value == "install"
}

func shellCommandSegments(words []string) [][]string {
	var result [][]string
	for len(words) > 0 {
		index := slices.IndexFunc(words, isShellOperator)
		if index < 0 {
			return append(result, words)
		}
		if index > 0 {
			result = append(result, words[:index])
		}
		words = words[index+1:]
	}
	return result
}

func installerSource(repo, rawURL, explicitRef string) (source, ref, skillPath string) {
	if repo != "" {
		if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
			return repo, explicitRef, ""
		}
		if strings.Count(repo, "/") == 1 {
			return "https://github.com/" + strings.TrimSuffix(repo, ".git") + ".git", explicitRef, ""
		}
		return "", "", ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", ""
	}
	source = "https://github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git") + ".git"
	ref = explicitRef
	if len(parts) >= 4 && parts[2] == "tree" {
		if ref == "" {
			ref = parts[3]
		}
		if len(parts) > 4 {
			skillPath = strings.Join(parts[4:], "/")
		}
	}
	return source, ref, skillPath
}

func shellWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote byte
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			if ch != '\n' {
				word.WriteByte(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			if i+1 < len(command) {
				next := command[i+1]
				if quote == '"' && (next == '"' || next == '\\' || next == '$' || next == '`' || next == '\n') ||
					quote == 0 && strings.ContainsRune(" \t\r\n\"'\\;|&", rune(next)) {
					escaped = true
					continue
				}
			}
			word.WriteByte(ch)
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteByte(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' {
			flush()
			continue
		}
		if ch == '\n' || strings.ContainsRune(";|&", rune(ch)) {
			flush()
			operator := string(ch)
			if i+1 < len(command) && command[i+1] == ch && (ch == '|' || ch == '&') {
				operator += string(ch)
				i++
			}
			words = append(words, operator)
			continue
		}
		word.WriteByte(ch)
	}
	flush()
	return words
}

func isShellOperator(value string) bool {
	return value == ";" || value == "|" || value == "||" || value == "&" || value == "&&" || value == "\n"
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
