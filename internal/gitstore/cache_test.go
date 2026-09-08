package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceCacheLockReleasesOnProcessExit(t *testing.T) {
	if cache := os.Getenv("SKILLCTL_TEST_CACHE_CRASH"); cache != "" {
		if _, err := acquireSourceCacheLock(context.Background(), cache); err != nil {
			os.Exit(21)
		}
		os.Exit(0) // Deliberately bypass release, as an interrupted CLI would.
	}
	cache := filepath.Join(t.TempDir(), "source")
	child := exec.Command(os.Args[0], "-test.run=^TestSourceCacheLockReleasesOnProcessExit$")
	child.Env = append(os.Environ(), "SKILLCTL_TEST_CACHE_CRASH="+cache)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	lock, err := acquireSourceCacheLock(ctx, cache)
	if err != nil {
		t.Fatalf("exited process still blocks source cache: %v", err)
	}
	lock.release()
}

func TestSourceCacheLockNeverExpiresWhileHeld(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "source")
	first, err := acquireSourceCacheLock(t.Context(), cache)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cache+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	second, err := acquireSourceCacheLock(ctx, cache)
	if second != nil {
		second.release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active lock was stolen because of its age: %v", err)
	}
	first.release()
	third, err := acquireSourceCacheLock(t.Context(), cache)
	if err != nil {
		t.Fatal(err)
	}
	third.release()
}

func TestSourceCacheLockRespectsLiveLegacyOwner(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "source")
	marker := []byte(fmt.Sprintf("pid=%d\ncreated=legacy\n", os.Getpid()))
	if err := os.WriteFile(cache+".lock", marker, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	lock, err := acquireSourceCacheLock(ctx, cache)
	if lock != nil {
		lock.release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live legacy owner was displaced: %v", err)
	}
	data, err := os.ReadFile(cache + ".lock")
	if err != nil || string(data) != string(marker) {
		t.Fatalf("active legacy marker was changed: %q %v", data, err)
	}
}

func TestSourceCacheLockUpgradesExitedLegacyOwner(t *testing.T) {
	if cache := os.Getenv("SKILLCTL_TEST_LEGACY_CRASH"); cache != "" {
		if err := os.WriteFile(cache+".lock", []byte(fmt.Sprintf("pid=%d\ncreated=legacy\n", os.Getpid())), 0600); err != nil {
			os.Exit(21)
		}
		os.Exit(0)
	}
	cache := filepath.Join(t.TempDir(), "source")
	child := exec.Command(os.Args[0], "-test.run=^TestSourceCacheLockUpgradesExitedLegacyOwner$")
	child.Env = append(os.Environ(), "SKILLCTL_TEST_LEGACY_CRASH="+cache)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	lock, err := acquireSourceCacheLock(ctx, cache)
	if err != nil {
		t.Fatalf("exited legacy process still blocks cache: %v", err)
	}
	lock.release()
}

func TestSyncObjectSourceRefreshesDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("XDG_CACHE_HOME", root)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(source, "init", "-b", "main")
	git(source, "config", "user.name", "skillctl test")
	git(source, "config", "user.email", "skillctl@example.invalid")
	var cache string
	for _, content := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(source, "README.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		git(source, "add", ".")
		git(source, "commit", "-m", content)
		var err error
		cache, err = SyncObject(t.Context(), source, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := git(cache, "rev-parse", "HEAD"), git(source, "rev-parse", "HEAD"); got != want {
			t.Fatalf("cached default branch = %s, want %s", got, want)
		}
	}
}
