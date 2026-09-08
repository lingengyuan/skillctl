package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func TestCommandSharesSourceAcrossCatalogTrackedAndHistory(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	source := newTestSourceRepository(t)
	for _, name := range []string{"managed", "tracked", "recovered"} {
		writeTestSkill(t, filepath.Join(source, name), name, "original")
	}
	commitTestSource(t, source, "initial")
	const sourceURL = "https://github.com/fixture/shared.git"
	original := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = original })
	var calls atomic.Int64
	checking := false
	syncSourceForSession = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		if checking {
			lock, err := acquireCommandLock()
			if err != nil {
				return "", fmt.Errorf("network phase held operation lock: %w", err)
			}
			lock.release()
		}
		return source, nil
	}
	runV2(t, config, "install", "git+"+sourceURL, "--skill", "managed", "--ref", "main", "--host", "codex")
	for _, name := range []string{"tracked", "recovered"} {
		if err := fsutil.CopyDirectory(filepath.Join(source, name), filepath.Join(home, "codex", name)); err != nil {
			t.Fatal(err)
		}
	}
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	trackedPath := filepath.Join(home, "codex", "tracked")
	digest, _ := fsutil.HashDirectory(trackedPath)
	state.put(trackedEntry{Path: trackedPath, Source: sourceURL, Ref: "main", SkillPath: "tracked", InstalledHash: digest})
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("npx skills add https://github.com/fixture/shared/tree/main --skill recovered --dir %q", filepath.Join(home, "codex"))
	arguments, _ := json.Marshal(map[string]string{"cmd": command})
	call, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "recovery", "arguments": string(arguments)}})
	record := string(call) + "\n" + `{"type":"response_item","payload":{"type":"function_call_output","call_id":"recovery","output":"{\"exit_code\":0}"}}` + "\n"
	if err := os.WriteFile(filepath.Join(history, "install.jsonl"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	checking = true
	result := runV2(t, config, "check")
	if calls.Load() != 1 {
		t.Fatalf("shared source fetched %d times", calls.Load())
	}
	if len(result.Items) != 3 {
		t.Fatalf("items=%d", len(result.Items))
	}
	for _, asset := range result.Items {
		if asset.Upstream.Status != "current" {
			t.Fatalf("not checked: %+v", asset)
		}
	}
	state, err = loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	entry, found := state.find(filepath.Join(home, "codex", "recovered"))
	if !found || entry.VerifiedRevision == "" || !strings.Contains(entry.EvidenceID, "install.jsonl:") {
		t.Fatalf("recovery was not committed with provenance: %+v", entry)
	}
}

func TestRecoveryDeadlineLeavesKnownSourceUsable(t *testing.T) {
	original := syncSourceForSession
	t.Cleanup(func() { syncSourceForSession = original })
	syncSourceForSession = func(ctx context.Context, source, _ string) (string, error) {
		if source == "known" {
			return "fixture-cache", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	root := newSourceSession(t.Context(), time.Second, io.Discard)
	ctx := context.WithValue(t.Context(), sourceSessionContextKey{}, root)
	root.ctx = ctx
	defer root.close()
	if _, err := root.source("known", "main"); err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	child, cleanup := commandSourceSession(limited, time.Second, io.Discard)
	defer cleanup()
	if _, err := child.source("optional", "main"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("recovery canceled parent command")
	}
	if got, err := root.source("known", "main"); err != nil || got != "fixture-cache" {
		t.Fatalf("lost known source: %s %v", got, err)
	}
}
