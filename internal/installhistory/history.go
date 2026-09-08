package installhistory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const githubInstaller = "install-skill-from-github.py"

// Candidate is unverified installer evidence; callers must verify content before registering a source.
type Candidate struct {
	Name        string
	Source      string
	Ref         string
	SkillPath   string
	When        time.Time
	EvidenceID  string
	Outcome     string
	Destination string
	Directory   string
	Hosts       []string
	Global      bool
}

type historyRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		CallID    string          `json:"call_id"`
		Output    json.RawMessage `json:"output"`
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
	} `json:"payload"`
	Message struct {
		Content []struct {
			ID        string          `json:"id"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Input     struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// ReadRoots collects and deduplicates structured installer records from JSONL files under the supplied roots.
func ReadRoots(roots []string) (map[string][]Candidate, error) {
	return ReadRootsContext(context.Background(), roots)
}

func ReadRootsContext(ctx context.Context, roots []string) (map[string][]Candidate, error) {
	result := make(map[string][]Candidate)
	seen := make(map[string]bool)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			return scanHistoryFileContext(ctx, path, result, seen)
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	for name := range result {
		slices.SortStableFunc(result[name], func(a, b Candidate) int {
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

func scanHistoryFileContext(ctx context.Context, path string, result map[string][]Candidate, seen map[string]bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	var fragments []byte
	pending := map[string][]Candidate{}
	outcomes := map[string]string{}
	lineNumber := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			fragments = append(fragments, line...)
			continue
		}
		if len(fragments) > 0 {
			fragments = append(fragments, line...)
			line = fragments
		}
		fragments = fragments[:0]
		lineNumber++
		relevant := bytes.Contains(line, []byte(githubInstaller)) || bytes.Contains(line, []byte("skills")) || bytes.Contains(line, []byte(`\u`))
		if !relevant {
			for id := range pending {
				encoded, _ := json.Marshal(id)
				if bytes.Contains(line, encoded) {
					relevant = true
					break
				}
			}
		}
		var record historyRecord
		if relevant && json.Unmarshal(line, &record) == nil {
			callID := record.Payload.CallID
			if record.Payload.Type == "function_call_output" || record.Payload.Type == "custom_tool_call_output" {
				if callID != "" {
					outcomes[callID] = executionOutcome(record.Payload.Output)
				}
			}
			for _, content := range record.Message.Content {
				if content.Type == "tool_result" && content.ToolUseID != "" {
					outcome := executionOutcome(content.Content)
					if content.IsError {
						outcome = "failed"
					}
					outcomes[content.ToolUseID] = outcome
				}
			}
			commands := trustedRecordCommands(record)
			for i, command := range commands {
				id := callID
				if record.Type == "assistant" {
					var ids []string
					for _, content := range record.Message.Content {
						if content.Type == "tool_use" && (content.Name == "Bash" || content.Name == "bash") {
							ids = append(ids, content.ID)
						}
					}
					if i < len(ids) {
						id = ids[i]
					}
				}
				for _, candidate := range parseInstallCommand(command) {
					candidate.When = record.Timestamp
					candidate.EvidenceID = path + ":" + strconv.Itoa(lineNumber)
					candidate.Outcome = "unknown"
					if candidate.Directory == "" {
						candidate.Directory = commandDirectory(record)
					}
					key := id
					if key == "" {
						key = candidate.EvidenceID
					}
					pending[key] = append(pending[key], candidate)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	for id, candidates := range pending {
		for _, candidate := range candidates {
			if outcome := outcomes[id]; outcome != "" {
				candidate.Outcome = outcome
			}
			// Retain distinct attempts and their actual outcomes. Never let file
			// traversal order turn an earlier failure into the latest evidence.
			key := candidate.EvidenceID + "\x00" + candidate.Name + "\x00" + candidate.Source + "\x00" + candidate.SkillPath
			if seen[key] {
				continue
			}
			seen[key] = true
			result[candidate.Name] = append(result[candidate.Name], candidate)
		}
	}
	return nil
}

func trustedCommands(line []byte) []string {
	var record historyRecord
	if json.Unmarshal(line, &record) != nil {
		return nil
	}
	return trustedRecordCommands(record)
}

func trustedRecordCommands(record historyRecord) []string {
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
					commands = append(commands, invokedCommands(input)...)
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

func parseInstallCommand(command string) []Candidate {
	words := ShellWords(strings.TrimSpace(command))
	directory := ""
	if len(words) > 3 && words[0] == "cd" && (filepath.IsAbs(words[1]) || path.IsAbs(words[1])) && words[2] == "&&" {
		directory, words = words[1], words[3:]
	}
	// Only literal, unconditional invocations are supported. Unsupported shell
	// control flow remains unverified evidence, never an installation fact.
	if strings.Contains(command, "<<") || strings.Contains(command, "$ (") || strings.Contains(command, "$(") || strings.Contains(command, "`") {
		return nil
	}
	for _, word := range words {
		if word == "&&" || word == "||" || word == "|" || word == "&" || word == ";" || word == "\n" || word == "--help" || word == "-h" || word == "--list" {
			return nil
		}
	}
	var result []Candidate
	for _, segment := range shellCommandSegments(words) {
		if len(segment) == 0 {
			continue
		}
		candidates := append(parseInstallerCommandWords(segment), parseSkillsAddWords(segment)...)
		for _, candidate := range candidates {
			candidate.Destination = optionValue(segment, "--dest", "--dir")
			candidate.Directory = directory
			for i, value := range segment {
				if value == "-g" || value == "--global" {
					candidate.Global = true
				}
				if value == "-a" || value == "--agent" {
					for j := i + 1; j < len(segment) && !strings.HasPrefix(segment[j], "-"); j++ {
						candidate.Hosts = append(candidate.Hosts, segment[j])
					}
				}
			}
			result = append(result, candidate)
		}
	}
	return result
}

func parseInstallerCommandWords(words []string) []Candidate {
	var result []Candidate
	words = skipEnvironmentPrefix(words)
	for index, word := range words {
		if index > 1 || index == 1 && !strings.HasPrefix(filepath.Base(words[0]), "python") {
			continue
		}
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
			result = append(result, Candidate{Name: candidateName, Source: source, Ref: ref, SkillPath: filepath.ToSlash(filepath.Clean(skillPath))})
		}
	}
	return result
}

func parseSkillsAddWords(words []string) []Candidate {
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
	source, ref, sourcePath := ParseSource(sourceArgument)
	if source == "" {
		return nil
	}
	if len(selected) == 0 {
		return []Candidate{{Source: source, Ref: ref, SkillPath: sourcePath}}
	}
	if slices.Contains(selected, "*") {
		return []Candidate{{Source: source, Ref: ref, SkillPath: sourcePath}}
	}
	result := make([]Candidate, 0, len(selected))
	for _, rawValue := range selected {
		value := filepath.ToSlash(filepath.Clean(rawValue))
		name := filepath.Base(value)
		if name == "." || name == string(filepath.Separator) {
			continue
		}
		candidate := Candidate{Name: name, Source: source, Ref: ref}
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

// ParseSource interprets the source and optional filter syntax used by supported installers.
func ParseSource(value string) (source, ref, skillPath string) {
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
		index := 1
		for index < len(words) && (words[index] == "--yes" || words[index] == "-y" || words[index] == "--quiet") {
			index++
		}
		if index+1 >= len(words) {
			return nil, false
		}
		name, _, _ := strings.Cut(filepath.Base(words[index]), "@")
		if name == "skills" && isSkillsAddVerb(words[index+1]) {
			return words[index+2:], true
		}
		return nil, false
	}
	if command != "npm" || len(words) < 2 || words[1] != "exec" {
		return nil, false
	}
	index := 2
	for index < len(words) && (words[index] == "--yes" || words[index] == "-y" || words[index] == "--quiet") {
		index++
	}
	if index < len(words) && words[index] == "--" {
		index++
	}
	if index >= len(words) {
		return nil, false
	}
	name, _, _ := strings.Cut(filepath.Base(words[index]), "@")
	index++
	if index < len(words) && words[index] == "--" {
		index++
	}
	if name == "skills" && index < len(words) && isSkillsAddVerb(words[index]) {
		return words[index+1:], true
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

// ShellWords splits supported shell syntax for inspection without executing it.
func ShellWords(command string) []string {
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
		if ch == '#' && word.Len() == 0 {
			for i < len(command) && command[i] != '\n' {
				i++
			}
			flush()
			words = append(words, "\n")
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
