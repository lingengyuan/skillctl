package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/gitstore"
)

type sourceSession struct {
	ctx            context.Context
	networkTimeout time.Duration
	caches         map[string]string
	sourceErrors   map[string]error
	objects        map[string]*gitstore.Reader
	treeHashes     map[string]string
	progress       io.Writer
	sourceCount    int
}

func newSourceSession(ctx context.Context, networkTimeout time.Duration, progress io.Writer) *sourceSession {
	return &sourceSession{
		ctx:            ctx,
		networkTimeout: networkTimeout,
		caches:         map[string]string{},
		sourceErrors:   map[string]error{},
		objects:        map[string]*gitstore.Reader{},
		treeHashes:     map[string]string{},
		progress:       progress,
	}
}

// Provider checks use an object-only cache unless they need filesystem access.
// These internal seams let tests replace both real Git adapters.
var syncSourceForSession = gitstore.SyncObject

// Provider checks use an object-only cache unless they need filesystem access.
// These internal seams let tests replace both real Git adapters.
var syncWorktreeSourceForSession = gitstore.SyncWorktree

const maxConcurrentSourceChecks = 4

type sourceRequest struct {
	Source   string
	Ref      string
	Skills   []string
	Worktree bool
}

type pendingSource struct {
	sourceRequest
	key    string
	number int
	label  string
}

type sourceResult struct {
	pendingSource
	cache   string
	err     error
	elapsed time.Duration
}

type sourceSyncError struct {
	Key   string
	Label string
	Stage string
	Err   error
}

func (e *sourceSyncError) Error() string {
	if e == nil {
		return ""
	}
	if e.Stage == "" {
		return e.Err.Error()
	}
	return e.Stage + ": " + e.Err.Error()
}

func (e *sourceSyncError) Unwrap() error { return e.Err }

func sourceErrorDetails(err error) (key, label, stage string, ok bool) {
	sourceErr, ok := errors.AsType[*sourceSyncError](err)
	if !ok {
		return "", "", "", false
	}
	return sourceErr.Key, sourceErr.Label, sourceErr.Stage, true
}

func (s *sourceSession) prefetch(requests []sourceRequest) {
	s.ensureSourceMaps()
	byKey := map[string]int{}
	var pending []pendingSource
	for _, request := range requests {
		key := sourceModeKey(request.Source, request.Ref, request.Worktree)
		if index, found := byKey[key]; found {
			for _, name := range request.Skills {
				pending[index].Skills = appendUniqueString(pending[index].Skills, name)
			}
			continue
		}
		if _, ok := s.caches[key]; ok {
			continue
		}
		if _, ok := s.sourceErrors[key]; ok {
			continue
		}
		s.sourceCount++
		item := pendingSource{
			sourceRequest: request,
			key:           key,
			number:        s.sourceCount,
			label:         sourceDisplayLabel(request.Source, request.Ref),
		}
		byKey[key] = len(pending)
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return
	}
	for _, item := range pending {
		s.progressf("Checking remote source %d/%d: %s%s...\n", item.number, s.sourceCount, item.label, affectedSkillLabel(item.Skills))
	}
	workerCount := min(len(pending), maxConcurrentSourceChecks)
	jobs := make(chan pendingSource)
	results := make(chan sourceResult, len(pending))
	for range workerCount {
		go func() {
			for item := range jobs {
				started := time.Now()
				cache, err := s.syncRequest(item.sourceRequest)
				results <- sourceResult{pendingSource: item, cache: cache, err: err, elapsed: time.Since(started)}
			}
		}()
	}
	for _, item := range pending {
		jobs <- item
	}
	close(jobs)
	for range pending {
		result := <-results
		elapsed := result.elapsed.Round(time.Millisecond)
		if result.err != nil {
			s.sourceErrors[result.key] = result.err
			s.progressf("Remote source %d/%d failed: %s (%s).\n", result.number, s.sourceCount, result.label, elapsed)
			continue
		}
		s.caches[result.key] = result.cache
		s.progressf("Remote source %d/%d ready: %s (%s).\n", result.number, s.sourceCount, result.label, elapsed)
	}
}

func appendUniqueString(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func affectedSkillLabel(skills []string) string {
	if len(skills) == 0 {
		return ""
	}
	if len(skills) == 1 {
		return " (1 skill)"
	}
	return fmt.Sprintf(" (%d skills)", len(skills))
}

func sourceDisplayLabel(source, ref string) string {
	normalized := strings.TrimSpace(source)
	label := ""
	if value, ok := strings.CutPrefix(normalized, "git@"); ok {
		if host, repository, found := strings.Cut(value, ":"); found {
			label = host + "/" + repository
		}
	}
	if label == "" {
		if parsed, err := url.Parse(normalized); err == nil && parsed.Host != "" {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			label = parsed.Host + "/" + strings.TrimPrefix(parsed.Path, "/")
		}
	}
	if label == "" {
		label = "local/" + filepath.Base(filepath.Clean(normalized))
	}
	label = strings.TrimSuffix(label, ".git")
	if ref != "" {
		label += "@" + ref
	}
	return label
}

func sourceErrorStage(err error) string {
	message := strings.ToLower(oneLine(err.Error()))
	switch {
	case strings.Contains(message, "clone"):
		return "clone"
	case strings.Contains(message, "fetch"):
		return "fetch"
	case strings.Contains(message, "cache lock"):
		return "cache-lock"
	case strings.Contains(message, "cache"):
		return "cache"
	case strings.Contains(message, "checkout"):
		return "checkout"
	case strings.Contains(message, "ref") || strings.Contains(message, "revision"):
		return "resolve"
	default:
		return "sync"
	}
}

func (s *sourceSession) syncRequest(request sourceRequest) (string, error) {
	operationCtx, cancel := context.WithTimeout(s.ctx, s.networkTimeout)
	defer cancel()
	var cache string
	var err error
	if request.Worktree {
		cache, err = syncWorktreeSourceForSession(operationCtx, request.Source, request.Ref)
	} else {
		cache, err = syncSourceForSession(operationCtx, request.Source, request.Ref)
	}
	if err == nil {
		return cache, nil
	}
	if operationCtx.Err() != nil {
		err = fmt.Errorf("network timeout: %w", operationCtx.Err())
	}
	return "", &sourceSyncError{
		Key:   sourceModeKey(request.Source, request.Ref, request.Worktree),
		Label: sourceDisplayLabel(request.Source, request.Ref),
		Stage: sourceErrorStage(err),
		Err:   err,
	}
}

func (s *sourceSession) source(source, ref string) (string, error) {
	return s.sourceWithMode(source, ref, false)
}

func (s *sourceSession) worktreeSource(source, ref string) (string, error) {
	return s.sourceWithMode(source, ref, true)
}

func (s *sourceSession) sourceWithMode(source, ref string, worktree bool) (string, error) {
	s.ensureSourceMaps()
	key := sourceModeKey(source, ref, worktree)
	if cache, ok := s.caches[key]; ok {
		return cache, nil
	}
	if err, ok := s.sourceErrors[key]; ok {
		return "", err
	}
	if !worktree {
		worktreeKey := sourceModeKey(source, ref, true)
		if cache, ok := s.caches[worktreeKey]; ok {
			return cache, nil
		}
		if err, ok := s.sourceErrors[worktreeKey]; ok {
			return "", err
		}
		// Compatibility for command-scoped fixtures and callers created before
		// object/worktree cache modes were split.
		if cache, ok := s.caches[sourceKey(source, ref)]; ok {
			return cache, nil
		}
	}
	s.sourceCount++
	number := s.sourceCount
	label := sourceDisplayLabel(source, ref)
	s.progressf("Checking remote source %d: %s...\n", number, label)
	started := time.Now()
	cache, err := s.syncRequest(sourceRequest{Source: source, Ref: ref, Worktree: worktree})
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		s.sourceErrors[key] = err
		s.progressf("Remote source %d failed: %s (%s).\n", number, label, elapsed)
		return "", err
	}
	s.progressf("Remote source %d ready: %s (%s).\n", number, label, elapsed)
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
	return gitstore.NormalizeSource(source) + "\x00" + ref
}

func sourceModeKey(source, ref string, worktree bool) string {
	mode := "object"
	if worktree {
		mode = "worktree"
	}
	return mode + "\x00" + sourceKey(source, ref)
}

func (s *sourceSession) progressf(format string, args ...any) {
	if s.progress != nil {
		fmt.Fprintf(s.progress, format, args...)
	}
}

func (s *sourceSession) gitObject(cache, spec string) (gitstore.Object, error) {
	if s.objects == nil {
		s.objects = map[string]*gitstore.Reader{}
	}
	reader := s.objects[cache]
	if reader == nil {
		var err error
		reader, err = gitstore.NewReader(cache)
		if err != nil {
			return gitstore.Object{}, err
		}
		s.objects[cache] = reader
	}
	return reader.Read(spec)
}

func (s *sourceSession) close() {
	for _, reader := range s.objects {
		_ = reader.Close()
	}
}
