package installhistory

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// invokedCommands accepts literal top-level awaited tool calls only. Lexing
// skips comments and strings; nested functions and conditional blocks cannot
// contribute an invocation. This is deliberately not a JavaScript evaluator.
func invokedCommands(script string) []string {
	tokens, ok := scriptTokens(script)
	if !ok {
		return nil
	}
	var commands []string
	depth := 0
	for i, token := range tokens {
		if (depth == 0 || depth == 1 && i >= 2 && tokens[i-2] == "text" && tokens[i-1] == "(") && token == "await" && i+5 < len(tokens) && tokens[i+1] == "tools" && tokens[i+2] == "." && (tokens[i+3] == "exec_command" || tokens[i+3] == "shell_command") && tokens[i+4] == "(" && tokens[i+5] == "{" {
			// The await must begin a statement, optionally assigned to a variable.
			start := i - 1
			for start >= 0 && tokens[start] != ";" {
				start--
			}
			prefix := tokens[start+1 : i]
			if len(prefix) != 0 && !(len(prefix) == 2 && prefix[0] == "text" && prefix[1] == "(") && !(len(prefix) == 3 && (prefix[0] == "const" || prefix[0] == "let" || prefix[0] == "var") && prefix[2] == "=") {
				continue
			}
			for j := i + 6; j+2 < len(tokens) && tokens[j] != "}"; j++ {
				key := strings.Trim(tokens[j], "\"")
				if (key == "cmd" || key == "command") && tokens[j+1] == ":" {
					var command string
					if json.Unmarshal([]byte(tokens[j+2]), &command) == nil && j+3 < len(tokens) && (tokens[j+3] == "," || tokens[j+3] == "}") {
						commands = append(commands, command)
					}
				}
			}
		}
		if token == "{" || token == "(" || token == "[" {
			depth++
		}
		if token == "}" || token == ")" || token == "]" {
			depth--
		}
	}
	return commands
}

func scriptTokens(script string) ([]string, bool) {
	var tokens []string
	for i := 0; i < len(script); {
		if unicode.IsSpace(rune(script[i])) {
			i++
			continue
		}
		if strings.HasPrefix(script[i:], "//") {
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		if strings.HasPrefix(script[i:], "/*") {
			end := strings.Index(script[i+2:], "*/")
			if end < 0 {
				return nil, false
			}
			i += end + 4
			continue
		}
		start := i
		if script[i] == '"' || script[i] == '\'' || script[i] == '`' {
			quote := script[i]
			i++
			for i < len(script) && script[i] != quote {
				if script[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(script) {
				return nil, false
			}
			i++
		} else if unicode.IsLetter(rune(script[i])) || script[i] == '_' {
			for i < len(script) && (unicode.IsLetter(rune(script[i])) || unicode.IsDigit(rune(script[i])) || script[i] == '_') {
				i++
			}
		} else {
			i++
		}
		tokens = append(tokens, script[start:i])
	}
	return tokens, true
}

func commandDirectory(record historyRecord) string {
	var args struct {
		Workdir string `json:"workdir"`
	}
	_ = json.Unmarshal([]byte(record.Payload.Arguments), &args)
	return args.Workdir
}

func optionValue(words []string, flags ...string) string {
	for i, word := range words {
		for _, flag := range flags {
			if value, found := strings.CutPrefix(word, flag+"="); found {
				return value
			}
			if word == flag && i+1 < len(words) {
				return words[i+1]
			}
		}
	}
	return ""
}

// Only a final process exit status establishes success. Accepted requests,
// running session IDs, and free-text success claims leave the outcome unknown.
func executionOutcome(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "unknown"
	}
	return valueOutcome(value)
}

func valueOutcome(value any) string {
	switch v := value.(type) {
	case string:
		var inner any
		if json.Unmarshal([]byte(v), &inner) == nil {
			return valueOutcome(inner)
		}
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			if line == "Output:" || line == "Final output:" {
				break
			}
			for _, prefix := range []string{"Process exited with code ", "Process exit code: "} {
				if value, found := strings.CutPrefix(line, prefix); found {
					code, err := strconv.Atoi(value)
					if err != nil {
						return "unknown"
					}
					if code == 0 {
						return "succeeded"
					}
					return "failed"
				}
			}
		}
	case map[string]any:
		if output, ok := v["output"].(string); ok {
			lower := strings.ToLower(output)
			if strings.Contains(lower, "failed to install") || strings.Contains(lower, "installation failed") {
				return "partial"
			}
		}
		if code, ok := v["exit_code"].(float64); ok {
			if code == 0 {
				return "succeeded"
			}
			return "failed"
		}
		if text, ok := v["text"].(string); ok {
			return valueOutcome(text)
		}
	case []any:
		if len(v) == 0 {
			return "unknown"
		}
		outcome := "succeeded"
		for _, entry := range v {
			current := valueOutcome(entry)
			if current == "failed" || current == "partial" {
				return current
			}
			if current != "succeeded" {
				outcome = "unknown"
			}
		}
		return outcome
	}
	return "unknown"
}
