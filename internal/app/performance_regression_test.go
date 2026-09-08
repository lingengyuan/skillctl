package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingengyuan/skillctl/internal/installhistory"
)

func TestHistoryRecoveryDoesNotFetchUnrelatedWildcard(t *testing.T) {
	requireTestGit(t)
	home := setTestHome(t)
	source := newTestSourceRepository(t)
	writeTestSkill(t, filepath.Join(source, "unrelated"), "unrelated", "source")
	commitTestSource(t, source, "initial")
	history := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	// A repository-only record is not evidence for these unknown installations.
	command := fmt.Sprintf("npx skills add '%s' -g -y", filepath.ToSlash(source))
	line := fmt.Sprintf(`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":%q}}`+"\n", fmt.Sprintf(`{"cmd":%q}`, command))
	if err := os.WriteFile(filepath.Join(history, "install.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	var items []skill
	for i := range 20 {
		name := fmt.Sprintf("unknown-%d", i)
		path := filepath.Join(home, "installed", name)
		writeTestSkill(t, path, name, "local")
		items = append(items, skill{Name: name, Path: path})
	}
	previous := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = previous })
	calls := 0
	syncSourceForSession = func(context.Context, string, string) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	}
	state := &trackedState{Version: 1, readOnly: true}
	var diagnostics strings.Builder
	if trackFromInstallHistory(t.Context(), time.Second, items, state, nil, nil, false, io.Discard, &diagnostics) {
		t.Fatal("unrelated evidence failed the check")
	}
	if calls != 0 {
		t.Fatalf("unrelated source synchronized %d times, want 0", calls)
	}
	if len(state.Skills) != 0 {
		t.Fatal("failed evidence was registered")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unrelated verification reported: %s", &diagnostics)
	}
}

func TestRecoveryIndexStillVerifiesEverySkill(t *testing.T) {
	requireTestGit(t)
	home := setTestHome(t)
	source := newTestSourceRepository(t)
	var items []skill
	for _, name := range []string{"first", "modified", "last"} {
		writeTestSkill(t, filepath.Join(source, name), name, "source")
		body := "source"
		if name == "modified" {
			body = "local edits"
		}
		path := filepath.Join(home, "installed", name)
		writeTestSkill(t, path, name, body)
		items = append(items, skill{Name: name, Path: path})
	}
	commitTestSource(t, source, "initial")
	previous := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = previous })
	calls := 0
	syncSourceForSession = func(context.Context, string, string) (string, error) {
		calls++
		return source, nil
	}
	state := &trackedState{Version: 1, readOnly: true}
	candidates := map[string][]installhistory.Candidate{"": {{Source: source, Outcome: "succeeded", Destination: filepath.Join(home, "installed")}}}
	var diagnostics strings.Builder
	if !recoverInstallCandidates(t.Context(), time.Second, items, state, candidates, false, io.Discard, &diagnostics) {
		t.Fatal("modified content was accepted")
	}
	if calls != 1 || len(state.Skills) != 2 {
		t.Fatalf("calls=%d verified=%d; want 1 and 2", calls, len(state.Skills))
	}
	for _, item := range items {
		_, tracked := state.findSkill(item)
		if tracked != (item.Name != "modified") {
			t.Fatalf("wrong verification for %s: %v", item.Name, tracked)
		}
	}
}

func TestSourceIndexPreservesAmbiguityAndIgnoredDirectories(t *testing.T) {
	root := newTestSourceRepository(t)
	for path, name := range map[string]string{"first": "duplicate", "second": "duplicate", "only": "unique", ".git/ignored": "unique"} {
		writeTestSkill(t, filepath.Join(root, path), name, "content")
	}
	commitTestSource(t, root, "source index fixture")
	session := newSourceSession(t.Context(), time.Second, io.Discard)
	defer session.close()
	index := session.sourceIndex(root)
	if _, err := index.find("duplicate"); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguity was lost: %v", err)
	}
	if path, err := index.find("unique"); err != nil || path != "only" {
		t.Fatalf("ignored directory included: %s %v", path, err)
	}
	if _, err := index.find("missing"); err == nil {
		t.Fatal("missing name was accepted")
	}
}
