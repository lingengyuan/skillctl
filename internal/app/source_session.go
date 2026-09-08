package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/gitstore"
)

type sourceSession struct {
	observation    *observation
	ctx            context.Context
	networkTimeout time.Duration
	caches         map[string]string
	sourceErrors   map[string]error
	objects        map[string]*gitstore.Reader
	treeHashes     map[string]string
	revisions      map[string]string
	indexes        map[string]sourceSkillIndex
	stages         []string
	operations     []*operationRecord
	parent         *sourceSession
	wellKnown      map[string]wellKnownIndexResult
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
		revisions:      map[string]string{},
		indexes:        map[string]sourceSkillIndex{},
		wellKnown:      map[string]wellKnownIndexResult{},
		progress:       progress,
	}
}

// All source consumers use the same object-cache adapter.
var syncSourceForSession = gitstore.SyncObject

const maxConcurrentSourceChecks = 4

type sourceRequest struct {
	Source string
	Ref    string
	Skills []string
}

type pendingSource struct {
	sourceRequest
	key    string
	number int
	label  string
}

type sourceResult struct {
	started bool
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
		key := sourceKey(request.Source, request.Ref)
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
	s.progressf("Queued %d remote sources (up to %d running).\n", len(pending), maxConcurrentSourceChecks)
	workerCount := min(len(pending), maxConcurrentSourceChecks)
	jobs := make(chan pendingSource, len(pending))
	results := make(chan sourceResult, 2*len(pending))
	for range workerCount {
		go func() {
			for item := range jobs {
				results <- sourceResult{pendingSource: item, started: true}
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
	for range 2 * len(pending) {
		result := <-results
		if result.started {
			s.progressf("Checking remote source %d/%d: %s%s...\n", result.number, s.sourceCount, result.label, affectedSkillLabel(result.Skills))
			continue
		}
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
	if staged, ok := errors.AsType[*gitstore.OperationError](err); ok {
		return staged.Stage
	}
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
	cache, err := syncSourceForSession(operationCtx, request.Source, request.Ref)
	if err == nil {
		return cache, nil
	}
	stage := sourceErrorStage(err)
	if operationCtx.Err() != nil {
		if s.ctx.Err() != nil {
			err = fmt.Errorf("source operation canceled: %w", s.ctx.Err())
		} else {
			err = fmt.Errorf("network timeout: %w", operationCtx.Err())
		}
	}
	return "", &sourceSyncError{
		Key:   sourceKey(request.Source, request.Ref),
		Label: sourceDisplayLabel(request.Source, request.Ref),
		Stage: stage,
		Err:   err,
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
	label := sourceDisplayLabel(source, ref)
	s.progressf("Checking remote source %d: %s...\n", number, label)
	started := time.Now()
	cache, err := s.syncRequest(sourceRequest{Source: source, Ref: ref})
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

func (s *sourceSession) progressf(format string, args ...any) {
	if s.progress != nil {
		fmt.Fprintf(s.progress, format, args...)
	}
}

func (s *sourceSession) gitObject(cache, spec string) (gitstore.Object, error) {
	if err := s.ctx.Err(); err != nil {
		return gitstore.Object{}, err
	}
	if spec == "HEAD" || strings.HasPrefix(spec, "HEAD:") || strings.HasPrefix(spec, "HEAD^") {
		revision, err := s.revision(cache)
		if err != nil {
			return gitstore.Object{}, err
		}
		spec = revision + strings.TrimPrefix(spec, "HEAD")
	}
	if s.objects == nil {
		s.objects = map[string]*gitstore.Reader{}
	}
	reader := s.objects[cache]
	if reader == nil {
		var err error
		reader, err = gitstore.NewReaderContext(s.ctx, cache)
		if err != nil {
			return gitstore.Object{}, err
		}
		s.objects[cache] = reader
	}
	return reader.Read(spec)
}

func (s *sourceSession) close() {
	if s.parent != nil {
		s.parent.sourceCount = max(s.parent.sourceCount, s.sourceCount)
	}
	for _, stage := range s.stages {
		_ = os.RemoveAll(stage)
	}
	for _, reader := range s.objects {
		_ = reader.Close()
	}
}
