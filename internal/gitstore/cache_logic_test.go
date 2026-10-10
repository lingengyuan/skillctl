package gitstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A ref that looks like a hex commit may be one, so it must be tried as a
// commit rather than only as a branch or tag.
func TestIsCommitRef(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"abc1234", true},
		{"3884ed79442d00ca47e62e16b2aacb1f5a91ae6e", true},
		{"ABCDEF1", true},
		{"abc", false},       // too short to be a commit
		{"v1.0.0", false},    // tag name, not hex
		{"main", false},      // branch name
		{"deadbeefg", false}, // 'g' is not hex
		{"", false},          // empty
		{"feature/x", false}, // contains a separator
	}
	for _, tc := range cases {
		if got := isCommitRef(tc.ref); got != tc.want {
			t.Fatalf("isCommitRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

// A cache directory without a usable worktree must be rejected, or a corrupt
// cache would be reused silently.
func TestValidWorktreeRejectsUnusableCaches(t *testing.T) {
	base := t.TempDir()

	// No .git at all.
	empty := filepath.Join(base, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if ValidWorktree(empty) {
		t.Fatal("directory without .git was accepted")
	}

	// A .git directory that is not a valid repository.
	fake := filepath.Join(base, "fake")
	if err := os.MkdirAll(filepath.Join(fake, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ValidWorktree(fake) {
		t.Fatal("bogus .git was accepted as a worktree")
	}

	// A missing path.
	if ValidWorktree(filepath.Join(base, "absent")) {
		t.Fatal("missing path was accepted")
	}
}

// Only interrupted clone stages are stale. A fresh stage may belong to a
// running process and must be kept.
func TestStaleCloneDirectoriesRespectsAge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("USERPROFILE", home)

	base := mustCacheDirectory(t)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(base, ".skillctl-source-clone-old")
	fresh := filepath.Join(base, ".skillctl-source-clone-fresh")
	other := filepath.Join(base, "object-abcdef.git")
	for _, dir := range []string{old, fresh, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	stale, err := StaleCloneDirectories(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != old {
		t.Fatalf("expected only the old stage, got %v", stale)
	}

	// A window smaller than the age keeps everything.
	all, err := StaleCloneDirectories(72 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("fresh stages were reported as stale: %v", all)
	}
}

// An absent cache directory is not an error: a machine that never cloned
// anything must simply report nothing.
func TestStaleCloneDirectoriesWithoutCache(t *testing.T) {
	// The cache location differs per platform, so redirect every variable the
	// resolver consults. Without this the test would read the developer's real
	// cache and could observe unrelated directories.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("USERPROFILE", home)

	// Nothing was ever cloned, so the directory does not exist yet.
	if _, err := os.Stat(mustCacheDirectory(t)); !os.IsNotExist(err) {
		t.Skipf("cache directory already exists: %v", err)
	}

	stale, err := StaleCloneDirectories(time.Second)
	if err != nil {
		t.Fatalf("missing cache directory reported an error: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("unexpected results: %v", stale)
	}
}

// mustCacheDirectory returns the resolved source cache directory.
func mustCacheDirectory(t *testing.T) string {
	t.Helper()
	dir, err := sourceCacheDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
