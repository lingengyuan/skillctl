package main

import (
	"bytes"
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

type vercelLock struct {
	Version int                        `json:"version"`
	Skills  map[string]vercelLockEntry `json:"skills"`
}

type vercelLockEntry struct {
	Source          string `json:"source"`
	SourceType      string `json:"sourceType"`
	SourceURL       string `json:"sourceUrl"`
	Ref             string `json:"ref"`
	SkillPath       string `json:"skillPath"`
	SkillFolderHash string `json:"skillFolderHash"`
}

func vercelStatus(action string, available bool, drift string) string {
	if action == "update" && drift == "modified" {
		if available {
			return "update available, skipped (local files were modified)"
		}
		return "up to date, skipped (local files were modified)"
	}
	if available {
		if drift == "modified" {
			return "update available, local files were modified"
		}
		if action == "update" {
			return "update available, skipped (provider updater not available)"
		}
		return "update available"
	}
	if drift == "modified" {
		return "up to date, local files were modified"
	}
	return "up to date"
}

type vercelUpdateRequest struct {
	Name         string
	ManifestPath string
}

var runVercelUpdater = executeVercelUpdater

func updateVercelProvider(ctx context.Context, session *sourceSession, item skill, claim vercelClaim, progress io.Writer) (vercelLockEntry, error) {
	snapshot, err := createVercelUpdateSnapshot(item.Path, claim.ManifestPath)
	if err != nil {
		return vercelLockEntry{}, fmt.Errorf("create update backup: %w", err)
	}
	defer snapshot.cleanup()

	started := time.Now()
	fmt.Fprintf(progress, "Updating %s with Vercel Skills...\n", claim.Name)
	_, err = runVercelUpdater(ctx, vercelUpdateRequest{Name: claim.Name, ManifestPath: claim.ManifestPath}, progress)
	if err != nil {
		fmt.Fprintf(progress, "Vercel Skills update failed (%s).\n", time.Since(started).Round(time.Millisecond))
		return vercelLockEntry{}, snapshot.fail(item.Path, claim.ManifestPath, err)
	}

	updated, err := readVercelLockEntry(claim.ManifestPath, claim.Name)
	if err == nil && !sameVercelSource(claim.Entry, updated) {
		err = fmt.Errorf("provider changed the skill source")
	}
	if err == nil && updated.SkillFolderHash == claim.Entry.SkillFolderHash {
		err = fmt.Errorf("provider did not advance the lock revision")
	}
	if err == nil {
		name, readErr := readSkill(filepath.Join(item.Path, "SKILL.md"))
		if readErr != nil || name != item.Name {
			err = fmt.Errorf("updated directory does not contain skill %q", item.Name)
		}
	}
	if err == nil {
		var available bool
		var drift string
		available, drift, err = checkVercelEntry(session, updated, item.Path)
		if err == nil && (available || drift != "clean") {
			err = fmt.Errorf("post-update verification failed: update_available=%t drift=%s", available, drift)
		}
	}
	if err != nil {
		fmt.Fprintf(progress, "Vercel Skills verification failed (%s).\n", time.Since(started).Round(time.Millisecond))
		return vercelLockEntry{}, snapshot.fail(item.Path, claim.ManifestPath, err)
	}
	fmt.Fprintf(progress, "Vercel Skills update verified (%s).\n", time.Since(started).Round(time.Millisecond))
	return updated, nil
}

func executeVercelUpdater(ctx context.Context, request vercelUpdateRequest, progress io.Writer) (string, error) {
	activeLock, err := activeVercelLockPath()
	if err != nil {
		return "", err
	}
	if !samePath(activeLock, request.ManifestPath) {
		return "", fmt.Errorf("configured manifest is not the active Vercel global lock: %s", request.ManifestPath)
	}

	command, err := exec.LookPath("skills")
	args := []string{"update", request.Name, "--global", "--yes"}
	if err != nil {
		command, err = exec.LookPath("npx")
		if err != nil {
			return "", fmt.Errorf("Vercel Skills CLI was not found; install Node.js or skills")
		}
		args = append([]string{"--yes", "skills"}, args...)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, progress)
	cmd.Stderr = io.MultiWriter(&output, progress)
	err = cmd.Run()
	if ctx.Err() != nil {
		return output.String(), fmt.Errorf("provider update timeout: %w", ctx.Err())
	}
	message := oneLine(output.String())
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return output.String(), fmt.Errorf("Vercel Skills CLI: %s", message)
	}
	if strings.Contains(strings.ToLower(message), "failed to update") {
		return output.String(), fmt.Errorf("Vercel Skills CLI reported an update failure")
	}
	return output.String(), nil
}

func activeVercelLockPath() (string, error) {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "skills", ".skill-lock.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".agents", ".skill-lock.json"), nil
}

func readVercelLockEntry(path, name string) (vercelLockEntry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return vercelLockEntry{}, fmt.Errorf("read provider lock: %w", err)
	}
	var lock vercelLock
	if err := json.Unmarshal(content, &lock); err != nil {
		return vercelLockEntry{}, fmt.Errorf("read provider lock: invalid JSON")
	}
	if lock.Version != 3 {
		return vercelLockEntry{}, fmt.Errorf("read provider lock: unsupported schema version %d", lock.Version)
	}
	entry, ok := lock.Skills[name]
	if !ok {
		return vercelLockEntry{}, fmt.Errorf("provider removed lock entry %q", name)
	}
	return entry, nil
}

func sameVercelSource(left, right vercelLockEntry) bool {
	return left.Source == right.Source &&
		left.SourceType == right.SourceType &&
		left.SourceURL == right.SourceURL &&
		left.Ref == right.Ref &&
		left.SkillPath == right.SkillPath
}

type vercelUpdateSnapshot struct {
	directory string
	lock      []byte
	lockMode  os.FileMode
}

func createVercelUpdateSnapshot(installed, manifestPath string) (*vercelUpdateSnapshot, error) {
	lock, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(filepath.Dir(installed), ".skillctl-provider-snapshot-")
	if err != nil {
		return nil, err
	}
	if err := copyDirectory(installed, directory); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &vercelUpdateSnapshot{directory: directory, lock: lock, lockMode: info.Mode().Perm()}, nil
}

func (s *vercelUpdateSnapshot) cleanup() {
	_ = os.RemoveAll(s.directory)
}

func (s *vercelUpdateSnapshot) fail(installed, manifestPath string, cause error) error {
	if err := s.restore(installed, manifestPath); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, err)
	}
	return cause
}

func (s *vercelUpdateSnapshot) restore(installed, manifestPath string) error {
	if _, err := os.Lstat(installed); os.IsNotExist(err) {
		stage, err := os.MkdirTemp(filepath.Dir(installed), ".skillctl-restore-")
		if err != nil {
			return fmt.Errorf("restore skill: %w", err)
		}
		defer os.RemoveAll(stage)
		if err := copyDirectory(s.directory, stage); err != nil {
			return fmt.Errorf("restore skill: %w", err)
		}
		if err := os.Rename(stage, installed); err != nil {
			return fmt.Errorf("restore skill: %w", err)
		}
		if err := writeFileAtomically(manifestPath, s.lock, s.lockMode); err != nil {
			if removeErr := os.RemoveAll(installed); removeErr != nil {
				return fmt.Errorf("restore lock: %w; restore provider result: %v", err, removeErr)
			}
			return fmt.Errorf("restore lock: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("restore skill: %w", err)
	}

	replacement, err := beginDirectoryReplacement(installed, s.directory)
	if err != nil {
		return fmt.Errorf("restore skill: %w", err)
	}
	if err := writeFileAtomically(manifestPath, s.lock, s.lockMode); err != nil {
		if rollbackErr := replacement.rollback(); rollbackErr != nil {
			return fmt.Errorf("restore lock: %w; restore provider result: %v", err, rollbackErr)
		}
		return fmt.Errorf("restore lock: %w", err)
	}
	if err := replacement.commit(); err != nil {
		return fmt.Errorf("remove provider result backup: %w", err)
	}
	return nil
}

func writeFileAtomically(path string, content []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".skillctl-lock-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// sourceSession is command-scoped so provider and explicit-track checks sync
// each normalized source/ref pair once and share one Git object process per
// cached repository.
func checkVercelEntry(session *sourceSession, entry vercelLockEntry, installed string) (bool, string, error) {
	if entry.SourceURL == "" || entry.SkillPath == "" || entry.SkillFolderHash == "" {
		return false, "unknown", fmt.Errorf("incomplete v3 lock entry")
	}
	if filepath.IsAbs(entry.SkillPath) || filepath.Base(entry.SkillPath) != "SKILL.md" || strings.HasPrefix(filepath.Clean(entry.SkillPath), "..") {
		return false, "unknown", fmt.Errorf("invalid skillPath")
	}
	cache, err := session.source(entry.SourceURL, entry.Ref)
	if err != nil {
		return false, "unknown", err
	}
	folder := filepath.ToSlash(filepath.Dir(entry.SkillPath))
	if entry.SourceType == "git" {
		remote, err := sourceSkillPath(cache, folder)
		if err != nil {
			return false, "unknown", err
		}
		current, err := hashDirectory(remote)
		if err != nil {
			return false, "unknown", err
		}
		local, err := hashDirectory(installed)
		if err != nil {
			return false, "unknown", err
		}
		drift := "clean"
		if local != entry.SkillFolderHash {
			drift = "modified"
		}
		return current != entry.SkillFolderHash, drift, nil
	}
	current, err := session.gitObject(cache, "HEAD:"+folder)
	if err != nil {
		return false, "unknown", fmt.Errorf("resolve upstream skill folder: %w", err)
	}
	if current.Type != "tree" {
		return false, "unknown", fmt.Errorf("upstream skill path is not a directory")
	}
	drift, err := vercelLocalDrift(session, cache, entry.SkillFolderHash, installed)
	if err != nil {
		return false, "unknown", err
	}
	return !strings.EqualFold(current.Hash, entry.SkillFolderHash), drift, nil
}

func vercelLocalDrift(session *sourceSession, cache, expectedTree, installed string) (string, error) {
	expected, err := hashGitTree(session, cache, expectedTree)
	if err != nil {
		return "unknown", err
	}
	actual, err := hashDirectory(installed)
	if err != nil {
		return "unknown", err
	}
	if expected == actual {
		return "clean", nil
	}
	return "modified", nil
}

func hashGitTree(session *sourceSession, cache, tree string) (string, error) {
	if session.treeHashes == nil {
		session.treeHashes = map[string]string{}
	}
	key := cache + "\x00" + tree
	if value, ok := session.treeHashes[key]; ok {
		return value, nil
	}
	files, err := collectGitTreeFiles(session, cache, tree, "")
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	hash := sha256.New()
	for _, file := range files {
		object, err := session.gitObject(cache, file.Object)
		if err != nil {
			return "", err
		}
		data := object.Data
		if file.Mode == "120000" {
			data = append([]byte("symlink\x00"), object.Data...)
		} else {
			data = bytes.ReplaceAll(object.Data, []byte("\r\n"), []byte("\n"))
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", file.Path, len(data))
		if _, err := hash.Write(data); err != nil {
			return "", err
		}
	}
	value := hex.EncodeToString(hash.Sum(nil))
	session.treeHashes[key] = value
	return value, nil
}

type gitTreeFile struct {
	Path   string
	Mode   string
	Object string
}

func collectGitTreeFiles(session *sourceSession, cache, tree, prefix string) ([]gitTreeFile, error) {
	object, err := session.gitObject(cache, tree)
	if err != nil {
		return nil, err
	}
	if object.Type != "tree" {
		return nil, fmt.Errorf("git object %q is not a tree", tree)
	}
	entries, err := parseGitTree(object)
	if err != nil {
		return nil, err
	}
	var files []gitTreeFile
	for _, entry := range entries {
		path := entry.Path
		if prefix != "" {
			path = prefix + "/" + path
		}
		if entry.Mode == "40000" || entry.Mode == "040000" {
			children, err := collectGitTreeFiles(session, cache, entry.Object, path)
			if err != nil {
				return nil, err
			}
			files = append(files, children...)
			continue
		}
		files = append(files, gitTreeFile{Path: path, Mode: entry.Mode, Object: entry.Object})
	}
	return files, nil
}

func parseGitTree(object gitObject) ([]gitTreeFile, error) {
	objectBytes := len(object.Hash) / 2
	if objectBytes == 0 {
		return nil, fmt.Errorf("invalid git tree hash")
	}
	data := object.Data
	var entries []gitTreeFile
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space <= 0 {
			return nil, fmt.Errorf("invalid git tree mode")
		}
		mode := string(data[:space])
		data = data[space+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 0 {
			return nil, fmt.Errorf("invalid git tree path")
		}
		path := string(data[:nul])
		data = data[nul+1:]
		if len(data) < objectBytes {
			return nil, fmt.Errorf("invalid git tree object id")
		}
		objectID := hex.EncodeToString(data[:objectBytes])
		data = data[objectBytes:]
		entries = append(entries, gitTreeFile{Path: path, Mode: mode, Object: objectID})
	}
	return entries, nil
}

type vercelLocks []vercelManifest
type vercelManifest struct {
	path, installRoot string
	lock              vercelLock
}

type vercelClaim struct {
	Name         string
	ManifestPath string
	Entry        vercelLockEntry
}

func loadVercelLocks(manifests []manifest) (vercelLocks, map[string]string) {
	var result vercelLocks
	errors := map[string]string{}
	for _, m := range manifests {
		if m.Kind != "vercel-skills-lock-v3" {
			continue
		}
		content, err := os.ReadFile(m.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errors[m.Path] = oneLine(err.Error())
			continue
		}
		var lock vercelLock
		if err := json.Unmarshal(content, &lock); err != nil {
			errors[m.Path] = "invalid JSON"
			continue
		}
		if lock.Version != 3 {
			errors[m.Path] = fmt.Sprintf("unsupported schema version: %d", lock.Version)
			continue
		}
		result = append(result, vercelManifest{m.Path, m.InstallRoot, lock})
	}
	return result, errors
}

func manifestInstallRoot(manifests []manifest, path string) string {
	for _, m := range manifests {
		if m.Path == path {
			return m.InstallRoot
		}
	}
	return ""
}

func (locks vercelLocks) claim(item skill) (vercelClaim, []string, bool) {
	for _, lock := range locks {
		for key, entry := range lock.lock.Skills {
			install := filepath.Join(lock.installRoot, key)
			matches := samePath(item.Path, install)
			for _, alias := range item.Aliases {
				matches = matches || samePath(alias, install)
			}
			if matches {
				return vercelClaim{Name: key, ManifestPath: lock.path, Entry: entry}, []string{lock.path}, true
			}
		}
	}
	return vercelClaim{}, nil, false
}
