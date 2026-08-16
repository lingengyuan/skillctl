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
	"sort"
	"strings"
	"time"
)

const githubInstaller = "install-skill-from-github.py"

var commandProperty = regexp.MustCompile(`\bcmd\s*:\s*("(?:\\.|[^"\\])*")`)

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
	locks, _ := loadVercelLocks(manifests)
	hostClaims := loadCodexCuratedClaims(items)
	rootCache := map[string]gitRootResult{}
	var pending []skill
	for _, item := range items {
		if _, ok := state.findSkill(item); ok {
			fmt.Fprintf(stdout, "%s: already tracked\n", item.Name)
			continue
		}
		_, _, lockClaim := locks.claim(item)
		_, hostClaim := hostClaims[item.Path]
		managedClaim, _ := managedOwner(item, managed)
		ghClaim := readGHSkillClaim(item)
		root, gitClaim := findGitRoot(item.Path, rootCache)
		if gitClaim {
			relSkill, err := filepath.Rel(root, filepath.Join(item.Path, "SKILL.md"))
			gitClaim = err == nil && within(root, filepath.Join(item.Path, "SKILL.md")) && gitTracks(root, relSkill)
		}
		if lockClaim || hostClaim || managedClaim != "" || ghClaim.Found || gitClaim {
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
		matches := candidates[item.Name]
		if len(matches) == 0 {
			fmt.Fprintf(stdout, "%s: no trusted install record\n", item.Name)
			if namesExplicit {
				failed = true
			}
			continue
		}
		var lastErr error
		tracked := false
		for _, candidate := range matches {
			operationCtx, cancel := context.WithTimeout(ctx, timeout)
			lastErr = trackCopiedSkill(operationCtx, item, candidate.Source, candidate.Ref, candidate.SkillPath, state)
			cancel()
			if lastErr == nil {
				fmt.Fprintf(stdout, "%s: tracked from install history\n", item.Name)
				tracked = true
				break
			}
		}
		if !tracked {
			fmt.Fprintf(stderr, "%s: install record found but verification failed (%s)\n", item.Name, oneLine(lastErr.Error()))
			failed = true
		}
	}
	return failed
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
		sort.SliceStable(result[name], func(i, j int) bool {
			return result[name][i].When.After(result[name][j].When)
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
		if !bytes.Contains(line, []byte(githubInstaller)) {
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			continue
		}
		for _, command := range trustedCommands(line) {
			for _, candidate := range parseInstallerCommand(command) {
				candidate.When = modified
				key := candidate.Name + "\x00" + candidate.Source + "\x00" + candidate.Ref + "\x00" + candidate.SkillPath
				if candidate.Name == "" || seen[key] {
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

func parseInstallerCommand(command string) []installCandidate {
	words := shellWords(command)
	var result []installCandidate
	for index, word := range words {
		if filepath.Base(word) != githubInstaller {
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
			values[flag] = values[flag]
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
			escaped = true
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
