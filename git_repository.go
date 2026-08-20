package main

import (
	"context"
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

type repository struct {
	Root    string
	Skills  []skill
	Allowed bool
}

func processGit(action string, skills []skill, state *trackedState, session *sourceSession, stdout, stderr io.Writer) bool {
	if _, err := exec.LookPath("git"); err != nil {
		if sink, ok := stderr.(*reportSink); ok {
			for _, item := range skills {
				sink.failure(item, "git was not found in PATH")
			}
		} else {
			fmt.Fprintln(stderr, "git was not found in PATH")
		}
		return true
	}
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
	var roots []string
	for root := range repos {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if sink, ok := stdout.(*reportSink); ok {
			sink.markGit(repos[root].Skills, root)
		}
		if processRepository(session.ctx, session.networkTimeout, action, repos[root], stdout, stderr) {
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
	message string
	pull    bool
}

func decideRepository(action string, allowed, dirty bool, ahead, behind int) repositoryDecision {
	if dirty {
		if behind > 0 {
			return repositoryDecision{message: fmt.Sprintf("update available (behind %d commits), skipped (working tree is dirty)", behind)}
		}
		return repositoryDecision{message: "skipped (working tree is dirty)"}
	}
	if ahead > 0 && behind > 0 {
		return repositoryDecision{message: "skipped (branch has diverged)"}
	}
	if ahead > 0 {
		return repositoryDecision{message: fmt.Sprintf("skipped (ahead by %d commits)", ahead)}
	}
	if behind == 0 {
		return repositoryDecision{message: "up to date"}
	}
	if action == "check" {
		return repositoryDecision{message: fmt.Sprintf("update available (behind %d commits)", behind)}
	}
	if !allowed {
		return repositoryDecision{message: "skipped (repository root is outside the scan path)"}
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

func processRepository(ctx context.Context, networkTimeout time.Duration, action string, repo *repository, stdout, stderr io.Writer) bool {
	branch, err := gitOutput(repo.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		printSkills(stdout, repo.Skills, "skipped (detached HEAD)")
		return false
	}
	remote, err := gitOutput(repo.Root, "config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" || remote == "." {
		printSkills(stdout, repo.Skills, "skipped (no upstream)")
		return false
	}
	if _, err := gitOutput(repo.Root, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		printSkills(stdout, repo.Skills, "skipped (no upstream)")
		return false
	}
	if _, err := gitNetworkOutputWithTimeout(ctx, networkTimeout, repo.Root, "fetch", "--prune", "--recurse-submodules=no", remote); err != nil {
		printSkills(stderr, repo.Skills, "failed (git fetch: "+oneLine(err.Error())+")")
		return true
	}
	dirtyOutput, err := gitOutput(repo.Root, "status", "--porcelain")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (git status)")
		return true
	}
	counts, err := gitOutput(repo.Root, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (compare upstream)")
		return true
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		printSkills(stderr, repo.Skills, "failed (invalid Git comparison)")
		return true
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		printSkills(stderr, repo.Skills, "failed (invalid Git comparison)")
		return true
	}

	dirty := dirtyOutput != ""
	if dirty || ahead > 0 || behind == 0 {
		decision := decideRepository(action, repo.Allowed, dirty, ahead, behind)
		printSkills(stdout, repo.Skills, decision.message)
		return false
	}

	changed, err := repositorySkillChanges(repo.Root, repo.Skills, "HEAD", "@{upstream}")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (compare skill trees: "+oneLine(err.Error())+")")
		return true
	}
	if !anySkillChanged(changed) {
		printSkills(stdout, repo.Skills, "up to date")
		return false
	}
	if action == "check" {
		for _, item := range repo.Skills {
			if changed[canonicalPathKey(item.Path)] {
				printSkills(stdout, []skill{item}, fmt.Sprintf("update available (behind %d commits)", behind))
			} else {
				printSkills(stdout, []skill{item}, "up to date")
			}
		}
		return false
	}
	if !repo.Allowed {
		for _, item := range repo.Skills {
			if changed[canonicalPathKey(item.Path)] {
				printSkills(stdout, []skill{item}, "skipped (repository root is outside the scan path)")
			} else {
				printSkills(stdout, []skill{item}, "up to date")
			}
		}
		return false
	}

	oldHead, _ := gitOutput(repo.Root, "rev-parse", "--short", "HEAD")
	if _, err := gitNetworkOutputWithTimeout(ctx, networkTimeout, repo.Root, "-c", "submodule.recurse=false", "pull", "--ff-only", "--no-rebase", "--recurse-submodules=no"); err != nil {
		printSkills(stderr, repo.Skills, "failed (git pull: "+oneLine(err.Error())+")")
		return true
	}
	newHead, err := gitOutput(repo.Root, "rev-parse", "--short", "HEAD")
	if err != nil {
		printSkills(stderr, repo.Skills, "failed (verify updated HEAD)")
		return true
	}
	for _, item := range repo.Skills {
		if !changed[canonicalPathKey(item.Path)] || oldHead == newHead {
			printSkills(stdout, []skill{item}, "up to date")
			continue
		}
		printSkills(stdout, []skill{item}, fmt.Sprintf("updated (%s -> %s)", oldHead, newHead))
	}
	return false
}

func repositorySkillChanges(root string, skills []skill, leftRevision, rightRevision string) (map[string]bool, error) {
	result := make(map[string]bool, len(skills))
	for _, item := range skills {
		left, err := gitTreeAtRevision(root, item.Path, leftRevision)
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", item.Name, leftRevision, err)
		}
		right, err := gitTreeAtRevision(root, item.Path, rightRevision)
		if err != nil {
			// A missing path upstream means the installed skill was removed or
			// relocated and must be treated as a material change.
			result[canonicalPathKey(item.Path)] = true
			continue
		}
		result[canonicalPathKey(item.Path)] = left != right
	}
	return result, nil
}

func gitTreeAtRevision(root, skillPath, revision string) (string, error) {
	rel, err := filepath.Rel(root, skillPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill path is outside repository")
	}
	if rel == "." {
		return gitOutput(root, "rev-parse", revision+"^{tree}")
	}
	return gitOutput(root, "rev-parse", revision+":"+filepath.ToSlash(rel))
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
