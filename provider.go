package main

import (
	"bufio"
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
	"strconv"
	"strings"
	"time"
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

func inspect(ctx context.Context, action string, skills []skill, state *trackedState, manifests []manifest, managed []managedRoot, stdout, progress io.Writer) ([]report, bool) {
	locks, lockErrors := loadVercelLocks(manifests)
	session := newSourceSession(ctx, progress)
	defer session.close()
	var sourceRequests []sourceRequest
	for _, item := range skills {
		if item.Broken {
			continue
		}
		claim, _, providerClaim := locks.claim(item)
		managedClaim, _ := managedOwner(item, managed)
		tracked, trackedClaim := state.findSkill(item)
		claims := 0
		if providerClaim {
			claims++
		}
		if managedClaim != "" {
			claims++
		}
		if trackedClaim {
			claims++
		}
		if claims != 1 {
			continue
		}
		if providerClaim && claim.Entry.SourceURL != "" && claim.Entry.SkillPath != "" && claim.Entry.SkillFolderHash != "" && (claim.Entry.SourceType == "github" || claim.Entry.SourceType == "git") {
			sourceRequests = append(sourceRequests, sourceRequest{Source: claim.Entry.SourceURL, Ref: claim.Entry.Ref})
		}
		if trackedClaim {
			sourceRequests = append(sourceRequests, sourceRequest{Source: tracked.Source, Ref: tracked.Ref})
		}
	}
	session.prefetch(sourceRequests)
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
		claim, evidence, found := locks.claim(item)
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
			r.Revision = claim.Entry.SkillFolderHash
			if claim.Entry.SourceType != "github" && claim.Entry.SourceType != "git" {
				r.Status = "unsupported source type: " + claim.Entry.SourceType
			} else {
				r.Executor = "vercel-skills-cli"
				available, drift, err := checkVercelEntry(session, claim.Entry, item.Path)
				r.Drift = drift
				r.UpdateAvailable = available
				if err != nil {
					r.Status = "provider check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					failed = true
				} else if action == "update" && available && drift == "clean" {
					updated, err := updateVercelProvider(ctx, session, item, claim, progress)
					if err != nil {
						r.Status = "provider update failed: " + oneLine(err.Error())
						r.Error = oneLine(err.Error())
						failed = true
					} else {
						r.Revision = updated.SkillFolderHash
						r.Drift = "clean"
						r.UpdateAvailable = false
						r.Status = "updated"
					}
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
		failed = processGit(action, remaining, state, session, sink, sink) || failed
		reports = append(reports, sink.reports...)
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Identity < reports[j].Identity || reports[i].Identity == reports[j].Identity && reports[i].Path < reports[j].Path
	})
	for _, r := range reports {
		printReport(stdout, r, duplicateName(reports, r.Identity))
	}
	printTrackRepairHint(stdout, reports)
	return reports, failed
}

func printTrackRepairHint(w io.Writer, reports []report) {
	count := 0
	name := ""
	for _, r := range reports {
		if r.Provider == "local-authoring" && r.Status == "local/untracked (no update source)" {
			count++
			name = r.Identity
		}
	}
	if count == 1 {
		fmt.Fprintf(w, "Hint: register its update source: skillctl track --source SOURCE_URL %s\n", name)
	}
	if count > 1 {
		fmt.Fprintf(w, "Hint: %d skills have no update source; register one with: skillctl track --source SOURCE_URL SKILL_NAME\n", count)
	}
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
		name, valid := readSkill(filepath.Join(item.Path, "SKILL.md"), filepath.Base(item.Path))
		if !valid || name != item.Name {
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
type sourceSession struct {
	ctx          context.Context
	caches       map[string]string
	sourceErrors map[string]error
	objects      map[string]*gitObjectReader
	treeHashes   map[string]string
	progress     io.Writer
	sourceCount  int
}

func newSourceSession(ctx context.Context, progress io.Writer) *sourceSession {
	return &sourceSession{
		ctx:          ctx,
		caches:       map[string]string{},
		sourceErrors: map[string]error{},
		objects:      map[string]*gitObjectReader{},
		treeHashes:   map[string]string{},
		progress:     progress,
	}
}

var syncSourceForSession = syncSource

const maxConcurrentSourceChecks = 4

type sourceRequest struct {
	Source string
	Ref    string
}

type pendingSource struct {
	sourceRequest
	key     string
	number  int
	started time.Time
}

type sourceResult struct {
	pendingSource
	cache string
	err   error
}

func (s *sourceSession) prefetch(requests []sourceRequest) {
	s.ensureSourceMaps()
	seen := map[string]bool{}
	var pending []pendingSource
	for _, request := range requests {
		key := sourceKey(request.Source, request.Ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := s.caches[key]; ok {
			continue
		}
		if _, ok := s.sourceErrors[key]; ok {
			continue
		}
		s.sourceCount++
		item := pendingSource{sourceRequest: request, key: key, number: s.sourceCount, started: time.Now()}
		pending = append(pending, item)
		s.progressf("Checking remote source %d...\n", item.number)
	}
	if len(pending) == 0 {
		return
	}
	workerCount := len(pending)
	if workerCount > maxConcurrentSourceChecks {
		workerCount = maxConcurrentSourceChecks
	}
	jobs := make(chan pendingSource)
	results := make(chan sourceResult, len(pending))
	for i := 0; i < workerCount; i++ {
		go func() {
			for item := range jobs {
				cache, err := syncSourceForSession(s.ctx, item.Source, item.Ref)
				results <- sourceResult{pendingSource: item, cache: cache, err: err}
			}
		}()
	}
	for _, item := range pending {
		jobs <- item
	}
	close(jobs)
	for range pending {
		result := <-results
		if result.err != nil && s.ctx.Err() != nil {
			result.err = fmt.Errorf("network timeout: %w", s.ctx.Err())
		}
		elapsed := time.Since(result.started).Round(time.Millisecond)
		if result.err != nil {
			s.sourceErrors[result.key] = result.err
			s.progressf("Remote source %d failed (%s).\n", result.number, elapsed)
			continue
		}
		s.caches[result.key] = result.cache
		s.progressf("Remote source %d ready (%s).\n", result.number, elapsed)
	}
}

func (s *sourceSession) source(source, ref string) (string, error) {
	s.ensureSourceMaps()
	key := sourceKey(source, ref)
	if cache, ok := s.caches[key]; ok {
		return cache, nil
	}
	if err, ok := s.sourceErrors[key]; ok {
		return "", err
	}
	s.sourceCount++
	number := s.sourceCount
	started := time.Now()
	s.progressf("Checking remote source %d...\n", number)
	cache, err := syncSourceForSession(s.ctx, source, ref)
	if err != nil {
		if s.ctx.Err() != nil {
			err = fmt.Errorf("network timeout: %w", s.ctx.Err())
		}
		s.sourceErrors[key] = err
		s.progressf("Remote source %d failed (%s).\n", number, time.Since(started).Round(time.Millisecond))
		return "", err
	}
	s.progressf("Remote source %d ready (%s).\n", number, time.Since(started).Round(time.Millisecond))
	s.caches[key] = cache
	return cache, nil
}

func (s *sourceSession) ensureSourceMaps() {
	if s.caches == nil {
		s.caches = map[string]string{}
	}
	if s.sourceErrors == nil {
		s.sourceErrors = map[string]error{}
	}
}

func sourceKey(source, ref string) string {
	return normalizeSource(source) + "\x00" + ref
}

func (s *sourceSession) progressf(format string, args ...any) {
	if s.progress != nil {
		fmt.Fprintf(s.progress, format, args...)
	}
}

func (s *sourceSession) gitObject(cache, spec string) (gitObject, error) {
	if s.objects == nil {
		s.objects = map[string]*gitObjectReader{}
	}
	reader := s.objects[cache]
	if reader == nil {
		var err error
		reader, err = newGitObjectReader(cache)
		if err != nil {
			return gitObject{}, err
		}
		s.objects[cache] = reader
	}
	return reader.read(spec)
}

func (s *sourceSession) close() {
	for _, reader := range s.objects {
		_ = reader.close()
	}
}

type gitObject struct {
	Hash string
	Type string
	Data []byte
}

type gitObjectReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
}

func newGitObjectReader(cache string) (*gitObjectReader, error) {
	reader := &gitObjectReader{}
	reader.cmd = exec.Command("git", "-C", cache, "cat-file", "--batch")
	stdin, err := reader.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open git object input: %w", err)
	}
	stdout, err := reader.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open git object output: %w", err)
	}
	reader.stdin = stdin
	reader.stdout = bufio.NewReader(stdout)
	reader.cmd.Stderr = &reader.stderr
	if err := reader.cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start git cat-file --batch: %w", err)
	}
	return reader, nil
}

func (r *gitObjectReader) read(spec string) (gitObject, error) {
	if spec == "" || strings.ContainsAny(spec, "\r\n\x00") {
		return gitObject{}, fmt.Errorf("invalid git object spec")
	}
	if _, err := fmt.Fprintln(r.stdin, spec); err != nil {
		return gitObject{}, fmt.Errorf("request git object: %w", err)
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return gitObject{}, fmt.Errorf("read git object header: %w", err)
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return gitObject{}, fmt.Errorf("git object %q was not found", spec)
	}
	if len(fields) != 3 {
		return gitObject{}, fmt.Errorf("invalid git object header %q", strings.TrimSpace(header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return gitObject{}, fmt.Errorf("invalid git object size %q", fields[2])
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, data); err != nil {
		return gitObject{}, fmt.Errorf("read git object contents: %w", err)
	}
	terminator, err := r.stdout.ReadByte()
	if err != nil || terminator != '\n' {
		return gitObject{}, fmt.Errorf("invalid git object terminator")
	}
	return gitObject{Hash: fields[0], Type: fields[1], Data: data}, nil
}

func (r *gitObjectReader) close() error {
	if r.stdin != nil {
		_ = r.stdin.Close()
		r.stdin = nil
	}
	if err := r.cmd.Wait(); err != nil {
		message := oneLine(r.stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("git cat-file --batch: %s", message)
	}
	return nil
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
