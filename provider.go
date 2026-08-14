package main

import (
	"bytes"
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
)

// report is deliberately a value object: rendering does not need to know how
// an adapter discovered ownership.
type report struct {
	Identity        string   `json:"identity"`
	Path            string   `json:"path"`
	Aliases         []string `json:"aliases,omitempty"`
	ScanRoot        string   `json:"scanRoot"`
	Host            string   `json:"host"`
	Scope           string   `json:"scope"`
	Provider        string   `json:"provider"`
	Owner           string   `json:"owner"`
	Evidence        []string `json:"evidence"`
	Revision        string   `json:"revision,omitempty"`
	Drift           string   `json:"drift"`
	Status          string   `json:"status"`
	UpdateAvailable bool     `json:"updateAvailable"`
	Executor        string   `json:"executor"`
	Error           string   `json:"error,omitempty"`
}

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

func inspect(action string, skills []skill, state *trackedState, manifests []manifest, managed []managedRoot, stdout, stderr io.Writer) ([]report, bool) {
	locks, lockErrors := loadVercelLocks(manifests)
	session := sourceSession{caches: map[string]string{}}
	reports := make([]report, 0, len(skills))
	remaining := make([]skill, 0, len(skills))
	failed := false
	var errorPaths []string
	for path := range lockErrors {
		errorPaths = append(errorPaths, path)
	}
	sort.Strings(errorPaths)
	for _, item := range skills {
		if item.Broken {
			reports = append(reports, reportFor(item, "filesystem", "unknown", nil, "broken", "broken link -> "+item.LinkTarget, false, "report-only", ""))
			continue
		}
		manifestBad := false
		for _, path := range errorPaths {
			message := lockErrors[path]
			matches := within(manifestInstallRoot(manifests, path), item.Path)
			for _, alias := range item.Aliases {
				matches = matches || within(manifestInstallRoot(manifests, path), alias)
			}
			if matches {
				reports = append(reports, reportFor(item, "vercel-skills-lock", "unknown", []string{path}, "unknown", "provider manifest unsupported", false, "report-only", message))
				failed = true
				manifestBad = true
				break
			}
		}
		if manifestBad {
			continue
		}
		entry, evidence, found := locks.claim(item)
		managedOwner, managedEvidence := managedOwner(item, managed)
		tracked, isTracked := state.findSkill(item)
		claims := 0
		if managedOwner != "" {
			claims++
		}
		if found {
			claims++
		}
		if isTracked {
			claims++
		}
		if claims > 1 {
			allEvidence := append([]string{}, managedEvidence...)
			allEvidence = append(allEvidence, evidence...)
			if isTracked {
				allEvidence = append(allEvidence, tracked.Source)
			}
			reports = append(reports, reportFor(item, "ambiguous", "unknown", allEvidence, "unknown", "ambiguous provenance", false, "report-only", "authoritative claims conflict"))
			failed = true
			continue
		}
		if managedOwner != "" {
			reports = append(reports, reportFor(item, "codex-host", "host", managedEvidence, "none", "managed by "+managedOwner, false, "report-only", ""))
			continue
		}
		if found {
			r := reportFor(item, "vercel-skills-lock-v3", "provider", evidence, "unknown", "provider check unsupported", false, "report-only", "")
			r.Revision = entry.SkillFolderHash
			if entry.SourceType != "github" && entry.SourceType != "git" {
				r.Status = "unsupported source type: " + entry.SourceType
			} else {
				available, drift, err := checkVercelEntry(&session, entry, item.Path)
				r.Drift = drift
				r.UpdateAvailable = available
				if err != nil {
					r.Status = "provider check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					failed = true
				} else {
					r.Status = vercelStatus(action, available, r.Drift)
				}
			}
			reports = append(reports, r)
			continue
		}
		remaining = append(remaining, item)
	}

	if len(remaining) > 0 {
		sink := newReportSink(remaining, state)
		failed = processGit(action, remaining, state, &session, sink, sink) || failed
		reports = append(reports, sink.reports...)
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Identity < reports[j].Identity || reports[i].Identity == reports[j].Identity && reports[i].Path < reports[j].Path
	})
	for _, r := range reports {
		printReport(stdout, r, duplicateName(reports, r.Identity))
	}
	return reports, failed
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

// sourceSession is command-scoped so provider and explicit-track checks sync
// each normalized source/ref pair only once.
type sourceSession struct{ caches map[string]string }

var syncSourceForSession = syncSource

func (s *sourceSession) source(source, ref string) (string, error) {
	key := normalizeSource(source) + "\x00" + ref
	if cache, ok := s.caches[key]; ok {
		return cache, nil
	}
	cache, err := syncSourceForSession(source, ref)
	if err != nil {
		return "", err
	}
	s.caches[key] = cache
	return cache, nil
}

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
	current, err := gitOutput(cache, "rev-parse", "HEAD:"+folder)
	if err != nil {
		return false, "unknown", fmt.Errorf("resolve upstream skill folder: %w", err)
	}
	drift, err := vercelLocalDrift(cache, entry.SkillFolderHash, installed)
	if err != nil {
		return false, "unknown", err
	}
	return current != entry.SkillFolderHash, drift, nil
}

func vercelLocalDrift(cache, expectedTree, installed string) (string, error) {
	expected, err := hashGitTree(cache, expectedTree)
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

func hashGitTree(cache, tree string) (string, error) {
	list, err := gitOutput(cache, "ls-tree", "-r", "--full-tree", tree)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, line := range strings.Split(list, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid git tree entry")
		}
		fields := strings.Fields(parts[0])
		if len(fields) != 3 {
			return "", fmt.Errorf("invalid git tree entry")
		}
		content, err := gitRaw(cache, "cat-file", "-p", fields[2])
		if err != nil {
			return "", err
		}
		data := content
		if fields[0] == "120000" {
			data = append([]byte("symlink\x00"), content...)
		} else {
			data = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", parts[1], len(data))
		if _, err := hash.Write(data); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func gitRaw(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func reportFor(item skill, provider, owner string, evidence []string, drift, status string, available bool, executor, err string) report {
	return report{Identity: item.Name, Path: item.Path, Aliases: item.Aliases, ScanRoot: item.ScanRoot, Host: item.Host, Scope: item.Scope, Provider: provider, Owner: owner, Evidence: evidence, Drift: drift, Status: status, UpdateAvailable: available, Executor: executor, Error: err}
}

func printReport(w io.Writer, r report, showPath bool) {
	name := r.Identity
	if showPath || r.Status == "ambiguous provenance" || strings.HasPrefix(r.Status, "broken") || r.Error != "" {
		name += " [" + r.Path + "]"
	}
	status := r.Status
	if r.Error != "" && !strings.Contains(status, r.Error) {
		status += " (" + r.Error + ")"
	}
	fmt.Fprintf(w, "%s [%s, %s]: %s\n", name, r.Provider, r.Owner, status)
}

func duplicateName(reports []report, name string) bool {
	n := 0
	for _, r := range reports {
		if r.Identity == name {
			n++
		}
	}
	return n > 1
}

type reportSink struct {
	reports []report
}

func newReportSink(items []skill, state *trackedState) *reportSink {
	s := &reportSink{}
	for _, item := range items {
		p, o, e := "local-authoring", "user", "report-only"
		var evidence []string
		if entry, ok := state.findSkill(item); ok {
			p, o, e = "skillctl-track-v1", "skillctl", "staged-replacement"
			evidence = []string{entry.Source}
		}
		drift := "unknown"
		if p != "local-authoring" {
			drift = "clean"
		}
		r := reportFor(item, p, o, evidence, drift, "local/untracked (no update source)", false, e, "")
		if entry, ok := state.findSkill(item); ok {
			r.Revision = entry.InstalledHash
		}
		s.reports = append(s.reports, r)
	}
	return s
}
func (s *reportSink) markGit(items []skill, root string) {
	revision, _ := gitOutput(root, "rev-parse", "HEAD")
	for _, item := range items {
		for i := range s.reports {
			if samePath(s.reports[i].Path, item.Path) && s.reports[i].Provider == "local-authoring" {
				s.reports[i].Provider = "git-worktree"
				s.reports[i].Owner = "repository"
				s.reports[i].Executor = "git-ff-only"
				s.reports[i].Evidence = []string{root}
				s.reports[i].Revision = revision
				s.reports[i].Drift = "clean"
			}
		}
	}
}
func (s *reportSink) failure(item skill, message string) {
	s.set([]skill{item}, "failed")
	for i := range s.reports {
		if samePath(s.reports[i].Path, item.Path) {
			s.reports[i].Error = message
		}
	}
}
func (s *reportSink) Write(p []byte) (int, error) { return len(p), nil }
func (s *reportSink) set(items []skill, message string) {
	for _, item := range items {
		for i := range s.reports {
			if samePath(s.reports[i].Path, item.Path) {
				s.reports[i].Status = message
				s.reports[i].UpdateAvailable = strings.HasPrefix(message, "update available")
				if strings.Contains(message, "modified") || strings.Contains(message, "dirty") {
					s.reports[i].Drift = "modified"
				}
				if strings.HasPrefix(message, "failed") {
					s.reports[i].Error = message
				}
				break
			}
		}
	}
}

func managedOwner(item skill, roots []managedRoot) (string, []string) {
	for _, root := range roots {
		if within(root.Path, item.Path) || samePath(root.Path, item.Path) {
			return root.Owner, []string{root.Path}
		}
		for _, alias := range item.Aliases {
			if within(root.Path, alias) || samePath(root.Path, alias) {
				return root.Owner, []string{root.Path}
			}
		}
	}
	return "", nil
}

type vercelLocks []vercelManifest
type vercelManifest struct {
	path, installRoot string
	lock              vercelLock
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

func (locks vercelLocks) claim(item skill) (vercelLockEntry, []string, bool) {
	for _, lock := range locks {
		for key, entry := range lock.lock.Skills {
			install := filepath.Join(lock.installRoot, key)
			matches := samePath(item.Path, install)
			for _, alias := range item.Aliases {
				matches = matches || samePath(alias, install)
			}
			if matches {
				return entry, []string{lock.path}, true
			}
		}
	}
	return vercelLockEntry{}, nil, false
}
