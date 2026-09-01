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

	currentCommand := "python3 /tmp/install-skill-from-github.py --repo tw93/Waza --path skills/write skills/learn skills/read --dest /tmp/codex/skills"
	currentInput, _ := json.Marshal(`const r = await tools.shell_command({command:"` + currentCommand + `"}); text(r)`)
	currentCodex := []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":` + string(currentInput) + `}}`)
	if got := trustedCommands(currentCodex); len(got) != 1 || got[0] != currentCommand {
		t.Fatalf("current Codex command = %#v", got)
	}

	realInput, _ := json.Marshal(`const r = await tools.exec_command({"cmd":"npx --yes skills add owner/repo"});`)
	realCodex := []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":` + string(realInput) + `}}`)
	if got := trustedCommands(realCodex); len(got) != 1 || got[0] != "npx --yes skills add owner/repo" {
		t.Fatalf("real Codex command = %#v", got)
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
	got := parseInstallCommand(command)
	if len(got) != 2 || got[0].Name != "scenes-gathered-zine-v1-3" || got[1].Name != "scene-distillation-zine-v1-3" {
		t.Fatalf("candidates = %#v", got)
	}
	if got[0].Source != "https://github.com/Zeejay0/gathered-scenes-zine-skill.git" {
		t.Fatalf("source = %q", got[0].Source)
	}

	urlCommand := "python3 /tmp/install-skill-from-github.py --url https://github.com/owner/repo/tree/v1/skills/demo --name renamed"
	got = parseInstallCommand(urlCommand)
	if len(got) != 1 || got[0].Name != "renamed" || got[0].Ref != "v1" || got[0].SkillPath != "skills/demo" {
		t.Fatalf("URL candidate = %#v", got)
	}

	if got := parseInstallCommand("python3 /tmp/install-skill-from-github.py --help"); len(got) != 0 {
		t.Fatalf("help command became candidate: %#v", got)
	}

	windowsCommand := `python "C:\Users\test\.codex\skills\.system\skill-installer\scripts\install-skill-from-github.py" --repo owner/repo --path skills/demo`
	got = parseInstallCommand(windowsCommand)
	if len(got) != 1 || got[0].Name != "demo" || got[0].Source != "https://github.com/owner/repo.git" {
		t.Fatalf("Windows path candidate = %#v", got)
	}
}

func TestParseSkillsAddCommand(t *testing.T) {
	got := parseInstallCommand("npx --yes skills add tw93/Waza -a codex -g -y")
	if len(got) != 1 || got[0].Name != "" || got[0].Source != "https://github.com/tw93/Waza.git" {
		t.Fatalf("npx all-skills candidate = %#v", got)
	}

	got = parseInstallCommand("npm exec --yes skills -- add https://github.com/owner/repo/tree/v1/skills/demo --skill skills/demo")
	if len(got) != 1 || got[0].Name != "demo" || got[0].SkillPath != "skills/demo" || got[0].Ref != "v1" {
		t.Fatalf("npm exec candidate = %#v", got)
	}

	if got := parseInstallCommand("npx skills update -g -y"); len(got) != 0 {
		t.Fatalf("update command became an install candidate: %#v", got)
	}

	got = parseInstallCommand("npx skills@latest add -g owner/repo -s alpha")
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Source != "https://github.com/owner/repo.git" {
		t.Fatalf("versioned explicit candidate = %#v", got)
	}

	got = parseInstallCommand("cd /tmp && env INSTALL_SCOPE=user npx skills add owner/repo --skill alpha")
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("compound command candidate = %#v", got)
	}

	got = parseInstallCommand("npx skills add -a codex cursor owner/repo --skill alpha beta")
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("multi-value candidate = %#v", got)
	}

	got = parseInstallCommand("npx skills add owner/repo --skill '*'")
	if len(got) != 1 || got[0].Name != "" {
		t.Fatalf("all-skills candidate = %#v", got)
	}

	got = parseInstallCommand("npx skills add owner/repo@alpha")
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Source != "https://github.com/owner/repo.git" {
		t.Fatalf("repository skill shorthand = %#v", got)
	}

	localSource := filepath.Join(t.TempDir(), "local-source")
	got = parseInstallCommand("npx skills add " + localSource)
	if len(got) != 1 || got[0].Source != localSource {
		t.Fatalf("absolute local source = %#v", got)
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

func TestReadInstallHistoryRootsParsesCurrentCodexInstallerCommand(t *testing.T) {
	root := t.TempDir()
	command := "python C:/Users/test/.codex/skills/.system/skill-installer/scripts/install-skill-from-github.py --repo tw93/Waza --path skills/write skills/learn skills/read --dest C:/Users/test/.codex/skills"
	input, err := json.Marshal(`const r = await tools.shell_command({command:"` + command + `"}); text(r)`)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":` + string(input) + `}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readInstallHistoryRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"write", "learn", "read"} {
		matches := got[name]
		if len(matches) != 1 || matches[0].Source != "https://github.com/tw93/Waza.git" || matches[0].SkillPath != "skills/"+name {
			t.Fatalf("history candidate for %s = %#v", name, matches)
		}
	}
}

func TestReadInstallHistoryRootsParsesSkillsAddWithoutSkillName(t *testing.T) {
	root := t.TempDir()
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npx --yes skills add tw93/Waza -a codex -g -y"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readInstallHistoryRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	matches := got[""]
	if len(matches) != 1 || matches[0].Source != "https://github.com/tw93/Waza.git" {
		t.Fatalf("all-skills history candidate = %#v", matches)
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
