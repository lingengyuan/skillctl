package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
)

// Preview fetches into a disposable bare repository. Even FETCH_HEAD, remote
// refs and maintenance metadata in the installed repository remain unchanged.
func previewRepository(ctx context.Context, timeout time.Duration, repo *repository, stdout, stderr io.Writer) bool {
	fail := func(err error) bool {
		for _, item := range repo.Skills {
			reportFailure(stderr, item, err.Error())
		}
		return true
	}
	branch, err := gitstore.Output(repo.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		printSkills(stdout, repo.Skills, "skipped (detached HEAD)", "blocked", "git_state_blocks_update", false)
		return false
	}
	remote, err := gitstore.Output(repo.Root, "config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" || remote == "." {
		printSkills(stdout, repo.Skills, "skipped (no upstream)", "blocked", "git_state_blocks_update", false)
		return false
	}
	merge, err := gitstore.Output(repo.Root, "config", "--get", "branch."+branch+".merge")
	if err != nil {
		return fail(err)
	}
	source, err := gitstore.Output(repo.Root, "remote", "get-url", remote)
	if err != nil {
		return fail(err)
	}
	if err := validateSourceURL(source); err != nil {
		return fail(err)
	}
	head, err := gitstore.Output(repo.Root, "rev-parse", "HEAD")
	if err != nil {
		return fail(err)
	}
	dirty, err := gitstore.Output(repo.Root, "status", "--porcelain")
	if err != nil {
		return fail(err)
	}
	temporary, err := os.MkdirTemp("", "skillctl-git-preview-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(temporary)
	bare := filepath.Join(temporary, "repository.git")
	if _, err := gitstore.NetworkOutputWithTimeout(ctx, timeout, temporary, "clone", "--bare", "--no-hardlinks", "--", repo.Root, bare); err != nil {
		return fail(err)
	}
	if _, err := gitstore.NetworkOutputWithTimeout(ctx, timeout, bare, "fetch", "--no-tags", "--no-recurse-submodules", "--", source, merge); err != nil {
		return fail(err)
	}
	counts, err := gitstore.Output(bare, "rev-list", "--left-right", "--count", head+"...FETCH_HEAD")
	if err != nil {
		return fail(err)
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return fail(fmt.Errorf("invalid Git comparison"))
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return fail(err)
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return fail(err)
	}
	if dirty != "" || ahead > 0 || behind == 0 {
		decision := decideRepository("check", repo.Allowed, dirty != "", ahead, behind)
		printSkills(stdout, repo.Skills, decision.message, decision.state, decision.reason, decision.available)
		return false
	}
	changed, err := repositorySkillChangesIn(repo.Root, bare, repo.Skills, head, "FETCH_HEAD")
	if err != nil {
		return fail(err)
	}
	for _, item := range repo.Skills {
		if changed[fsutil.PathKey(item.Path)] {
			printSkills(stdout, []skill{item}, fmt.Sprintf("update available (behind %d commits)", behind), "outdated", "upstream_changed", true)
		} else {
			printSkills(stdout, []skill{item}, "up to date", "current", "", false)
		}
	}
	return false
}
