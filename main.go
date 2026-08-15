package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var version = "0.3.2"

const defaultNetworkTimeout = 10 * time.Second

var defaultConfig = `# Directories are scanned recursively for SKILL.md files.
# Relative paths are resolved from this file.

[[roots]]
path = "~/.agents/skills"
host = "universal"
scope = "user"

[[roots]]
path = "~/.config/agents/skills"
host = "universal"
scope = "user"

[[roots]]
path = "~/.codex/skills"
host = "codex"
scope = "user"

[[roots]]
path = "~/.claude/skills"
host = "claude"
scope = "user"

[[roots]]
path = "~/.cursor/skills"
host = "cursor"
scope = "user"

[[roots]]
path = "~/.copilot/skills"
host = "copilot"
scope = "user"

[[roots]]
path = "~/.gemini/skills"
host = "gemini"
scope = "user"

[[roots]]
path = "~/.config/opencode/skills"
host = "opencode"
scope = "user"

[[manifests]]
kind = "vercel-skills-lock-v3"
path = "~/.agents/.skill-lock.json"
install_root = "~/.agents/skills"

[[managed_roots]]
path = "~/.codex/skills/.system"
owner = "codex"
`

var legacyDefaultConfig = `# Directories are scanned recursively for SKILL.md files.
# Relative paths are resolved from this file. Command-line --path values replace this list.
paths = [
  "~/.agents/skills",
  "~/.config/agents/skills",
  "~/.codex/skills",
  "~/.claude/skills",
  "~/.cursor/skills",
  "~/.copilot/skills",
  "~/.gemini/skills",
  "~/.config/opencode/skills",
]
`

type config struct {
	NetworkTimeout string        `toml:"network_timeout"`
	Paths          []string      `toml:"paths"`
	Roots          []scanRoot    `toml:"roots"`
	Manifests      []manifest    `toml:"manifests"`
	ManagedRoots   []managedRoot `toml:"managed_roots"`
}

type scanRoot struct {
	Path  string `toml:"path"`
	Host  string `toml:"host"`
	Scope string `toml:"scope"`
}

type manifest struct {
	Kind        string `toml:"kind"`
	Path        string `toml:"path"`
	InstallRoot string `toml:"install_root"`
}

type managedRoot struct {
	Path  string `toml:"path"`
	Owner string `toml:"owner"`
}

// skill is one installed instance.  Path is kept as the canonical path for
// compatibility with the v0.1 explicit-track state.
type skill struct {
	Name       string
	Path       string
	Aliases    []string
	ScanRoot   string
	Host       string
	Scope      string
	Broken     bool
	LinkTarget string
}

type options struct {
	Paths      []string
	ConfigPath string
	Names      []string
	Help       bool
	Source     string
	Ref        string
	SkillPath  string
	JSON       bool
	Timeout    time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	if args[0] == "--version" {
		fmt.Fprintf(stdout, "skillctl %s\n", version)
		return 0
	}
	if args[0] == "--help" {
		usage(stdout)
		return 0
	}
	if args[0] != "check" && args[0] != "update" && args[0] != "track" {
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		return 2
	}

	opt, code := parseCommand(args[1:], stderr)
	if code != 0 {
		return code
	}
	if opt.Help {
		usage(stdout)
		return 0
	}
	var roots []scanRoot
	var manifests []manifest
	var managed []managedRoot
	networkTimeout := defaultNetworkTimeout
	ignoreMissing := false
	var err error
	roots, manifests, managed, networkTimeout, ignoreMissing, err = loadConfig(opt.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(opt.Paths) > 0 {
		roots = nil
		for _, path := range opt.Paths {
			roots = append(roots, scanRoot{Path: resolvePath(path, "."), Host: "manual", Scope: "local"})
		}
	}
	if opt.Timeout > 0 {
		networkTimeout = opt.Timeout
	}

	progress := io.Writer(io.Discard)
	if args[0] != "track" && !opt.JSON {
		progress = stderr
	}
	started := time.Now()
	scanStarted := time.Now()
	fmt.Fprintln(progress, "Scanning skills...")
	skills, scanFailed := scan(roots, ignoreMissing, stderr)
	fmt.Fprintf(progress, "Found %d skill instances (%s).\n", len(skills), time.Since(scanStarted).Round(time.Millisecond))
	if len(opt.Names) > 0 {
		var err error
		skills, err = selectSkills(skills, opt.Names)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if len(skills) == 0 {
		if opt.JSON {
			_, _ = stdout.Write([]byte("[]\n"))
		} else {
			fmt.Fprintln(stdout, "No skills found.")
		}
		if scanFailed {
			return 1
		}
		return 0
	}
	state, err := loadTrackedState()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx := context.Background()
	if args[0] == "track" {
		if len(skills) != 1 {
			fmt.Fprintln(stderr, "track requires exactly one unambiguous skill")
			return 1
		}
		operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		err := trackCopiedSkill(operationCtx, skills[0], opt.Source, opt.Ref, opt.SkillPath, state)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "%s: failed (%s)\n", skills[0].Name, oneLine(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "%s: tracked\n", skills[0].Name)
		return 0
	}
	resultWriter := stdout
	if opt.JSON {
		resultWriter = io.Discard
	}
	fmt.Fprintf(progress, "Checking %d skill instances...\n", len(skills))
	reports, gitFailed := inspect(ctx, networkTimeout, args[0], skills, state, manifests, managed, resultWriter, progress)
	if opt.JSON {
		if err := json.NewEncoder(stdout).Encode(reports); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if scanFailed || gitFailed {
		fmt.Fprintf(progress, "Finished with errors (%s).\n", time.Since(started).Round(time.Millisecond))
		return 1
	}
	fmt.Fprintf(progress, "Finished (%s).\n", time.Since(started).Round(time.Millisecond))
	return 0
}

func parseCommand(args []string, stderr io.Writer) (options, int) {
	var opt options
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--help":
			opt.Help = true
			args = args[1:]
		case "--path":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--path requires a directory")
				return options{}, 2
			}
			opt.Paths = append(opt.Paths, args[1])
			args = args[2:]
		case "--config":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--config requires a file")
				return options{}, 2
			}
			opt.ConfigPath = args[1]
			args = args[2:]
		case "--json":
			opt.JSON = true
			args = args[1:]
		case "--timeout":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--timeout requires a positive duration")
				return options{}, 2
			}
			duration, err := time.ParseDuration(args[1])
			if err != nil || duration <= 0 {
				fmt.Fprintln(stderr, "--timeout requires a positive duration, for example 10s")
				return options{}, 2
			}
			opt.Timeout = duration
			args = args[2:]
		case "--source":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--source requires a Git URL or path")
				return options{}, 2
			}
			opt.Source = args[1]
			args = args[2:]
		case "--ref":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--ref requires a branch, tag, or commit")
				return options{}, 2
			}
			opt.Ref = args[1]
			args = args[2:]
		case "--skill-path":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--skill-path requires a repository-relative path")
				return options{}, 2
			}
			opt.SkillPath = args[1]
			args = args[2:]
		default:
			fmt.Fprintf(stderr, "unknown option: %s\n", args[0])
			return options{}, 2
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			fmt.Fprintln(stderr, "options must appear before skill names")
			return options{}, 2
		}
	}
	opt.Names = args
	return opt, 0
}

func loadConfig(explicit string) ([]scanRoot, []manifest, []managedRoot, time.Duration, bool, error) {
	path := explicit
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(dir, "skillctl", "config.toml")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && explicit == "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("create config directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(defaultConfig), 0o644); err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("create default config: %w", err)
		}
	} else if err != nil {
		return nil, nil, nil, 0, false, fmt.Errorf("read config: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, 0, false, fmt.Errorf("read config: %w", err)
	}
	if string(content) == legacyDefaultConfig {
		temp, err := os.CreateTemp(filepath.Dir(path), "config-*.toml")
		if err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("migrate legacy config: %w", err)
		}
		tempName := temp.Name()
		defer os.Remove(tempName)
		if _, err := temp.WriteString(defaultConfig); err != nil {
			temp.Close()
			return nil, nil, nil, 0, false, fmt.Errorf("migrate legacy config: %w", err)
		}
		if err := temp.Close(); err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("migrate legacy config: %w", err)
		}
		if err := os.Rename(tempName, path); err != nil {
			return nil, nil, nil, 0, false, fmt.Errorf("migrate legacy config: %w", err)
		}
		content = []byte(defaultConfig)
	}
	var cfg config
	meta, err := toml.Decode(string(content), &cfg)
	if err != nil {
		return nil, nil, nil, 0, false, fmt.Errorf("invalid config: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, nil, nil, 0, false, fmt.Errorf("invalid config: unknown field %q", undecoded[0])
	}
	networkTimeout := defaultNetworkTimeout
	if cfg.NetworkTimeout != "" {
		networkTimeout, err = time.ParseDuration(cfg.NetworkTimeout)
		if err != nil || networkTimeout <= 0 {
			return nil, nil, nil, 0, false, fmt.Errorf("invalid config: network_timeout must be a positive duration")
		}
	}
	base := filepath.Dir(path)
	for _, path := range cfg.Paths {
		cfg.Roots = append(cfg.Roots, scanRoot{Path: path, Host: "legacy", Scope: "user"})
	}
	if len(cfg.Roots) == 0 {
		return nil, nil, nil, 0, false, fmt.Errorf("invalid config: at least one root is required")
	}
	for i := range cfg.Roots {
		if cfg.Roots[i].Path == "" || cfg.Roots[i].Host == "" || cfg.Roots[i].Scope == "" {
			return nil, nil, nil, 0, false, fmt.Errorf("invalid config: roots require path, host, and scope")
		}
		cfg.Roots[i].Path = resolvePath(cfg.Roots[i].Path, base)
	}
	for i := range cfg.Manifests {
		if cfg.Manifests[i].Kind != "vercel-skills-lock-v3" {
			return nil, nil, nil, 0, false, fmt.Errorf("invalid config: unsupported manifest kind %q", cfg.Manifests[i].Kind)
		}
		if cfg.Manifests[i].Path == "" || cfg.Manifests[i].InstallRoot == "" {
			return nil, nil, nil, 0, false, fmt.Errorf("invalid config: manifests require path and install_root")
		}
		cfg.Manifests[i].Path = resolvePath(cfg.Manifests[i].Path, base)
		cfg.Manifests[i].InstallRoot = resolvePath(cfg.Manifests[i].InstallRoot, base)
	}
	for i := range cfg.ManagedRoots {
		if cfg.ManagedRoots[i].Path == "" || cfg.ManagedRoots[i].Owner == "" {
			return nil, nil, nil, 0, false, fmt.Errorf("invalid config: managed_roots require path and owner")
		}
		cfg.ManagedRoots[i].Path = resolvePath(cfg.ManagedRoots[i].Path, base)
	}
	return cfg.Roots, cfg.Manifests, cfg.ManagedRoots, networkTimeout, string(content) == defaultConfig, nil
}

func resolvePath(path, base string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func scan(roots []scanRoot, ignoreMissing bool, stderr io.Writer) ([]skill, bool) {
	seen := map[string]int{}
	visitedDirs := map[string]string{}
	var skills []skill
	failed := false
	for _, rootSpec := range roots {
		root := rootSpec.Path
		_, err := os.Stat(root)
		if err != nil {
			if ignoreMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			fmt.Fprintf(stderr, "%s: %v\n", root, err)
			failed = true
			continue
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			realRoot = root
		}
		if absolute, absErr := filepath.Abs(realRoot); absErr == nil {
			realRoot = absolute
		}
		err = walkFollowingLinks(root, visitedDirs, func(path, real string) error {
			dir := filepath.Dir(path)
			name, err := readSkill(path)
			if err != nil {
				fmt.Fprintf(stderr, "%s: skipped (%v)\n", path, err)
				return nil
			}
			key := canonicalPathKey(real)
			if index, ok := seen[key]; ok {
				skills[index].Aliases = appendUnique(skills[index].Aliases, dir)
				return nil
			}
			seen[key] = len(skills)
			skills = append(skills, skill{Name: name, Path: real, Aliases: []string{dir}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope})
			return nil
		}, func(path, target string) {
			name := filepath.Base(path)
			skills = append(skills, skill{Name: name, Path: path, Aliases: []string{path}, ScanRoot: realRoot, Host: rootSpec.Host, Scope: rootSpec.Scope, Broken: true, LinkTarget: target})
		}, func(alias, canonical string) {
			addAliasesForVisitedDir(skills, alias, canonical)
		})
		if err != nil {
			fmt.Fprintf(stderr, "%s: scan failed: %v\n", root, err)
			failed = true
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			return skills[i].Path < skills[j].Path
		}
		return skills[i].Name < skills[j].Name
	})
	return skills, failed
}

func addAliasesForVisitedDir(skills []skill, alias, canonical string) {
	for i := range skills {
		if !within(canonical, skills[i].Path) {
			continue
		}
		rel, err := filepath.Rel(canonical, skills[i].Path)
		if err == nil {
			skills[i].Aliases = appendUnique(skills[i].Aliases, filepath.Join(alias, rel))
		}
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if samePath(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func canonicalPathKey(path string) string {
	clean := filepath.Clean(path)
	if filepath.Separator == '\\' {
		return strings.ToLower(clean)
	}
	return clean
}

func walkFollowingLinks(dir string, visited map[string]string, visitSkill func(string, string) error, visitBroken func(string, string), visitAlias func(string, string)) error {
	key, canonical, err := identifyDirectory(dir)
	if err != nil {
		return err
	}
	if existing, found := visited[key]; found {
		visitAlias(dir, existing)
		return nil
	}
	visited[key] = canonical
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				target := ""
				if link, linkErr := os.Readlink(path); linkErr == nil {
					target = link
					if !filepath.IsAbs(target) {
						target = filepath.Join(dir, target)
					}
				}
				visitBroken(path, target)
				continue
			}
			return err
		}
		if info.IsDir() {
			if err := walkFollowingLinks(path, visited, visitSkill, visitBroken, visitAlias); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					visitBroken(path, "")
					continue
				}
				return err
			}
			continue
		}
		if entry.Name() == "SKILL.md" {
			if err := visitSkill(path, canonical); err != nil {
				return err
			}
		}
	}
	return nil
}

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
		if err != nil || !within(root, filepath.Join(item.Path, "SKILL.md")) || gitTracks(root, relSkill) == false {
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
	decision := decideRepository(action, repo.Allowed, dirtyOutput != "", ahead, behind)
	if !decision.pull {
		printSkills(stdout, repo.Skills, decision.message)
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
	if oldHead == newHead {
		printSkills(stdout, repo.Skills, "up to date")
	} else {
		printSkills(stdout, repo.Skills, fmt.Sprintf("updated (%s -> %s)", oldHead, newHead))
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

func printSkills(w io.Writer, skills []skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.set(skills, message)
		return
	}
	for _, item := range skills {
		fmt.Fprintf(w, "%s: %s\n", item.Name, message)
	}
}

func reportFailure(w io.Writer, item skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.failure(item, message)
		return
	}
	fmt.Fprintf(w, "%s: failed (%s)\n", item.Name, message)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func readSkill(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read skill: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read front matter: %w", err)
		}
		return "", errors.New("missing YAML front matter")
	}
	if scanner.Text() != "---" {
		return "", errors.New("missing YAML front matter")
	}
	values := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			name := values["name"]
			if name == "" {
				return "", errors.New("missing name")
			}
			if len(name) > 64 {
				return "", errors.New("name exceeds 64 characters")
			}
			if !skillName.MatchString(name) {
				return "", fmt.Errorf("invalid name %q: use lowercase letters, numbers, and hyphens", name)
			}
			if values["description"] == "" {
				return "", errors.New("missing description")
			}
			return name, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "name" || key == "description" {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read front matter: %w", err)
	}
	return "", errors.New("unterminated YAML front matter")
}

func selectSkills(all []skill, names []string) ([]skill, error) {
	byName := map[string][]skill{}
	for _, item := range all {
		byName[item.Name] = append(byName[item.Name], item)
	}
	var selected []skill
	for _, name := range names {
		matches := byName[name]
		if len(matches) == 0 {
			return nil, fmt.Errorf("skill not found: %s", name)
		}
		if len(matches) > 1 {
			var paths []string
			for _, match := range matches {
				paths = append(paths, match.Path)
			}
			return nil, fmt.Errorf("skill name is ambiguous: %s (%s)", name, strings.Join(paths, ", "))
		}
		selected = append(selected, matches[0])
	}
	return selected, nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  skillctl check [options] [skill...]
  skillctl update [options] [skill...]
  skillctl track [options] skill
  skillctl --version

Commands:
  check               check installed skills for updates
  update              safely update installed skills
  track               register the source of one copied skill

Options:
  --path PATH         replace configured roots; repeatable
  --config FILE       use a specific configuration file
  --timeout DURATION  set each network operation timeout, for example 30s
  --json              emit JSON for check or update
  --help              show this help

Track options:
  --source SOURCE     Git URL or local repository path
  --ref REF           branch, tag, or commit
  --skill-path PATH   repository-relative skill path

Options must appear before skill names.

Examples:
  skillctl check
  skillctl check --json
  skillctl update obsidian-assistant
  skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
`)
}
