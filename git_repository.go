package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type repository struct {
	Root    string
	Skills  []skill
	Allowed bool
}

func processGit(action string, skills []skill, state *trackedState, session *sourceSession, stdout, stderr io.Writer) bool {

	repos := map[string]*repository{}
	rootCache := map[string]gitRootResult{}
	var copied []skill
	failed := false
	for _, item := range skills {
		if _, tracked := state.findSkill(item); tracked {
			copied = append(copied, item)
			continue
		}
		root, found := findGitRoot(item.Path, rootCache)
		if !found {
			copied = append(copied, item)
			continue
		}
		relSkill, err := filepath.Rel(root, filepath.Join(item.Path, "SKILL.md"))
		if err != nil || !within(root, filepath.Join(item.Path, "SKILL.md")) || !gitTracks(root, relSkill) {
			copied = append(copied, item)
			continue
		}
		repo := repos[root]
		if repo == nil {
			repo = &repository{Root: root}
			repos[root] = repo
		}
		repo.Skills = append(repo.Skills, item)
		if within(item.ScanRoot, root) || filepath.Clean(item.Path) == root {
			repo.Allowed = true
		}
	}
	roots := slices.Sorted(maps.Keys(repos))
	for _, root := range roots {
		if sink, ok := stdout.(*reportSink); ok {
			sink.markGit(repos[root].Skills, root)
		}
		if processRepository(session.ctx, session.networkTimeout, action, repos[root], stdout, stderr, state.readOnly) {
			failed = true
		}
	}
	if processTracked(action, copied, state, session, stdout, stderr) {
		failed = true
	}
	return failed
}

type gitRootResult struct {
	root  string
	found bool
}

type repositoryDecision struct {
	state     string
	reason    string
	available bool
	message   string
	pull      bool
}

func decideRepository(action string, allowed, dirty bool, ahead, behind int) repositoryDecision {
	if dirty {
		if behind > 0 {
			return repositoryDecision{state: "modified", reason: "local_changes", available: true, message: fmt.Sprintf("update available (behind %d commits), skipped (working tree is dirty)", behind)}
		}
		return repositoryDecision{state: "modified", reason: "local_changes", available: false, message: "skipped (working tree is dirty)"}
	}
	if ahead > 0 && behind > 0 {
		return repositoryDecision{state: "blocked", reason: "git_state_blocks_update", available: false, message: "skipped (branch has diverged)"}
	}
	if ahead > 0 {
		return repositoryDecision{state: "blocked", reason: "git_state_blocks_update", available: false, message: fmt.Sprintf("skipped (ahead by %d commits)", ahead)}
	}
	if behind == 0 {
		return repositoryDecision{state: "current", reason: "", available: false, message: "up to date"}
	}
	if action == "check" {
		return repositoryDecision{state: "outdated", reason: "upstream_changed", available: true, message: fmt.Sprintf("update available (behind %d commits)", behind)}
	}
	if !allowed {
		return repositoryDecision{state: "blocked", reason: "git_state_blocks_update", available: false, message: "skipped (repository root is outside the scan path)"}
	}
	return repositoryDecision{pull: true}
}

func findGitRoot(path string, cache map[string]gitRootResult) (string, bool) {
	dir := filepath.Clean(path)
	var visited []string
	result := gitRootResult{}
	for {
		if cached, ok := cache[dir]; ok {
			result = cached
			break
		}
		visited = append(visited, dir)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			result = gitRootResult{root: dir, found: true}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for _, visitedDir := range visited {
		cache[visitedDir] = result
	}
	return result.root, result.found
}

func gitTracks(root, path string) bool {
	_, err := gitOutput(root, "ls-files", "--error-unmatch", "--", filepath.ToSlash(path))
	return err == nil
}

func processRepository(ctx context.Context, networkTimeout time.Duration, action string, repo *repository, stdout, stderr io.Writer, preview ...bool) bool {
	if len(preview) > 0 && preview[0] {
		return previewRepository(ctx, networkTimeout, repo, stdout, stderr)
	}
	branch, err := gitOutput(repo.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		printSkills(stdout, repo.Skills, "skipped (detached HEAD)", "blocked", "git_state_blocks_update", false)
		return false
	}
	remote, err := gitOutput(repo.Root, "config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" || remote == "." {
		printSkills(stdout, repo.Skills, "skipped (no upstream)", "blocked", "git_state_blocks_update", false)
		return false
	}
	if _, err := gitOutput(repo.Root, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		printSkills(stdout, repo.Skills, "skipped (no upstream)", "blocked", "git_state_blocks_update", false)
		return false
	}
	if _, err := gitNetworkOutputWithTimeout(ctx, networkTimeout, repo.Root, "fetch", "--prune", "--recurse-submodules=no", remote); err != nil {
		printSkills(stderr, repo.Skills, "failed (git fetch: "+oneLine(err.Error())+")", "error", "provider_error", false)
		return true
	}
	dirtyOutput, err := gitOutput(repo.Root, "status", "--porcelain")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (git status)", "error", "provider_error", false)
		return true
	}
	counts, err := gitOutput(repo.Root, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (compare upstream)", "error", "provider_error", false)
		return true
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		printSkills(stderr, repo.Skills, "failed (invalid Git comparison)", "error", "provider_error", false)
		return true
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		printSkills(stderr, repo.Skills, "failed (invalid Git comparison)", "error", "provider_error", false)
		return true
	}

	dirty := dirtyOutput != ""
	if dirty || ahead > 0 || behind == 0 {
		decision := decideRepository(action, repo.Allowed, dirty, ahead, behind)
		printSkills(stdout, repo.Skills, decision.message, decision.state, decision.reason, decision.available)
		return false
	}

	changed, err := repositorySkillChanges(repo.Root, repo.Skills, "HEAD", "@{upstream}")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (compare skill trees: "+oneLine(err.Error())+")", "error", "provider_error", false)
		return true
	}
	if !anySkillChanged(changed) {
		printSkills(stdout, repo.Skills, "up to date", "current", "", false)
		return false
	}
	if action == "check" {
		for _, item := range repo.Skills {
			if changed[canonicalPathKey(item.Path)] {
				printSkills(stdout, []skill{item}, fmt.Sprintf("update available (behind %d commits)", behind), "outdated", "upstream_changed", true)
			} else {
				printSkills(stdout, []skill{item}, "up to date", "current", "", false)
			}
		}
		return false
	}
	if !repo.Allowed {
		for _, item := range repo.Skills {
			if changed[canonicalPathKey(item.Path)] {
				printSkills(stdout, []skill{item}, "skipped (repository root is outside the scan path)", "blocked", "git_state_blocks_update", false)
			} else {
				printSkills(stdout, []skill{item}, "up to date", "current", "", false)
			}
		}
		return false
	}

	oldHead, _ := gitOutput(repo.Root, "rev-parse", "--short", "HEAD")

	operation, err := beginGitOperation(repo.Root, repo.Skills)
	if err != nil {
		for _, item := range repo.Skills {
			reportFailure(stderr, item, "backup repository: "+err.Error())
		}
		return true
	}
	_, pullErr := gitNetworkOutputWithTimeout(ctx, networkTimeout, repo.Root, "-c", "submodule.recurse=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "pull", "--ff-only", "--no-rebase", "--recurse-submodules=no")
	if err := operation.finishExternal(pullErr); err != nil {
		for _, item := range repo.Skills {
			reportFailure(stderr, item, "git pull: "+err.Error())
		}
		return true
	}

	newHead, err := gitOutput(repo.Root, "rev-parse", "--short", "HEAD")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (verify updated HEAD)", "error", "provider_error", false)
		return true
	}
	for _, item := range repo.Skills {
		if !changed[canonicalPathKey(item.Path)] || oldHead == newHead {
			printSkills(stdout, []skill{item}, "up to date", "current", "", false)
			continue
		}
		printSkills(stdout, []skill{item}, fmt.Sprintf("updated (%s -> %s)", oldHead, newHead), "current", "", false)
	}
	return false
}

func repositorySkillChanges(root string, skills []skill, leftRevision, rightRevision string) (map[string]bool, error) {
	return repositorySkillChangesIn(root, root, skills, leftRevision, rightRevision)
}

func repositorySkillChangesIn(root, gitRoot string, skills []skill, leftRevision, rightRevision string) (map[string]bool, error) {
	result := make(map[string]bool, len(skills))
	if len(skills) == 0 {
		return result, nil
	}

	relativePaths := make([]string, len(skills))
	args := []string{"-C", gitRoot, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", leftRevision, rightRevision, "--"}
	for index, item := range skills {
		rel, err := filepath.Rel(root, item.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: skill path is outside repository", item.Name)
		}
		rel = filepath.ToSlash(rel)
		relativePaths[index] = rel
		args = append(args, rel)
	}

	cmd := exec.Command("git", args...)
	cmd.Env = append(gitNonInteractiveEnv(), "GIT_LITERAL_PATHSPECS=1")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if message := strings.TrimSpace(string(exitErr.Stderr)); message != "" {
				return nil, fmt.Errorf("compare repository revisions: %s", message)
			}
		}
		return nil, fmt.Errorf("compare repository revisions: %w", err)
	}
	for changedPath := range strings.SplitSeq(string(output), "\x00") {
		if changedPath == "" {
			continue
		}
		for index, item := range skills {
			rel := relativePaths[index]
			if rel == "." || changedPath == rel || strings.HasPrefix(changedPath, rel+"/") {
				result[canonicalPathKey(item.Path)] = true
			}
		}
	}
	return result, nil
}

func anySkillChanged(changed map[string]bool) bool {
	for _, value := range changed {
		if value {
			return true
		}
	}
	return false
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitNetworkOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("network timeout: %w", ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitNetworkOutputWithTimeout(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return gitNetworkOutput(operationCtx, dir, args...)
}
