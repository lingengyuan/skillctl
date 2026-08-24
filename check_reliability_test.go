package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeCheckReliabilitySkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: reliability test\n---\n", name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckScannerIgnoresSkillctlTransactionDirectories(t *testing.T) {
	root := t.TempDir()
	writeCheckReliabilitySkill(t, filepath.Join(root, "real"), "real-skill")
	writeCheckReliabilitySkill(t, filepath.Join(root, ".skillctl-provider-snapshot-123"), "snapshot-skill")
	writeCheckReliabilitySkill(t, filepath.Join(root, ".skillctl-stage-123"), "stage-skill")
	writeCheckReliabilitySkill(t, filepath.Join(root, ".skillctl-backup-123"), "backup-skill")

	var stderr bytes.Buffer
	items, failed := scan([]scanRoot{{Path: root, Host: "test", Scope: "user", Required: true}}, false, &stderr)
	if failed {
		t.Fatalf("scan failed: %s", stderr.String())
	}
	if len(items) != 1 || items[0].Name != "real-skill" {
		t.Fatalf("unexpected scan result: %#v", items)
	}
}

func TestCheckScannerStopsInsideValidSkillRoot(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	writeCheckReliabilitySkill(t, outer, "outer-skill")
	writeCheckReliabilitySkill(t, filepath.Join(outer, "references", "nested"), "nested-skill")

	var stderr bytes.Buffer
	items, failed := scan([]scanRoot{{Path: root, Host: "test", Scope: "user", Required: true}}, false, &stderr)
	if failed || len(items) != 1 || items[0].Name != "outer-skill" {
		t.Fatalf("unexpected scan result: failed=%v items=%#v stderr=%s", failed, items, stderr.String())
	}
}

func TestCheckSourceDisplayLabelRemovesCredentials(t *testing.T) {
	label := sourceDisplayLabel("https://user:secret@example.com/acme/skills.git?token=hidden#fragment", "main")
	if label != "example.com/acme/skills@main" {
		t.Fatalf("unexpected label %q", label)
	}
	if strings.Contains(label, "secret") || strings.Contains(label, "hidden") {
		t.Fatalf("credentials leaked in %q", label)
	}
}

func TestCheckSourceElapsedExcludesWorkerQueueTime(t *testing.T) {
	original := syncSourceForSession
	defer func() { syncSourceForSession = original }()

	gate := make(chan struct{})
	var calls atomic.Int32
	syncSourceForSession = func(ctx context.Context, source, ref string) (string, error) {
		n := calls.Add(1)
		if n <= maxConcurrentSourceChecks {
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		} else {
			time.Sleep(5 * time.Millisecond)
		}
		return source, nil
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(gate)
	}()

	var progress bytes.Buffer
	session := newSourceSession(context.Background(), time.Second, &progress)
	requests := make([]sourceRequest, 5)
	for i := range requests {
		requests[i] = sourceRequest{
			Source: fmt.Sprintf("https://example.com/source-%d.git", i+1),
			Skills: []string{fmt.Sprintf("skill-%d", i+1)},
		}
	}
	session.prefetch(requests)

	match := regexp.MustCompile(`Remote source 5(?:/5)? ready:.*\((\d+)ms\)`).FindStringSubmatch(progress.String())
	if len(match) != 2 {
		match = regexp.MustCompile(`Remote source 5 ready:.*\[5/5\].*\((\d+)ms\)`).FindStringSubmatch(progress.String())
	}
	if len(match) != 2 {
		t.Fatalf("source 5 timing missing:\n%s", progress.String())
	}
	millis, _ := strconv.Atoi(match[1])
	if millis >= 50 {
		t.Fatalf("queue delay leaked into execution time: %dms\n%s", millis, progress.String())
	}
}

func TestCheckMergedReportKeepsInstallationOwnership(t *testing.T) {
	first := reportFor(skill{Name: "shared", Path: "/first"}, "provider-a", "owner-a", nil, "clean", "up to date", false, "report-only", "")
	second := reportFor(skill{Name: "shared", Path: "/second"}, "provider-b", "owner-b", nil, "unknown", "provider check failed", false, "report-only", "network timeout")
	merged := mergeReportsByIdentity([]report{first, second})
	if len(merged) != 1 {
		t.Fatalf("unexpected merged reports: %#v", merged)
	}
	if merged[0].Path != "" {
		t.Fatalf("merged report retained a misleading path: %#v", merged[0])
	}
	if len(merged[0].Installations) != 2 {
		t.Fatalf("installation detail lost: %#v", merged[0])
	}
	if merged[0].Installations[0].Path != "/first" || merged[0].Installations[1].Path != "/second" {
		t.Fatalf("installation paths were reassigned: %#v", merged[0].Installations)
	}
}

func TestCheckSharedSourceFailurePrintsOnce(t *testing.T) {
	err := &sourceSyncError{Key: "object\x00source", Label: "example.com/acme/skills@main", Stage: "fetch", Err: context.DeadlineExceeded}
	first := reportFor(skill{Name: "alpha", Path: "/alpha"}, "provider", "provider", nil, "unknown", "provider check failed", false, "report-only", err.Error())
	second := reportFor(skill{Name: "beta", Path: "/beta"}, "provider", "provider", nil, "unknown", "provider check failed", false, "report-only", err.Error())
	attachSourceFailure(&first, err)
	attachSourceFailure(&second, err)

	var output bytes.Buffer
	printReports(&output, []report{first, second})
	text := output.String()
	if strings.Count(text, "example.com/acme/skills@main") != 1 {
		t.Fatalf("shared failure was not collapsed:\n%s", text)
	}
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "beta") {
		t.Fatalf("affected skills missing:\n%s", text)
	}
}
