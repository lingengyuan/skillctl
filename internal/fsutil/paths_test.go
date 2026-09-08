package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSamePathPreservesBindingIdentity(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, test := range []struct {
		left, right string
		want        bool
	}{
		{"", "", true}, {"", ".", false}, {".", root, true},
		{"alpha", filepath.Join(root, "alpha"), true}, {"alpha", "beta", false},
		{filepath.Join(root, "one", "demo"), filepath.Join(root, "two", "demo"), false},
	} {
		if got := SamePath(test.left, test.right); got != test.want {
			t.Errorf("SamePath(%q,%q)=%v", test.left, test.right, got)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "actual"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "actual"), filepath.Join(root, "alias")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if !SamePath(filepath.Join(root, "actual", "demo"), filepath.Join(root, "alias", "demo")) {
		t.Fatal("parent alias lost")
	}
	if SamePath(filepath.Join(root, "actual"), filepath.Join(root, "alias")) {
		t.Fatal("distinct link bindings merged")
	}
}
