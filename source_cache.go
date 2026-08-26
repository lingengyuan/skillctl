package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const sourceCacheLockStaleAfter = 30 * time.Minute

type sourceCacheLock struct {
	path string
}

func (l *sourceCacheLock) release() {
	if l != nil {
		_ = os.Remove(l.path)
	}
}

func sourceCacheDirectory() (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheBase, "skillctl", "sources"), nil
}

func sourceCachePath(kind, source, ref string) (string, error) {
	base, err := sourceCacheDirectory()
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(kind + "\x00" + normalizeSource(source) + "\x00" + ref))
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
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("create source cache lock: %w", closeErr)
			}
			return &sourceCacheLock{path: lockPath}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create source cache lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > sourceCacheLockStaleAfter {
			if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for source cache lock: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func gitNonInteractiveEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=Never",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
}

func runGitNetworkCommand(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitNonInteractiveEnv()
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
	value, err := gitOutput(cache, "rev-parse", "--is-bare-repository")
	return err == nil && value == "true"
}

func validWorktreeCache(cache string) bool {
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		return false
	}
	_, err := gitOutput(cache, "rev-parse", "--is-inside-work-tree")
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
		return fmt.Errorf("git clone: %w", err)
	}
	if err := os.Rename(tempPath, cache); err != nil {
		return fmt.Errorf("publish source cache: %w", err)
	}
	return nil
}

// syncObjectSource maintains a bare repository for provider checks. It never
// checks out the repository, so a large source does not pay worktree creation
// cost merely to compare one or more skill trees.
func syncObjectSource(ctx context.Context, source, ref string) (string, error) {
	cache, err := sourceCachePath("object", source, ref)
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
		if _, err := gitNetworkOutput(ctx, cache, args...); err != nil {
			return "", fmt.Errorf("git fetch: %w", err)
		}
	} else if ref == "" {
		defaultRevision, err := gitOutput(cache, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return "", fmt.Errorf("resolve source default branch: %w", err)
		}
		if _, err := gitOutput(cache, "update-ref", "refs/skillctl/default", defaultRevision); err != nil {
			return "", fmt.Errorf("remember source default branch: %w", err)
		}
	}
	revision, err := resolveObjectRevision(cache, ref)
	if err != nil {
		return "", err
	}
	if _, err := gitOutput(cache, "update-ref", "refs/skillctl/selected", revision); err != nil {
		return "", fmt.Errorf("select source ref: %w", err)
	}
	if _, err := gitOutput(cache, "symbolic-ref", "HEAD", "refs/skillctl/selected"); err != nil {
		return "", fmt.Errorf("select source HEAD: %w", err)
	}
	return cache, nil
}

func resolveObjectRevision(cache, ref string) (string, error) {
	if ref == "" {
		for _, candidate := range []string{"refs/skillctl/default", "refs/heads/main", "refs/heads/master", "refs/skillctl/selected", "HEAD"} {
			if revision, err := gitOutput(cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
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
		if revision, err := gitOutput(cache, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
			return revision, nil
		}
	}
	return "", fmt.Errorf("source ref %q was not found as a branch, tag, or commit", ref)
}

func syncWorktreeSource(ctx context.Context, source, ref string) (string, error) {
	cache, err := sourceCachePath("worktree", source, ref)
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
		if !validWorktreeCache(cache) {
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
		if _, err := gitNetworkOutput(ctx, cache, "fetch", "--prune", "--recurse-submodules=no", "origin"); err != nil {
			return "", fmt.Errorf("git fetch: %w", err)
		}
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
