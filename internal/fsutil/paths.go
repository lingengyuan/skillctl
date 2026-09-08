package fsutil

import (
	"path/filepath"
	"strings"
)

// Within checks lexical containment; callers must resolve symlinks separately when needed.
func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PathKey normalizes a path for platform-appropriate identity comparisons.
func PathKey(path string) string {
	clean := filepath.Clean(path)
	if filepath.Separator == '\\' {
		return strings.ToLower(clean)
	}
	return clean
}

// BindingPath resolves aliases in existing parents while retaining the identity of the leaf.
// Two Agent symlinks remain distinct bindings even when they share a target.
func BindingPath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	parent := filepath.Dir(absolute)
	tail := []string{filepath.Base(absolute)}
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved
		}
		next := filepath.Dir(parent)
		if next == parent {
			return absolute
		}
		tail = append(tail, filepath.Base(parent))
		parent = next
	}
}

// PhysicalPath resolves the link target when available, otherwise retaining the binding path.
func PhysicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return BindingPath(path)
}

// SamePath compares binding paths with Windows case folding when applicable.
func SamePath(left, right string) bool {
	left, right = BindingPath(left), BindingPath(right)
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
