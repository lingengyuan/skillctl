package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
)

type sourceSessionContextKey struct{}

func commandSourceSession(ctx context.Context, timeout time.Duration, progress io.Writer) (*sourceSession, func()) {
	if session, ok := ctx.Value(sourceSessionContextKey{}).(*sourceSession); ok {
		if ctx == session.ctx {
			return session, func() {}
		}
		child := *session
		child.ctx, child.parent = ctx, session
		child.objects = map[string]*gitstore.Reader{}
		child.stages = nil
		return &child, child.close
	}
	session := newSourceSession(ctx, timeout, progress)
	return session, session.close
}

// Freeze the commit before the first read. All HEAD specs in this command are
// rewritten to this OID, so another command can fetch without changing our view.
func (s *sourceSession) revision(cache string) (string, error) {
	if s.revisions == nil {
		s.revisions = map[string]string{}
	}
	if revision, found := s.revisions[cache]; found {
		return revision, nil
	}
	revision, err := gitstore.OutputContext(s.ctx, cache, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	s.revisions[cache] = revision
	return revision, nil
}

func sourceTreeSpec(revision, folder string) (string, error) {
	folder = strings.ReplaceAll(folder, "\\", "/")
	clean := path.Clean(folder)
	if path.IsAbs(folder) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(folder, "\x00\r\n") {
		return "", fmt.Errorf("skill path escapes source repository")
	}
	if clean == "." {
		return revision + "^{tree}", nil
	}
	return revision + ":" + clean, nil
}

func (s *sourceSession) sourceIndex(cache string) sourceSkillIndex {
	revision, err := s.revision(cache)
	if err != nil {
		return sourceSkillIndex{err: err}
	}
	if s.indexes == nil {
		s.indexes = map[string]sourceSkillIndex{}
	}
	key := cache + "\x00" + revision
	if index, found := s.indexes[key]; found {
		return index
	}
	index := sourceSkillIndex{paths: map[string][]string{}}
	files, err := collectGitTreeFiles(s, cache, revision+"^{tree}", "")
	if err != nil {
		index.err = err
		return index
	}
	for _, file := range files {
		if path.Base(file.Path) != "SKILL.md" || file.Mode == "120000" {
			continue
		}
		object, err := s.gitObject(cache, file.Object)
		if err != nil {
			index.err = err
			break
		}
		document, err := skilldoc.Parse(object.Data)
		if err == nil && skilldoc.Validate(document) == nil {
			index.paths[document.Name] = append(index.paths[document.Name], path.Dir(file.Path))
		}
	}
	s.indexes[key] = index
	return index
}

// materializeTree creates a disposable stage for execution; it never checks
// out or alters a shared cache and rejects links just like CopyDirectory.
func (s *sourceSession) materializeTree(cache, spec string) (string, error) {
	files, err := collectGitTreeFiles(s, cache, spec, "")
	if err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp("", "skillctl-source-stage-")
	if err != nil {
		return "", err
	}
	s.stages = append(s.stages, directory)
	for _, file := range files {
		if err := s.ctx.Err(); err != nil {
			return "", err
		}
		if file.Mode != "100644" && file.Mode != "100755" {
			return "", fmt.Errorf("source contains unsupported link or mode: %s", file.Path)
		}
		destination := filepath.Join(directory, filepath.FromSlash(file.Path))
		if !fsutil.Within(directory, destination) {
			return "", fmt.Errorf("source path escapes stage")
		}
		object, err := s.gitObject(cache, file.Object)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return "", err
		}
		mode := os.FileMode(0644)
		if file.Mode == "100755" {
			mode = 0755
		}
		if err := os.WriteFile(destination, object.Data, mode); err != nil {
			return "", err
		}
	}
	return directory, nil
}
