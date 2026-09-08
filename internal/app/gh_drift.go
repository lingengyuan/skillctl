package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
	"gopkg.in/yaml.v3"
)

func comparableGHDocument(data []byte) ([]byte, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	front, err := skilldoc.FrontMatter(data)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := yaml.Unmarshal(front, &document); err != nil {
		return nil, err
	}
	if metadata, ok := document["metadata"].(map[string]any); ok {
		for _, key := range []string{"github-repo", "github-ref", "github-tree-sha", "github-path", "github-pinned", "local-path"} {
			delete(metadata, key)
		}
		if len(metadata) == 0 {
			delete(document, "metadata")
		}
	}
	canonical, err := yaml.Marshal(document)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		if bytes.Equal(lines[i], []byte("---")) {
			return append(append(canonical, []byte("\n---\n")...), bytes.Join(lines[i+1:], []byte("\n"))...), nil
		}
	}
	return nil, fmt.Errorf("unterminated skill document")
}

func checkGHLocalDrift(session *sourceSession, claim ghSkillClaim, installed string) (string, error) {
	cache, err := session.source(ghRepositoryURL(claim.Repository), claim.Ref)
	if err != nil {
		return "unknown", err
	}
	files, err := collectGitTreeFiles(session, cache, claim.TreeSHA, "")
	if err != nil {
		return "unknown", err
	}
	expected := map[string][]byte{}
	for _, file := range files {
		object, err := session.gitObject(cache, file.Object)
		if err != nil {
			return "unknown", err
		}
		data := object.Data
		if file.Mode == "120000" {
			data = append([]byte("symlink\x00"), data...)
		} else if file.Path == "SKILL.md" {
			data, err = comparableGHDocument(data)
			if err != nil {
				return "unknown", err
			}
		} else {
			data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		}
		expected[file.Path] = data
	}
	actualPaths := []string{}
	installed = fsutil.PhysicalPath(installed)
	modified := false
	err = filepath.WalkDir(installed, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(installed, path)
		if err != nil {
			return err
		}
		if rel != "." && fsutil.IgnoreContent(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel = filepath.ToSlash(rel)
		actualPaths = append(actualPaths, rel)
		var data []byte
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			data = []byte("symlink\x00" + target)
		} else {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
			if rel == "SKILL.md" {
				data, err = comparableGHDocument(data)
				if err != nil {
					return err
				}
			} else {
				data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			}
		}
		baseline, found := expected[rel]
		if !found || !bytes.Equal(baseline, data) {
			modified = true
		}
		return nil
	})
	if err != nil {
		return "unknown", err
	}
	if len(actualPaths) != len(expected) {
		modified = true
	}
	if modified {
		return "modified", nil
	}
	return "clean", nil
}
