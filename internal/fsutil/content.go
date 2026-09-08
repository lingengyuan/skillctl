package fsutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// IgnoreContent excludes version-control and skillctl transaction directories from Skill content.
func IgnoreContent(rel string) bool {
	for component := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		switch component {
		case ".git", ".hg", ".svn":
			return true
		}
		if strings.HasPrefix(component, ".skillctl-stage-") ||
			strings.HasPrefix(component, ".skillctl-backup-") ||
			strings.HasPrefix(component, ".skillctl-provider-snapshot-") ||
			strings.HasPrefix(component, ".skillctl-restore-") {
			return true
		}
	}
	return false
}

// HashDirectory hashes content with UTF-8 line endings normalized and version-control state excluded.
func HashDirectory(root string) (string, error) {
	return HashDirectoryContext(context.Background(), root)
}

// HashDirectoryContext is the cancellable form of HashDirectory, using the
// same persisted digest format for compatibility with existing baselines.
func HashDirectoryContext(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." && IgnoreContent(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.Sort(paths)
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		var content []byte
		isSymlink := info.Mode()&os.ModeSymlink != 0
		if isSymlink {
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			content = []byte("symlink\x00" + target)
		} else {
			content, err = os.ReadFile(path)
			if err != nil {
				return "", err
			}
		}
		if !isSymlink && utf8.Valid(content) {
			content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(content))
		if _, err := hash.Write(content); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CopyDirectory copies files and directories, preserving file permissions and rejecting symlinks.
func CopyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel != "." && IgnoreContent(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains unsupported symlink: %s", rel)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, info.Mode().Perm())
	})
}
