package installhistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvocationEvidenceRejectsDataAndControlFlow(t *testing.T) {
	command := `npx skills add owner/repo --skill demo`
	for _, script := range []string{
		`// await tools.exec_command({cmd:"` + command + `"});`,
		`const unused = {cmd:"` + command + `"};`,
		`async function unused() { await tools.exec_command({cmd:"` + command + `"}); }`,
		`if (false) await tools.exec_command({cmd:"` + command + `"});`,
		`const s = 'await tools.exec_command({cmd:"` + command + `"});';`,
		`await tools.exec_command({cmd:"` + command + `" + extra});`,
	} {
		if got := invokedCommands(script); len(got) != 0 {
			t.Errorf("data became invocation: %s: %v", script, got)
		}
	}
	for _, shell := range []string{
		"false && " + command,
		"# " + command,
		"cat <<'EOF'\n" + command + "\nEOF",
		"echo python3 /tmp/install-skill-from-github.py --repo owner/repo --path demo",
		command + " --help",
	} {
		if got := parseInstallCommand(shell); len(got) != 0 {
			t.Errorf("data became install: %s: %v", shell, got)
		}
	}
}

func TestHistoryCorrelatesRetriesAndUsesEventTime(t *testing.T) {
	root := t.TempDir()
	var records []map[string]any
	for i, id := range []string{"first", "retry"} {
		arguments, _ := json.Marshal(map[string]string{"cmd": "npx skills add owner/repo --skill demo --dir /installed", "workdir": "/project"})
		records = append(records, map[string]any{"type": "response_item", "timestamp": time.Date(2026, 8, 29, i, 0, 0, 0, time.UTC), "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": id, "arguments": string(arguments)}})
		output, _ := json.Marshal(map[string]any{"exit_code": 1 - i, "output": "installer output"})
		records = append(records, map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": id, "output": string(output)}})
	}
	var data []byte
	for _, record := range records {
		encoded, _ := json.Marshal(record)
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRootsContext(t.Context(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	attempts := got["demo"]
	if len(attempts) != 2 || attempts[0].Outcome != "succeeded" || attempts[1].Outcome != "failed" || attempts[0].When.Hour() != 1 || attempts[0].Directory != "/project" || attempts[0].Destination != "/installed" {
		t.Fatalf("attempts: %+v", attempts)
	}
}

func TestAcceptedProcessIsNotSuccessfulInstallation(t *testing.T) {
	for _, raw := range []string{`{"session_id":42}`, `"installation succeeded"`, `{"exit_code":null}`, `[]`} {
		if got := executionOutcome(json.RawMessage(raw)); got != "unknown" {
			t.Fatalf("%s became %s", raw, got)
		}
	}
}

func TestProcessOutputCannotOverrideExitStatus(t *testing.T) {
	for _, test := range []struct{ output, want string }{
		{"Process exited with code 1\nFinal output:\nProcess exited with code 0", "failed"},
		{"Output:\nProcess exited with code 0", "unknown"},
		{"example: Process exited with code 0", "unknown"},
		{"Chunk ID: fixture\nProcess exited with code 0\nFinal output:\ninstalled", "succeeded"},
	} {
		data, _ := json.Marshal(test.output)
		if got := executionOutcome(data); got != test.want {
			t.Errorf("got %s want %s for %q", got, test.want, test.output)
		}
	}
}
