package app

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
)

type trackedEntry struct {
	Path          string `json:"path"`
	Source        string `json:"source"`
	Ref           string `json:"ref,omitempty"`
	SkillPath     string `json:"skillPath"`
	InstalledHash string `json:"installedHash"`
}

type providerBaseline struct {
	Path          string `json:"path"`
	Provider      string `json:"provider"`
	Revision      string `json:"revision"`
	InstalledHash string `json:"installedHash"`
}

type trackedState struct {
	Version           int                `json:"version"`
	Skills            []trackedEntry     `json:"skills"`
	ProviderBaselines []providerBaseline `json:"providerBaselines,omitempty"`
	path              string
	readOnly          bool
}

func loadTrackedState() (*trackedState, error) {
	dir, err := skillctlDirectory()
	if err != nil {
		return nil, fmt.Errorf("find user config directory: %w", err)
	}
	path := filepath.Join(dir, "sources.json")
	state := &trackedState{Version: 1, path: path}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source state: %w", err)
	}
	if err := json.Unmarshal(content, state); err != nil {
		return nil, fmt.Errorf("invalid source state: %w", err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported source state version: %d", state.Version)
	}
	for _, entry := range state.Skills {
		if err := validateSourceURL(entry.Source); err != nil {
			return nil, err
		}
	}
	state.path = path
	return state, nil
}

func (s *trackedState) save() error {
	if s.readOnly {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	slices.SortFunc(s.Skills, func(a, b trackedEntry) int { return cmp.Compare(a.Path, b.Path) })
	slices.SortFunc(s.ProviderBaselines, func(a, b providerBaseline) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Path, b.Path))
	})
	content, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temp, err := os.CreateTemp(filepath.Dir(s.path), "sources-*.json")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return fsutil.ReplaceFile(tempPath, s.path)
}

func (s *trackedState) find(path string) (*trackedEntry, bool) {
	if s == nil {
		return nil, false
	}
	clean := filepath.Clean(path)
	for i := range s.Skills {
		if fsutil.SamePath(s.Skills[i].Path, clean) {
			return &s.Skills[i], true
		}
	}
	return nil, false
}

func (s *trackedState) findSkill(item skill) (*trackedEntry, bool) {
	if entry, ok := s.find(item.Path); ok {
		return entry, true
	}
	for _, alias := range item.Aliases {
		if entry, ok := s.find(alias); ok {
			return entry, true
		}
	}
	return nil, false
}

func (s *trackedState) put(entry trackedEntry) {
	if existing, ok := s.find(entry.Path); ok {
		*existing = entry
		return
	}
	s.Skills = append(s.Skills, entry)
}

func (s *trackedState) findProviderBaseline(path, provider string) (*providerBaseline, bool) {
	for i := range s.ProviderBaselines {
		entry := &s.ProviderBaselines[i]
		if entry.Provider == provider && fsutil.SamePath(entry.Path, path) {
			return entry, true
		}
	}
	return nil, false
}

func (s *trackedState) putProviderBaseline(entry providerBaseline) bool {
	if existing, ok := s.findProviderBaseline(entry.Path, entry.Provider); ok {
		if *existing == entry {
			return false
		}
		*existing = entry
		return true
	}
	s.ProviderBaselines = append(s.ProviderBaselines, entry)
	return true
}

func trackCopiedSkill(ctx context.Context, item skill, source, ref, skillPath string, state *trackedState) error {
	entry, err := verifyCopiedSkill(ctx, item, source, ref, skillPath)
	if err != nil {
		return err
	}
	state.put(entry)
	return state.save()
}

func verifyCopiedSkill(ctx context.Context, item skill, source, ref, skillPath string) (trackedEntry, error) {
	if err := validateSourceURL(source); err != nil {
		return trackedEntry{}, err
	}
	if source == "" {
		return trackedEntry{}, fmt.Errorf("track requires --source")
	}
	source = gitstore.NormalizeSource(source)
	cache, err := gitstore.SyncWorktree(ctx, source, ref)
	if err != nil {
		return trackedEntry{}, err
	}
	return verifyCopiedSkillInCache(item, source, ref, skillPath, cache)
}

func verifyCopiedSkillInCache(item skill, source, ref, skillPath, cache string) (trackedEntry, error) {
	if skillPath == "" {
		var err error
		skillPath, err = discoverSourceSkill(cache, item.Name)
		if err != nil {
			return trackedEntry{}, err
		}
	}
	remoteSkill, err := sourceSkillPath(cache, skillPath)
	if err != nil {
		return trackedEntry{}, err
	}
	remoteName, readErr := skilldoc.ReadName(filepath.Join(remoteSkill, "SKILL.md"))
	if readErr != nil || remoteName != item.Name {
		return trackedEntry{}, fmt.Errorf("source path does not contain skill %q", item.Name)
	}
	installedHash, err := fsutil.HashDirectory(item.Path)
	if err != nil {
		return trackedEntry{}, fmt.Errorf("hash installed skill: %w", err)
	}
	remoteHash, err := fsutil.HashDirectory(remoteSkill)
	if err != nil {
		return trackedEntry{}, fmt.Errorf("hash source skill: %w", err)
	}
	if installedHash != remoteHash {
		matched, err := matchesSourceHistory(cache, skillPath, installedHash)
		if err != nil {
			return trackedEntry{}, err
		}
		if !matched {
			return trackedEntry{}, fmt.Errorf("local content does not match the source or its history")
		}
	}
	return trackedEntry{
		Path:          filepath.Clean(item.Path),
		Source:        source,
		Ref:           ref,
		SkillPath:     filepath.ToSlash(filepath.Clean(skillPath)),
		InstalledHash: installedHash,
	}, nil
}

func matchesSourceHistory(cache, skillPath, installedHash string) (bool, error) {
	latest, err := gitstore.Output(cache, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("read source HEAD: %w", err)
	}
	defer func() { _, _ = gitstore.Output(cache, "checkout", "--force", "--detach", latest) }()
	commits, err := gitstore.Output(cache, "log", "--format=%H", "--", filepath.ToSlash(skillPath))
	if err != nil {
		return false, fmt.Errorf("read source history: %w", err)
	}
	for _, commit := range strings.Fields(commits) {
		if commit == latest {
			continue
		}
		if _, err := gitstore.Output(cache, "checkout", "--force", "--detach", commit); err != nil {
			return false, fmt.Errorf("inspect source history: %w", err)
		}
		candidate := filepath.Join(cache, filepath.FromSlash(skillPath))
		if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err != nil {
			continue
		}
		hash, err := fsutil.HashDirectory(candidate)
		if err != nil {
			return false, fmt.Errorf("hash source history: %w", err)
		}
		if hash == installedHash {
			return true, nil
		}
	}
	return false, nil
}

func processTracked(action string, items []skill, state *trackedState, session *sourceSession, stdout, stderr io.Writer) bool {
	failed := false
	for _, item := range items {
		entry, ok := state.findSkill(item)
		if !ok {
			printSkills(stdout, []skill{item}, "local/untracked (no update source)", "untracked", "missing_update_source", false)
			continue
		}
		cache, err := session.source(entry.Source, entry.Ref)
		if err != nil {
			reportFailure(stderr, item, oneLine(err.Error()))
			failed = true
			continue
		}
		remoteSkill, err := sourceSkillPath(cache, entry.SkillPath)
		if err != nil {
			reportFailure(stderr, item, oneLine(err.Error()))
			failed = true
			continue
		}
		remoteName, readErr := skilldoc.ReadName(filepath.Join(remoteSkill, "SKILL.md"))
		if readErr != nil || remoteName != item.Name {
			reportFailure(stderr, item, fmt.Sprintf("source path does not contain skill %q", item.Name))
			failed = true
			continue
		}
		localHash, err := fsutil.HashDirectory(item.Path)
		if err != nil {
			reportFailure(stderr, item, "hash local skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		remoteHash, err := fsutil.HashDirectory(remoteSkill)
		if err != nil {
			reportFailure(stderr, item, "hash remote skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		if localHash != entry.InstalledHash {
			if remoteHash != entry.InstalledHash {
				printSkills(stdout, []skill{item}, "update available, skipped (local files were modified)", "modified", "local_changes", true)
			} else {
				printSkills(stdout, []skill{item}, "skipped (local files were modified)", "modified", "local_changes", false)
			}
			continue
		}
		if remoteHash == entry.InstalledHash {
			printSkills(stdout, []skill{item}, "up to date", "current", "", false)
			continue
		}
		if action == "check" {
			printSkills(stdout, []skill{item}, "update available", "outdated", "upstream_changed", true)
			continue
		}

		if err := trackedUpdateTransaction(item, entry, state, remoteSkill, remoteHash); err != nil {
			reportFailure(stderr, item, "save source state/update content: "+oneLine(err.Error()))
			failed = true
			continue
		}

		printSkills(stdout, []skill{item}, "updated", "current", "", false)
	}
	return failed
}

func sourceSkillPath(cache, skillPath string) (string, error) {
	remoteSkill := filepath.Join(cache, filepath.FromSlash(skillPath))
	if !fsutil.Within(cache, remoteSkill) {
		return "", fmt.Errorf("skill path escapes the source repository")
	}
	realCache, err := filepath.EvalSymlinks(cache)
	if err != nil {
		return "", fmt.Errorf("resolve source cache: %w", err)
	}
	realSkill, err := filepath.EvalSymlinks(remoteSkill)
	if err != nil {
		return "", fmt.Errorf("resolve source skill path: %w", err)
	}
	if !fsutil.Within(realCache, realSkill) {
		return "", fmt.Errorf("source skill path resolves outside the repository")
	}
	remoteSkill = realSkill
	if _, err := os.Stat(filepath.Join(remoteSkill, "SKILL.md")); err != nil {
		return "", fmt.Errorf("source skill path: %w", err)
	}
	return remoteSkill, nil
}

func discoverSourceSkill(cache, name string) (string, error) {
	return scanSourceSkills(cache).find(name)
}

type sourceSkillIndex struct {
	paths map[string][]string
	err   error
}

func scanSourceSkills(cache string) sourceSkillIndex {
	index := sourceSkillIndex{paths: map[string][]string{}}
	err := filepath.WalkDir(cache, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(cache, path)
		if relErr != nil {
			return relErr
		}
		if rel != "." && fsutil.IgnoreContent(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		dir := filepath.Dir(path)
		found, readErr := skilldoc.ReadName(path)
		if readErr == nil {
			rel, err := filepath.Rel(cache, dir)
			if err != nil {
				return err
			}
			index.paths[found] = append(index.paths[found], filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		index.err = fmt.Errorf("scan source: %w", err)
	}
	return index
}

func (index sourceSkillIndex) find(name string) (string, error) {
	if index.err != nil {
		return "", index.err
	}
	matches := index.paths[name]
	if len(matches) == 0 {
		return "", fmt.Errorf("source does not contain skill %q", name)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("source contains multiple skills named %q; use --skill-path", name)
	}
	return matches[0], nil
}

type pendingDirectoryReplacement struct {
	target string
	backup string
}

func beginDirectoryReplacement(target, source string) (*pendingDirectoryReplacement, error) {
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".skillctl-stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := fsutil.CopyDirectory(source, stage); err != nil {
		return nil, err
	}
	backup, err := os.MkdirTemp(parent, ".skillctl-backup-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(backup); err != nil {
		return nil, err
	}
	if err := os.Rename(target, backup); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, target); err != nil {
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return nil, fmt.Errorf("%w; restore backup: %v", err, restoreErr)
		}
		return nil, err
	}
	return &pendingDirectoryReplacement{target: target, backup: backup}, nil
}

func (r *pendingDirectoryReplacement) commit() error {
	return os.RemoveAll(r.backup)
}

func (r *pendingDirectoryReplacement) rollback() error {
	if err := os.RemoveAll(r.target); err != nil {
		return err
	}
	return os.Rename(r.backup, r.target)
}

func trackedStateBytes(state *trackedState) ([]byte, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	return append(data, '\n'), err
}
