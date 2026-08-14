package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type trackedEntry struct {
	Path          string `json:"path"`
	Source        string `json:"source"`
	Ref           string `json:"ref,omitempty"`
	SkillPath     string `json:"skillPath"`
	InstalledHash string `json:"installedHash"`
}

type trackedState struct {
	Version int            `json:"version"`
	Skills  []trackedEntry `json:"skills"`
	path    string
}

func loadTrackedState() (*trackedState, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find user config directory: %w", err)
	}
	path := filepath.Join(dir, "skillctl", "sources.json")
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
	state.path = path
	return state, nil
}

func (s *trackedState) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Path < s.Skills[j].Path })
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
	return os.Rename(tempPath, s.path)
}

func (s *trackedState) find(path string) (*trackedEntry, bool) {
	clean := filepath.Clean(path)
	for i := range s.Skills {
		if samePath(s.Skills[i].Path, clean) {
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

func trackCopiedSkill(ctx context.Context, item skill, source, ref, skillPath string, state *trackedState) error {
	if source == "" {
		return fmt.Errorf("track requires --source")
	}
	source = normalizeSource(source)
	cache, err := syncSource(ctx, source, ref)
	if err != nil {
		return err
	}
	if skillPath == "" {
		skillPath, err = discoverSourceSkill(cache, item.Name)
		if err != nil {
			return err
		}
	}
	remoteSkill, err := sourceSkillPath(cache, skillPath)
	if err != nil {
		return err
	}
	remoteName, valid := readSkill(filepath.Join(remoteSkill, "SKILL.md"), filepath.Base(remoteSkill))
	if !valid || remoteName != item.Name {
		return fmt.Errorf("source path does not contain skill %q", item.Name)
	}
	installedHash, err := hashDirectory(item.Path)
	if err != nil {
		return fmt.Errorf("hash installed skill: %w", err)
	}
	remoteHash, err := hashDirectory(remoteSkill)
	if err != nil {
		return fmt.Errorf("hash source skill: %w", err)
	}
	if installedHash != remoteHash {
		matched, err := matchesSourceHistory(cache, skillPath, installedHash)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("local content does not match the source or its history")
		}
	}
	state.put(trackedEntry{
		Path:          filepath.Clean(item.Path),
		Source:        source,
		Ref:           ref,
		SkillPath:     filepath.ToSlash(filepath.Clean(skillPath)),
		InstalledHash: installedHash,
	})
	return state.save()
}

func matchesSourceHistory(cache, skillPath, installedHash string) (bool, error) {
	latest, err := gitOutput(cache, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("read source HEAD: %w", err)
	}
	defer func() { _, _ = gitOutput(cache, "checkout", "--force", "--detach", latest) }()
	commits, err := gitOutput(cache, "log", "--format=%H", "--", filepath.ToSlash(skillPath))
	if err != nil {
		return false, fmt.Errorf("read source history: %w", err)
	}
	for _, commit := range strings.Fields(commits) {
		if commit == latest {
			continue
		}
		if _, err := gitOutput(cache, "checkout", "--force", "--detach", commit); err != nil {
			return false, fmt.Errorf("inspect source history: %w", err)
		}
		candidate := filepath.Join(cache, filepath.FromSlash(skillPath))
		if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err != nil {
			continue
		}
		hash, err := hashDirectory(candidate)
		if err != nil {
			return false, fmt.Errorf("hash source history: %w", err)
		}
		if hash == installedHash {
			return true, nil
		}
	}
	return false, nil
}

func normalizeSource(source string) string {
	if _, err := os.Stat(source); err == nil {
		if absolute, err := filepath.Abs(source); err == nil {
			return filepath.Clean(absolute)
		}
	}
	return source
}

func processTracked(action string, items []skill, state *trackedState, session *sourceSession, stdout, stderr io.Writer) bool {
	failed := false
	for _, item := range items {
		entry, ok := state.findSkill(item)
		if !ok {
			printSkills(stdout, []skill{item}, "local/untracked (no update source)")
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
		remoteName, valid := readSkill(filepath.Join(remoteSkill, "SKILL.md"), filepath.Base(remoteSkill))
		if !valid || remoteName != item.Name {
			reportFailure(stderr, item, fmt.Sprintf("source path does not contain skill %q", item.Name))
			failed = true
			continue
		}
		localHash, err := hashDirectory(item.Path)
		if err != nil {
			reportFailure(stderr, item, "hash local skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		remoteHash, err := hashDirectory(remoteSkill)
		if err != nil {
			reportFailure(stderr, item, "hash remote skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		if localHash != entry.InstalledHash {
			if remoteHash != entry.InstalledHash {
				printSkills(stdout, []skill{item}, "update available, skipped (local files were modified)")
			} else {
				printSkills(stdout, []skill{item}, "skipped (local files were modified)")
			}
			continue
		}
		if remoteHash == entry.InstalledHash {
			printSkills(stdout, []skill{item}, "up to date")
			continue
		}
		if action == "check" {
			printSkills(stdout, []skill{item}, "update available")
			continue
		}
		replacement, err := beginDirectoryReplacement(item.Path, remoteSkill)
		if err != nil {
			reportFailure(stderr, item, "replace skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		previousHash := entry.InstalledHash
		entry.InstalledHash = remoteHash
		if err := state.save(); err != nil {
			entry.InstalledHash = previousHash
			if rollbackErr := replacement.rollback(); rollbackErr != nil {
				reportFailure(stderr, item, "save source state: "+oneLine(err.Error())+"; rollback skill: "+oneLine(rollbackErr.Error()))
			} else {
				reportFailure(stderr, item, "save source state: "+oneLine(err.Error()))
			}
			failed = true
			continue
		}
		if err := replacement.commit(); err != nil {
			reportFailure(stderr, item, "remove skill backup: "+oneLine(err.Error()))
			failed = true
			continue
		}
		printSkills(stdout, []skill{item}, "updated")
	}
	return failed
}

func syncSource(ctx context.Context, source, ref string) (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	id := sha256.Sum256([]byte(normalizeSource(source) + "\x00" + ref))
	cache := filepath.Join(cacheBase, "skillctl", "sources", hex.EncodeToString(id[:16]))
	if _, err := os.Stat(filepath.Join(cache, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
			return "", fmt.Errorf("create source cache: %w", err)
		}
		cmd := exec.CommandContext(ctx, "git", "clone", "--no-checkout", source, cache)
		cmd.WaitDelay = time.Second
		if output, err := cmd.CombinedOutput(); err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("git clone: network timeout: %w", ctx.Err())
			}
			return "", fmt.Errorf("git clone: %s", strings.TrimSpace(string(output)))
		}
	}
	if _, err := gitNetworkOutput(ctx, cache, "fetch", "--prune", "--recurse-submodules=no", "origin"); err != nil {
		return "", fmt.Errorf("git fetch: %w", err)
	}
	revision, err := resolveSourceRevision(cache, ref)
	if err != nil {
		return "", err
	}
	if _, err := gitOutput(cache, "checkout", "--force", "--detach", revision); err != nil {
		return "", fmt.Errorf("checkout source ref: %w", err)
	}
	return cache, nil
}

func resolveSourceRevision(cache, ref string) (string, error) {
	if ref == "" {
		return "refs/remotes/origin/HEAD", nil
	}
	if strings.HasPrefix(ref, "refs/heads/") {
		ref = strings.TrimPrefix(ref, "refs/heads/")
	}
	candidates := []string{
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
	}
	if strings.HasPrefix(ref, "refs/") {
		candidates = []string{ref}
	} else if isCommitRef(ref) {
		candidates = append(candidates, ref)
	}
	for _, candidate := range candidates {
		if _, err := gitOutput(cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("source ref %q was not found as a remote branch, tag, or commit", ref)
}

func isCommitRef(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, char := range ref {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func sourceSkillPath(cache, skillPath string) (string, error) {
	remoteSkill := filepath.Join(cache, filepath.FromSlash(skillPath))
	if !within(cache, remoteSkill) {
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
	if !within(realCache, realSkill) {
		return "", fmt.Errorf("source skill path resolves outside the repository")
	}
	remoteSkill = realSkill
	if _, err := os.Stat(filepath.Join(remoteSkill, "SKILL.md")); err != nil {
		return "", fmt.Errorf("source skill path: %w", err)
	}
	return remoteSkill, nil
}

func discoverSourceSkill(cache, name string) (string, error) {
	var matches []string
	err := filepath.WalkDir(cache, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		dir := filepath.Dir(path)
		found, valid := readSkill(path, filepath.Base(dir))
		if valid && found == name {
			rel, err := filepath.Rel(cache, dir)
			if err != nil {
				return err
			}
			matches = append(matches, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan source: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("source does not contain skill %q", name)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("source contains multiple skills named %q; use --skill-path", name)
	}
	return matches[0], nil
}

func hashDirectory(root string) (string, error) {
	hash := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
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
		if !strings.ContainsRune(string(content), '\x00') {
			content = []byte(strings.ReplaceAll(string(content), "\r\n", "\n"))
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(content))
		if _, err := hash.Write(content); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
	if err := copyDirectory(source, stage); err != nil {
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

func copyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
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

func samePath(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
