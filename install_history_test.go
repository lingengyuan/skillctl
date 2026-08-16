package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedCommandsOnlyReadsStructuredToolCalls(t *testing.T) {
	command := "python3 /tmp/install-skill-from-github.py --repo owner/repo --path skills/demo --name demo"
	input, _ := json.Marshal(`const r = await tools.exec_command({cmd:"` + command + `"});`)
	codex := []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":` + string(input) + `}}`)
	if got := trustedCommands(codex); len(got) != 1 || got[0] != command {
		t.Fatalf("Codex command = %#v", got)
	}

	claude := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"python3 /tmp/install-skill-from-github.py --repo owner/repo --path demo"}}]}}`)
	if got := trustedCommands(claude); len(got) != 1 {
		t.Fatalf("Claude commands = %#v", got)
	}

	userText := []byte(`{"type":"user","message":{"content":"python3 /tmp/install-skill-from-github.py --repo bad/guess --path demo"}}`)
	if got := trustedCommands(userText); len(got) != 0 {
		t.Fatalf("ordinary text was trusted: %#v", got)
	}
}

func TestParseInstallerCommand(t *testing.T) {
	command := "python3 /tmp/install-skill-from-github.py --repo Zeejay0/gathered-scenes-zine-skill --path skills/scenes-gathered-zine-v1-3 skills/scene-distillation-zine-v1-3"
	got := parseInstallerCommand(command)
	if len(got) != 2 || got[0].Name != "scenes-gathered-zine-v1-3" || got[1].Name != "scene-distillation-zine-v1-3" {
		t.Fatalf("candidates = %#v", got)
	}
	if got[0].Source != "https://github.com/Zeejay0/gathered-scenes-zine-skill.git" {
		t.Fatalf("source = %q", got[0].Source)
	}

	urlCommand := "python3 /tmp/install-skill-from-github.py --url https://github.com/owner/repo/tree/v1/skills/demo --name renamed"
	got = parseInstallerCommand(urlCommand)
	if len(got) != 1 || got[0].Name != "renamed" || got[0].Ref != "v1" || got[0].SkillPath != "skills/demo" {
		t.Fatalf("URL candidate = %#v", got)
	}

	if got := parseInstallerCommand("python3 /tmp/install-skill-from-github.py --help"); len(got) != 0 {
		t.Fatalf("help command became candidate: %#v", got)
	}
}

func TestReadInstallHistoryRoots(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	lines := "{\"type\":\"user\",\"message\":{\"content\":\"install-skill-from-github.py --repo bad/guess --path demo\"}}\n" +
		"{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"python3 /tmp/install-skill-from-github.py --repo owner/repo --path skills/demo\"}}]}}\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readInstallHistoryRoots([]string{root, filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["demo"]) != 1 || got["demo"][0].SkillPath != "skills/demo" {
		t.Fatalf("history = %#v", got)
	}
}

func TestReadInstallHistoryLongJSONLine(t *testing.T) {
	root := t.TempDir()
	record := map[string]any{
		"type":    "assistant",
		"padding": strings.Repeat("x", 9*1024*1024),
		"message": map[string]any{"content": []any{map[string]any{
			"type":  "tool_use",
			"name":  "Bash",
			"input": map[string]any{"command": "python3 /tmp/install-skill-from-github.py --repo owner/repo --path demo"},
		}}},
	}
	content, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(filepath.Join(root, "large.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readInstallHistoryRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["demo"]) != 1 {
		t.Fatalf("large history record was not parsed: %#v", got)
	}
}
