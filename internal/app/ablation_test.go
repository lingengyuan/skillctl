//go:build ablation

package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingengyuan/skillctl/internal/installhistory"
)

// These benchmarks run the production recovery path with deterministic source
// adapters. They neither contact remotes nor read the user's installation history.
func BenchmarkAblationRecoveryFailed(b *testing.B) {
	root := b.TempDir()
	var items []skill
	candidates := map[string][]installhistory.Candidate{}
	for i := range 32 {
		name := fmt.Sprintf("sample-%02d", i)
		items = append(items, skill{Name: name, Path: filepath.Join(root, name)})
		candidates[name] = []installhistory.Candidate{{Source: fmt.Sprintf("https://example.invalid/repo-%d.git", i%8), Name: name, Outcome: "succeeded"}}
	}
	previous := syncSourceForSession
	b.Cleanup(func() { syncSourceForSession = previous })
	var calls atomic.Int64
	syncSourceForSession = func(ctx context.Context, _, _ string) (string, error) {
		calls.Add(1)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return "", context.DeadlineExceeded
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		state := &trackedState{Version: 1, readOnly: true}
		if !recoverInstallCandidates(b.Context(), time.Second, items, state, candidates, false, io.Discard, io.Discard) || len(state.Skills) != 0 {
			b.Fatal("failed source changed verification semantics")
		}
	}
	b.ReportMetric(float64(calls.Load())/float64(b.N), "syncs/op")
}

func BenchmarkAblationRecoveryVerified(b *testing.B) {
	root := b.TempDir()
	source := filepath.Join(root, "source")
	var items []skill
	for i := range 200 {
		name := fmt.Sprintf("sample-%03d", i)
		content := fmt.Sprintf("---\nname: %s\ndescription: Ablation fixture\n---\nOriginal content.\n", name)
		writeAblationSkill(b, filepath.Join(source, name), content)
		if i < 20 {
			installed := filepath.Join(root, "installed", name)
			writeAblationSkill(b, installed, content)
			items = append(items, skill{Name: name, Path: installed})
		}
	}
	previous := syncSourceForSession
	b.Cleanup(func() { syncSourceForSession = previous })
	var calls atomic.Int64
	syncSourceForSession = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return source, nil
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if output, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git: %s %v", output, err)
		}
	}
	candidates := map[string][]installhistory.Candidate{"": {{Source: source, Outcome: "succeeded", Destination: filepath.Join(root, "installed")}}}
	b.ReportAllocs()
	for b.Loop() {
		state := &trackedState{Version: 1, readOnly: true}
		if recoverInstallCandidates(b.Context(), time.Second, items, state, candidates, false, io.Discard, io.Discard) || len(state.Skills) != len(items) {
			b.Fatalf("verified=%d, want %d", len(state.Skills), len(items))
		}
		for _, item := range items {
			entry, ok := state.findSkill(item)
			if !ok || entry.Source != source || entry.SkillPath != item.Name || entry.InstalledHash == "" {
				b.Fatal("candidate identity or content verification changed")
			}
		}
	}
	b.ReportMetric(float64(calls.Load())/float64(b.N), "syncs/op")
	b.ReportMetric(float64(len(items)), "verified/op")
}

func writeAblationSkill(b *testing.B, dir, content string) {
	b.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		b.Fatal(err)
	}
}
