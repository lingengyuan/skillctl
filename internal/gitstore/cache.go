package gitstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type sourceCacheLock struct {
	file *os.File
}

func (l *sourceCacheLock) release() {
	if l != nil && l.file != nil {
		fsutil.Unlock(l.file)
		_ = l.file.Close()
		l.file = nil
	}
}

func sourceCacheDirectory() (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheBase, "skillctl", "sources"), nil
}

// CachePath returns the deterministic cache location for a source, ref and cache mode.
func CachePath(kind, source, ref string) (string, error) {
	base, err := sourceCacheDirectory()
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(kind + "\x00" + NormalizeSource(source) + "\x00" + ref))
	name := kind + "-" + hex.EncodeToString(id[:16])
	if kind == "object" {
		name += ".git"
	}
	return filepath.Join(base, name), nil
}

func acquireSourceCacheLock(ctx context.Context, cache string) (*sourceCacheLock, error) {
	lockPath := cache + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create source cache directory: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, &OperationError{Stage: "cache-lock", Err: err}
		}
		file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create source cache lock: %w", err)
		}
		err = fsutil.TryLock(file)
		if err == nil && !legacySourceCacheLockActive(file) {
			lock := &sourceCacheLock{file: file}
			if err := file.Truncate(0); err != nil {
				lock.release()
				return nil, err
			}
			if _, err := fmt.Fprintf(file, "format=2\npid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				lock.release()
				return nil, err
			}
			// Keep the inode. Unlinking permits two processes to lock different
			// files with the same name; the kernel releases ownership on exit.
			return lock, nil
		}
		if err == nil {
			fsutil.Unlock(file)
		}
		_ = file.Close()
		if err != nil && !fsutil.LockBusy(err) {
			return nil, fmt.Errorf("lock source cache: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, &OperationError{Stage: "cache-lock", Err: ctx.Err()}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Older binaries used O_EXCL marker files. Do not take over a live legacy
// writer; a marker left by an exited process can be upgraded immediately.
func legacySourceCacheLockActive(file *os.File) bool {
	var buffer [256]byte
	n, _ := file.ReadAt(buffer[:], 0)
	text := string(buffer[:n])
	if strings.HasPrefix(text, "format=2\n") {
		return false
	}
	for line := range strings.SplitSeq(text, "\n") {
		if value, ok := strings.CutPrefix(line, "pid="); ok {
			pid, err := strconv.Atoi(value)
			return err == nil && pid > 0 && fsutil.ProcessRunning(pid)
		}
	}
	return false
}

func runGitNetworkCommand(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = NonInteractiveEnv()
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("network timeout: %w", ctx.Err())
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return strings.TrimSpace(string(output)), nil
}

func validObjectCache(cache string) bool {
	value, err := Output(cache, "rev-parse", "--is-bare-repository")
	return err == nil && value == "true"
}

// ValidWorktree checks whether a cache contains a usable Git worktree.
func ValidWorktree(cache string) bool {
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		return false
	}
	_, err := Output(cache, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func quarantineSourceCache(cache string) error {
	if _, err := os.Lstat(cache); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	quarantine := fmt.Sprintf("%s.corrupt-%d", cache, time.Now().UnixNano())
	if err := os.Rename(cache, quarantine); err != nil {
		return fmt.Errorf("quarantine invalid source cache: %w", err)
	}
	return nil
}

func cloneSourceAtomically(ctx context.Context, source, cache string, bare bool) error {
	parent := filepath.Dir(cache)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create source cache: %w", err)
	}
	temp, err := os.MkdirTemp(parent, ".skillctl-source-clone-")
	if err != nil {
		return fmt.Errorf("create source clone stage: %w", err)
	}
	tempPath := temp
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("prepare source clone stage: %w", err)
	}
	defer os.RemoveAll(tempPath)
	args := []string{"clone"}
	if bare {
		args = append(args, "--bare")
	} else {
		args = append(args, "--no-checkout")
	}
	args = append(args, "--recurse-submodules=no", source, tempPath)
	if _, err := runGitNetworkCommand(ctx, parent, args...); err != nil {
		return &OperationError{Stage: "clone", Err: err}
	}
	if err := os.Rename(tempPath, cache); err != nil {
		return fmt.Errorf("publish source cache: %w", err)
	}
	return nil
}

// SyncObject maintains a bare repository for provider checks. It never
// checks out the repository, so a large source does not pay worktree creation
// cost merely to compare one or more skill trees.
func SyncObject(ctx context.Context, source, ref string) (string, error) {
	cache, err := CachePath("object", source, ref)
	if err != nil {
		return "", err
	}
	lock, err := acquireSourceCacheLock(ctx, cache)
	if err != nil {
		return "", err
	}
	defer lock.release()

	fresh := false
	if _, statErr := os.Lstat(cache); statErr == nil {
		if !validObjectCache(cache) {
			if err := quarantineSourceCache(cache); err != nil {
				return "", err
			}
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect source cache: %w", statErr)
	}
	if _, statErr := os.Lstat(cache); errors.Is(statErr, os.ErrNotExist) {
		if err := cloneSourceAtomically(ctx, source, cache, true); err != nil {
			return "", err
		}
		fresh = true
	} else if statErr != nil {
		return "", fmt.Errorf("inspect source cache: %w", statErr)
	}

	// A fresh clone already contains the advertised refs. Avoid the redundant
	// clone-then-fetch sequence that previously doubled first-run network work.
	if !fresh {
		args := []string{"fetch", "--prune", "--force", "--recurse-submodules=no", "origin", "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}
		if ref == "" {
			args = append(args, "+HEAD:refs/skillctl/default")
		}
		if _, err := NetworkOutput(ctx, cache, args...); err != nil {
			return "", &OperationError{Stage: "fetch", Err: err}
		}
	} else if ref == "" {
		defaultRevision, err := OutputContext(ctx, cache, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return "", fmt.Errorf("resolve source default branch: %w", err)
		}
		if _, err := OutputContext(ctx, cache, "update-ref", "refs/skillctl/default", defaultRevision); err != nil {
			return "", fmt.Errorf("remember source default branch: %w", err)
		}
	}
	revision, err := resolveObjectRevision(ctx, cache, ref)
	if err != nil {
		return "", err
	}
	if _, err := OutputContext(ctx, cache, "update-ref", "refs/skillctl/selected", revision); err != nil {
		return "", fmt.Errorf("select source ref: %w", err)
	}
	if _, err := OutputContext(ctx, cache, "symbolic-ref", "HEAD", "refs/skillctl/selected"); err != nil {
		return "", fmt.Errorf("select source HEAD: %w", err)
	}
	return cache, nil
}

func resolveObjectRevision(ctx context.Context, cache, ref string) (string, error) {
	if ref == "" {
		for _, candidate := range []string{"refs/skillctl/default", "refs/heads/main", "refs/heads/master", "refs/skillctl/selected", "HEAD"} {
			if revision, err := OutputContext(ctx, cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
				return revision, nil
			}
		}
		return "", fmt.Errorf("source default branch was not found")
	}
	clean := strings.TrimPrefix(ref, "refs/heads/")
	candidates := []string{
		"refs/heads/" + clean,
		"refs/remotes/origin/" + clean,
		"refs/tags/" + clean,
	}
	if strings.HasPrefix(ref, "refs/") {
		candidates = []string{ref}
	} else if isCommitRef(ref) {
		candidates = append(candidates, ref)
	}
	for _, candidate := range candidates {
		if revision, err := OutputContext(ctx, cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
			return revision, nil
		}
	}
	return "", fmt.Errorf("source ref %q was not found as a branch, tag, or commit", ref)
}

// SyncWorktree refreshes a source under a kernel lock and checks out the requested revision.
func SyncWorktree(ctx context.Context, source, ref string) (string, error) {
	cache, err := CachePath("worktree", source, ref)
	if err != nil {
		return "", err
	}
	lock, err := acquireSourceCacheLock(ctx, cache)
	if err != nil {
		return "", err
	}
	defer lock.release()

	fresh := false
	if _, statErr := os.Lstat(cache); statErr == nil {
		if !ValidWorktree(cache) {
			if err := quarantineSourceCache(cache); err != nil {
				return "", err
			}
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect source cache: %w", statErr)
	}
	if _, statErr := os.Lstat(cache); errors.Is(statErr, os.ErrNotExist) {
		if err := cloneSourceAtomically(ctx, source, cache, false); err != nil {
			return "", err
		}
		fresh = true
	} else if statErr != nil {
		return "", fmt.Errorf("inspect source cache: %w", statErr)
	}
	if !fresh {
		if _, err := NetworkOutput(ctx, cache, "fetch", "--prune", "--recurse-submodules=no", "origin"); err != nil {
			return "", &OperationError{Stage: "fetch", Err: err}
		}
	}
	revision, err := resolveSourceRevision(ctx, cache, ref)
	if err != nil {
		return "", err
	}
	if _, err := OutputContext(ctx, cache, "checkout", "--force", "--detach", revision); err != nil {
		return "", &OperationError{Stage: "checkout", Err: err}
	}
	return cache, nil
}

// NormalizeSource canonicalizes existing local sources while preserving remote source URLs.
func NormalizeSource(source string) string {
	if _, err := os.Stat(source); err == nil {
		if absolute, err := filepath.Abs(source); err == nil {
			return filepath.Clean(absolute)
		}
	}
	return source
}

func resolveSourceRevision(ctx context.Context, cache, ref string) (string, error) {
	if ref == "" {
		return "refs/remotes/origin/HEAD", nil
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
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
		if _, err := OutputContext(ctx, cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
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
