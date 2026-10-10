package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Within decides containment, which gates root boundaries and mutation targets.
// A false positive would let an operation reach outside its root.
func TestWithin(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "a", "b")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(root, "c"), true},
		{filepath.Join(root, "c", "d"), true},
		{root, true},                           // the root itself
		{filepath.Join(sep, "a", "bc"), false}, // sibling sharing a prefix
		{filepath.Join(sep, "a"), false},       // parent
		// Cleans to /a/c, which is a sibling of the root, not a descendant.
		{filepath.Join(sep, "a", "b", "..", "c"), false},
		// Cleans to /a/b/c, which stays inside.
		{filepath.Join(sep, "a", "c", "..", "b", "c"), true},
	}
	for _, tc := range cases {
		if got := Within(root, tc.path); got != tc.want {
			t.Fatalf("Within(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
}

// PathKey must clean the path and fold case only where the platform does, so
// distinct paths are never merged on a case-sensitive filesystem.
func TestPathKeyFolding(t *testing.T) {
	// Build the expectation with the platform separator: Windows cleans to
	// `\a\c`, so a literal slash comparison would only pass on Unix.
	want := filepath.Join(string(filepath.Separator), "A", "C")
	if got := PathKey("/A/B/../C"); got != want {
		t.Fatalf("path was not cleaned: got %q want %q", got, want)
	}
	if runtime.GOOS != "windows" && PathKey("/A") == PathKey("/a") {
		t.Fatal("case was folded on a case-sensitive platform")
	}
	if runtime.GOOS == "windows" && PathKey(`C:\A`) != PathKey(`c:\a`) {
		t.Fatal("case was not folded on Windows")
	}
}

// Two Agent links pointing at one target must stay distinguishable, or two
// separate bindings would be collapsed into one.
func TestBindingPathKeepsLeafIdentity(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(base, "agent-a", "demo")
	second := filepath.Join(base, "agent-b", "demo")
	for _, link := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	keyA, keyB := PathKey(BindingPath(first)), PathKey(BindingPath(second))
	if keyA == keyB {
		t.Fatalf("distinct bindings collapsed to %q", keyA)
	}

	// The physical path resolves through the link, so both agree there.
	physicalA, physicalB := PhysicalPath(first), PhysicalPath(second)
	if !SamePath(physicalA, target) || !SamePath(physicalB, target) {
		t.Fatalf("physical path did not resolve: %q %q", physicalA, physicalB)
	}
}

// A parent that is itself a link must be resolved, while a missing leaf keeps
// its own identity.
func TestBindingPathResolvesExistingParents(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := BindingPath(filepath.Join(alias, "not-created-yet"))
	want := filepath.Join(real, "not-created-yet")
	if !SamePath(got, want) {
		t.Fatalf("alias parent was not resolved: got %q want %q", got, want)
	}
}

// A missing path must not panic and should fall back to its binding form.
func TestPhysicalPathFallsBackWhenMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	got := PhysicalPath(missing)
	if got == "" {
		t.Fatal("missing path produced an empty result")
	}
	if !SamePath(got, missing) {
		t.Fatalf("fallback changed the path: %q", got)
	}
	if BindingPath("") != "" {
		t.Fatal("empty path was not preserved")
	}
}
