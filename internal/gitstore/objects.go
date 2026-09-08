package gitstore

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

// Object contains a Git object identity, type and unmodified payload.
type Object struct {
	Hash string
	Type string
	Data []byte
}

// Reader owns one git cat-file process. Reads are sequential; callers must call Close.
type Reader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
}

// NewReader starts a batch Git object reader for a repository.
func NewReader(cache string) (*Reader, error) {
	return NewReaderContext(context.Background(), cache)
}

func NewReaderContext(ctx context.Context, cache string) (*Reader, error) {
	reader := &Reader{}
	reader.cmd = exec.CommandContext(ctx, "git", "-C", cache, "cat-file", "--batch")
	reader.cmd.Env = NonInteractiveEnv()
	reader.cmd.WaitDelay = time.Second
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

// Read retrieves one object by a validated Git object specification.
func (r *Reader) Read(spec string) (Object, error) {
	if spec == "" || strings.ContainsAny(spec, "\r\n\x00") {
		return Object{}, fmt.Errorf("invalid git object spec")
	}
	if _, err := fmt.Fprintln(r.stdin, spec); err != nil {
		return Object{}, fmt.Errorf("request git object: %w", err)
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return Object{}, fmt.Errorf("read git object header: %w", err)
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return Object{}, fmt.Errorf("git object %q was not found", spec)
	}
	if len(fields) != 3 {
		return Object{}, fmt.Errorf("invalid git object header %q", strings.TrimSpace(header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return Object{}, fmt.Errorf("invalid git object size %q", fields[2])
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, data); err != nil {
		return Object{}, fmt.Errorf("read git object contents: %w", err)
	}
	terminator, err := r.stdout.ReadByte()
	if err != nil || terminator != '\n' {
		return Object{}, fmt.Errorf("invalid git object terminator")
	}
	return Object{Hash: fields[0], Type: fields[1], Data: data}, nil
}

// Close terminates the batch reader and reports any Git process failure.
func (r *Reader) Close() error {
	if r.stdin != nil {
		_ = r.stdin.Close()
		r.stdin = nil
	}
	if err := r.cmd.Wait(); err != nil {
		message := strings.Join(strings.Fields(r.stderr.String()), " ")
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("git cat-file --batch: %s", message)
	}
	return nil
}
