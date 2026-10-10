package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// Every command that users can run must document itself, so
// `skillctl COMMAND --help` never falls back to the general reference.
func TestCommandUsageCoversEveryCommand(t *testing.T) {
	commands := []string{
		"list", "show", "check", "diff", "doctor", "search", "install",
		"enable", "disable", "remove", "update", "pin", "unpin",
		"history", "rollback", "track", "sync", "import", "export",
		"profile", "config", "completion", "version",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			if !CommandUsage(&out, command) {
				t.Fatalf("%s has no dedicated help", command)
			}
			text := out.String()
			for _, want := range []string{"Usage:", "skillctl " + command} {
				if !strings.Contains(text, want) {
					t.Fatalf("%s help is missing %q:\n%s", command, want, text)
				}
			}
		})
	}
}

// Help must be specific: a command page differs from the full reference and
// does not print the whole command list.
func TestCommandUsageIsSpecificNotGlobal(t *testing.T) {
	var command bytes.Buffer
	if !CommandUsage(&command, "install") {
		t.Fatal("install help missing")
	}
	var global bytes.Buffer
	Usage(&global)
	if command.String() == global.String() {
		t.Fatal("command help duplicates the global reference")
	}
	if strings.Contains(command.String(), "config gc") {
		t.Fatal("command help leaked the global command list")
	}
}

// Commands with options must show them, and commands with examples must show
// an example, since that is the part a first-time user needs.
func TestCommandUsageShowsOptionsAndExamples(t *testing.T) {
	var install bytes.Buffer
	if !CommandUsage(&install, "install") {
		t.Fatal("install help missing")
	}
	text := install.String()
	if !strings.Contains(text, "Options:") || !strings.Contains(text, "--skill") {
		t.Fatalf("install help does not document --skill:\n%s", text)
	}
	if !strings.Contains(text, "Examples:") {
		t.Fatalf("install help has no examples:\n%s", text)
	}
}

// An unknown command must not claim to have help, so the caller can fall back.
func TestCommandUsageRejectsUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if CommandUsage(&out, "frobnicate") {
		t.Fatal("unknown command reported dedicated help")
	}
	if out.Len() != 0 {
		t.Fatalf("unknown command wrote output: %s", out.String())
	}
}

// Commands lists exactly the documented commands, sorted.
func TestCommandsListsDocumentedCommands(t *testing.T) {
	names := Commands()
	if len(names) == 0 {
		t.Fatal("no commands listed")
	}
	for index := 1; index < len(names); index++ {
		if names[index-1] >= names[index] {
			t.Fatalf("commands are not sorted: %v", names)
		}
	}
	if !strings.Contains(strings.Join(names, ","), "install") {
		t.Fatalf("install missing from command list: %v", names)
	}
}

// Every option a command documents must also appear in the global reference,
// so a flag cannot be added to a command page and forgotten in the overview.
func TestGlobalUsageCoversEveryCommandOption(t *testing.T) {
	optionPattern := regexp.MustCompile(`(?m)^\s+(--[a-z][a-z-]*)`)
	var global bytes.Buffer
	Usage(&global)
	globalText := global.String()

	for _, command := range Commands() {
		var page bytes.Buffer
		if !CommandUsage(&page, command) {
			t.Fatalf("%s has no help page", command)
		}
		// Only the Options section documents flags that must appear globally.
		index := strings.Index(page.String(), "Options:")
		if index < 0 {
			continue
		}
		section := page.String()[index:]
		if end := strings.Index(section, "Examples:"); end >= 0 {
			section = section[:end]
		}
		for _, match := range optionPattern.FindAllStringSubmatch(section, -1) {
			flag := match[1]
			if !strings.Contains(globalText, flag) {
				t.Fatalf("%s documents %s, but the global reference does not list it", command, flag)
			}
		}
	}
}
