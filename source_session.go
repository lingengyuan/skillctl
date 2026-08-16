package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type sourceSession struct {
	ctx            context.Context
	networkTimeout time.Duration
	caches         map[string]string
	sourceErrors   map[string]error
	objects        map[string]*gitObjectReader
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
		objects:        map[string]*gitObjectReader{},
		treeHashes:     map[string]string{},
		progress:       progress,
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
				cache, err := s.syncSource(item.Source, item.Ref)
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
	cache, err := s.syncSource(source, ref)
	if err != nil {
		s.sourceErrors[key] = err
		s.progressf("Remote source %d failed (%s).\n", number, time.Since(started).Round(time.Millisecond))
		return "", err
	}
	s.progressf("Remote source %d ready (%s).\n", number, time.Since(started).Round(time.Millisecond))
	s.caches[key] = cache
	return cache, nil
}

func (s *sourceSession) syncSource(source, ref string) (string, error) {
	operationCtx, cancel := context.WithTimeout(s.ctx, s.networkTimeout)
	defer cancel()
	cache, err := syncSourceForSession(operationCtx, source, ref)
	if err != nil && operationCtx.Err() != nil {
		return "", fmt.Errorf("network timeout: %w", operationCtx.Err())
	}
	return cache, err
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
