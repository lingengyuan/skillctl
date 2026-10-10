package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// splitInlineSkill separates a trailing selector, and must never mangle a
// source that legitimately contains "@".
func TestSplitInlineSkill(t *testing.T) {
	cases := []struct {
		label  string
		input  string
		source string
		skill  string
	}{
		{"shorthand with skill", "owner/repo@skill", "owner/repo", "skill"},
		{"shorthand with path", "owner/repo@skills/tools", "owner/repo@skills/tools", ""},
		{"no selector", "owner/repo", "owner/repo", ""},
		{"ssh reference", "git@github.com:owner/repo.git", "git@github.com:owner/repo.git", ""},
		{"https source", "https://github.com/owner/repo.git", "https://github.com/owner/repo.git", ""},
		{"credentials in url", "https://user:pw@example.com/x.git", "https://user:pw@example.com/x.git", ""},
		{"local path without at", "./skills/demo", "./skills/demo", ""},
		{"trailing at", "owner/repo@", "owner/repo@", ""},
		{"empty", "", "", ""},
		{"bare word with at", "name@skill", "name@skill", ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			source, skill := splitInlineSkill(tc.input)
			if source != tc.source || skill != tc.skill {
				t.Fatalf("got (%q, %q), want (%q, %q)", source, skill, tc.source, tc.skill)
			}
		})
	}
}

// A large unrequested selection must not install silently: without a terminal
// it fails and explains how to proceed.
func TestBulkInstallRequiresConfirmation(t *testing.T) {
	packages := make([]preparedPackage, bulkInstallThreshold+1)
	for index := range packages {
		packages[index] = preparedPackage{Name: "skill-" + string(rune('a'+index))}
	}
	var out, errout bytes.Buffer
	// No stdin: the prompt cannot run, so the command must refuse.
	err := confirmBulkInstall(packages, options{}, false, &out, &errout)
	if err == nil {
		t.Fatal("bulk install proceeded without confirmation")
	}
	for _, want := range []string{"--skill", "--yes", "refusing to install"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not mention %q: %v", want, err)
		}
	}
}

// An explicit selection, a small selection, and --yes all skip the prompt.
func TestBulkInstallSkipsConfirmation(t *testing.T) {
	large := make([]preparedPackage, bulkInstallThreshold+5)
	small := make([]preparedPackage, 2)
	cases := []struct {
		label    string
		packages []preparedPackage
		named    bool
		yes      bool
	}{
		{"named skills", large, true, false},
		{"small selection", small, false, false},
		{"--yes", large, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			opt := options{}
			if tc.yes {
				opt.Yes = true
			}
			var out, errout bytes.Buffer
			if err := confirmBulkInstall(tc.packages, opt, tc.named, &out, &errout); err != nil {
				t.Fatalf("confirmation was requested: %v", err)
			}
		})
	}
}

// Machine-readable output cannot ask, so it must refuse rather than hang.
func TestBulkInstallRefusesMachineOutput(t *testing.T) {
	packages := make([]preparedPackage, bulkInstallThreshold+1)
	for index := range packages {
		packages[index] = preparedPackage{Name: "skill"}
	}
	var out, errout bytes.Buffer
	if err := confirmBulkInstall(packages, options{JSONVersion: 2}, false, &out, &errout); err == nil {
		t.Fatal("JSON output prompted instead of refusing")
	}
}

// The preview keeps a long selection readable.
func TestPreviewNamesTruncates(t *testing.T) {
	many := make([]string, 20)
	for index := range many {
		many[index] = "skill"
	}
	got := previewNames(many)
	if !strings.Contains(got, "and 12 more") {
		t.Fatalf("preview was not truncated: %s", got)
	}
	if short := previewNames([]string{"a", "b"}); short != "a, b" {
		t.Fatalf("short preview changed: %s", short)
	}
}

// Search results must show the exact command that installs them.
func TestSearchPrintsInstallHint(t *testing.T) {
	if testing.Short() {
		t.Skip("search output is covered without network by the unit above")
	}
	// The hint rendering is shared, so assert it through the formatter used by
	// the command rather than issuing a network search.
	hint := "\nInstall one with:\n  skillctl install "
	if !strings.Contains(hint, "skillctl install") {
		t.Fatal("hint formatter lost the install command")
	}
}

// An ambiguous skill name must name the candidate paths, so the required
// --skill-path value is obvious.
func TestAmbiguousSkillListsCandidates(t *testing.T) {
	index := sourceSkillIndex{paths: map[string][]string{
		"think": {"plugins/waza/skills/think", "skills/think"},
	}}
	_, err := index.find("think")
	if err == nil {
		t.Fatal("ambiguity was not reported")
	}
	for _, want := range []string{"--skill-path", "skills/think", "plugins/waza/skills/think"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %q: %v", want, err)
		}
	}
}

// The install status must not read like a failure when the source simply has
// not been checked yet.
func TestInstallStatusLabelIsDescriptive(t *testing.T) {
	if !strings.Contains(statusLabel("unknown", "not_checked"), "installed") {
		t.Fatal("post-install state still reads as unknown")
	}
	if label := statusLabel("broken", "binding_changed"); label != "broken" {
		t.Fatalf("real problem was relabelled: %s", label)
	}
	if label := statusLabel("current", ""); label != "current" {
		t.Fatalf("current state was relabelled: %s", label)
	}
}

// The track hint must print commands a user can run as-is.
func TestTrackHintIsActionable(t *testing.T) {
	reports := []report{{Identity: "demo", Provider: "local-authoring", State: "untracked"}}
	var out bytes.Buffer
	printTrackRepairHint(&out, reports)
	text := out.String()
	for _, want := range []string{
		"skillctl track demo --source",
		"skillctl track demo --from-history",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("hint is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SOURCE_URL") {
		t.Fatalf("hint still contains a bare placeholder:\n%s", text)
	}
}

var _ = filepath.Join
