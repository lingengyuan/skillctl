package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHostSelectionAcceptsNamesAndNumbers(t *testing.T) {
	names := []string{"codex", "claude", "cursor"}
	cases := []struct {
		label string
		input string
		want  []string
	}{
		{"number", "2\n", []string{"claude"}},
		{"name", "cursor\n", []string{"cursor"}},
		{"alias", "claude-code\n", []string{"claude"}},
		{"multiple numbers", "1,3\n", []string{"codex", "cursor"}},
		{"mixed separators", " 1 ; cursor , 2 ", []string{"codex", "cursor", "claude"}},
		{"blank takes default", "\n", []string{"codex"}},
		{"deduplicates", "1,1,codex\n", []string{"codex"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, err := parseHostSelection(tc.input, names)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseHostSelectionRejectsInvalidInput(t *testing.T) {
	names := []string{"codex", "claude"}
	cases := []struct {
		label string
		input string
	}{
		{"out of range number", "9\n"},
		{"zero index", "0\n"},
		{"unknown name", "bogus\n"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if _, err := parseHostSelection(tc.input, names); err == nil {
				t.Fatal("invalid selection was accepted")
			}
		})
	}
}

// The non-interactive failure must name the valid Agents so the message is
// actionable without another lookup.
func TestNoAgentErrorListsAvailableHosts(t *testing.T) {
	err := noAgentError()
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, name := range []string{"codex", "claude", "cursor", "--host"} {
		if !strings.Contains(message, name) {
			t.Fatalf("message is missing %q: %s", name, message)
		}
	}
}

// Piped input must never prompt, so scripts and CI stay deterministic.
func TestInteractiveInputRejectsNonTerminal(t *testing.T) {
	if interactiveInput(bytes.NewReader([]byte("1\n"))) {
		t.Fatal("a non-file reader was treated as a terminal")
	}
}

// A prompt must write the menu to stderr and leave stdout for machine output.
func TestPromptInstallHostsUsesGivenStreams(t *testing.T) {
	choices := []hostChoice{
		{Name: "codex", Path: "/home/u/.agents/skills"},
		{Name: "claude", Path: "/home/u/.claude/skills"},
	}
	var menu bytes.Buffer
	hosts, err := promptInstallHosts(strings.NewReader("claude\n"), &menu, choices)
	if err != nil {
		t.Fatalf("prompt failed: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "claude" {
		t.Fatalf("unexpected hosts: %v", hosts)
	}
	for _, want := range []string{"No Agent detected", "codex", "claude", "/home/u/.claude/skills", "Installing to: claude"} {
		if !strings.Contains(menu.String(), want) {
			t.Fatalf("menu is missing %q: %s", want, menu.String())
		}
	}
}

// The menu must offer installable Agents beyond the six canonical defaults,
// because --host accepts them and installing to them works. The "universal"
// shared-directory alias must be absent: it resolves to multiple roots, so
// offering it would produce a "multiple roots" failure.
func TestInstallHostChoicesIncludeExtendedAgents(t *testing.T) {
	choices := installHostChoices(nil, "user", "")
	names := map[string]bool{}
	for _, choice := range choices {
		names[choice.Name] = true
	}
	for _, want := range []string{"codex", "claude", "trae", "kiro", "qoder"} {
		if !names[want] {
			t.Fatalf("choices are missing %q: %v", want, names)
		}
	}
	if names["universal"] {
		t.Fatalf("universal alias must not be offered as a target: %v", names)
	}
	for _, choice := range choices {
		if choice.Path == "" {
			t.Fatalf("choice %q has no target path", choice.Name)
		}
	}
}

// Every Agent offered by the menu must actually accept an install target,
// so the prompt never leads a user into a guaranteed failure.
func TestInstallHostChoicesAreRealTargets(t *testing.T) {
	home := setTestHome(t)
	roots := projectRoots(filepath.Join(home, "proj"))
	for _, scope := range []string{"user", "project"} {
		for _, choice := range installHostChoices(roots, scope, filepath.Join(home, "proj")) {
			if choice.Name == "universal" {
				t.Fatal("universal must not be offered")
			}
		}
	}
}
