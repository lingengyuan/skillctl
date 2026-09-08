package app

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
)

type trackedEntry struct {
	Path             string    `json:"path"`
	Source           string    `json:"source"`
	Ref              string    `json:"ref,omitempty"`
	SkillPath        string    `json:"skillPath"`
	InstalledHash    string    `json:"installedHash"`
	VerifiedRevision string    `json:"verifiedRevision,omitempty"`
	EvidenceID       string    `json:"evidenceId,omitempty"`
	EvidenceWhen     time.Time `json:"evidenceWhen,omitzero"`
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
	session, cleanup := commandSourceSession(ctx, defaultNetworkTimeout, io.Discard)
	defer cleanup()
	source = gitstore.NormalizeSource(source)
	cache, err := session.source(source, ref)
	if err != nil {
		return trackedEntry{}, err
	}
	return verifyCopiedSkillInSession(ctx, session, item, source, ref, skillPath, cache)
}

func verifyCopiedSkillInSession(ctx context.Context, session *sourceSession, item skill, source, ref, skillPath, cache string) (trackedEntry, error) {
	if skillPath == "" {
		var err error
		skillPath, err = session.sourceIndex(cache).find(item.Name)
		if err != nil {
			return trackedEntry{}, err
		}
	}
	revision, err := session.revision(cache)
	if err != nil {
		return trackedEntry{}, err
	}
	tree, err := sourceTreeSpec(revision, skillPath)
	if err != nil {
		return trackedEntry{}, err
	}
	document, err := session.gitObject(cache, revision+":"+path.Join(skillPath, "SKILL.md"))
	if err != nil {
		return trackedEntry{}, err
	}
	parsed, err := skilldoc.Parse(document.Data)
	if err != nil || skilldoc.Validate(parsed) != nil || parsed.Name != item.Name {
		return trackedEntry{}, fmt.Errorf("source path does not contain skill %q", item.Name)
	}
	installedHash, err := fsutil.HashDirectoryContext(ctx, item.Path)
	if err != nil {
		return trackedEntry{}, fmt.Errorf("hash installed skill: %w", err)
	}
	remoteHash, err := hashGitTree(session, cache, tree)
	if err != nil {
		return trackedEntry{}, fmt.Errorf("hash source skill: %w", err)
	}
	if installedHash != remoteHash {
		revision, err = matchingSourceRevision(ctx, session, cache, revision, skillPath, installedHash)
		if err != nil {
			return trackedEntry{}, err
		}
		if revision == "" {
			return trackedEntry{}, fmt.Errorf("local content does not match the source or its history")
		}
	}
	return trackedEntry{Path: filepath.Clean(item.Path), Source: source, Ref: ref, SkillPath: filepath.ToSlash(filepath.Clean(skillPath)), InstalledHash: installedHash, VerifiedRevision: revision}, nil
}

func matchesSourceHistory(cache, skillPath, installedHash string) (bool, error) {
	session := newSourceSession(context.Background(), defaultNetworkTimeout, io.Discard)
	defer session.close()
	revision, err := session.revision(cache)
	if err != nil {
		return false, err
	}
	matched, err := matchingSourceRevision(session.ctx, session, cache, revision, skillPath, installedHash)
	return matched != "", err
}

func matchingSourceRevision(ctx context.Context, session *sourceSession, cache, latest, skillPath, installedHash string) (string, error) {
	commits, err := gitstore.OutputContext(ctx, cache, "log", "--format=%H", latest, "--", filepath.ToSlash(skillPath))
	if err != nil {
		return "", fmt.Errorf("read source history: %w", err)
	}
	for _, commit := range strings.Fields(commits) {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("history verification incomplete: %w", err)
		}
		spec, err := sourceTreeSpec(commit, skillPath)
		if err != nil {
			return "", err
		}
		// A directory may be absent at a deletion commit. The remaining history
		// is still meaningful; no shared checkout or restoration is necessary.
		if _, err := session.gitObject(cache, spec); err != nil {
			continue
		}
		hash, err := hashGitTree(session, cache, spec)
		if err != nil {
			return "", fmt.Errorf("hash source history: %w", err)
		}
		if hash == installedHash {
			return commit, nil
		}
	}
	return "", nil
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
		tree, err := sourceTreeSpec("HEAD", entry.SkillPath)
		if err != nil {
			reportFailure(stderr, item, err.Error())
			failed = true
			continue
		}
		document, err := session.gitObject(cache, "HEAD:"+path.Join(entry.SkillPath, "SKILL.md"))
		if err != nil {
			reportFailure(stderr, item, err.Error())
			failed = true
			continue
		}
		parsed, err := skilldoc.Parse(document.Data)
		if err != nil || skilldoc.Validate(parsed) != nil || parsed.Name != item.Name {
			reportFailure(stderr, item, fmt.Sprintf("source path does not contain skill %q", item.Name))
			failed = true
			continue
		}
		var localHash string
		if action == "check" && session.observation != nil {
			localHash, err = session.observation.hash(item.Path)
		} else {
			localHash, err = fsutil.HashDirectoryContext(session.ctx, item.Path)
		}
		if err != nil {
			reportFailure(stderr, item, "hash local skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		remoteHash, err := hashGitTree(session, cache, tree)
		if err != nil {
			reportFailure(stderr, item, "hash remote skill: "+oneLine(err.Error()))
			failed = true
			continue
		}
		if localHash != entry.InstalledHash {
			if remoteHash != entry.InstalledHash {
				printSkills(stdout, []skill{item}, vercelStatus(action, true, "modified"), "modified", "local_changes", true)
			} else {
				printSkills(stdout, []skill{item}, vercelStatus(action, false, "modified"), "modified", "local_changes", false)
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

		remoteSkill, err := session.materializeTree(cache, tree)
		if err != nil {
			reportFailure(stderr, item, err.Error())
			failed = true
			continue
		}
		if err := trackedUpdateTransaction(item, entry, state, remoteSkill, remoteHash, session); err != nil {
			reportFailure(stderr, item, "save source state/update content: "+oneLine(err.Error()))
			failed = true
			continue
		}

		printSkills(stdout, []skill{item}, "updated", "current", "", false)
	}
	return failed
}

type sourceSkillIndex struct {
	paths map[string][]string
	err   error
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
