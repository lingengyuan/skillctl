package main

import (
	"bufio"
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

	"github.com/BurntSushi/toml"
)

var version = "0.1.0"

var defaultConfig = `# Directories are scanned recursively for SKILL.md files.
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
	Paths []string `toml:"paths"`
}

type skill struct {
	Name     string
	Path     string
	ScanRoot string
}

type options struct {
	Paths      []string
	ConfigPath string
	Names      []string
	Help       bool
	Source     string
	Ref        string
	SkillPath  string
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
	paths := opt.Paths
	ignoreMissing := false
	if len(paths) == 0 {
		var err error
		paths, ignoreMissing, err = loadConfig(opt.ConfigPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}

	skills, scanFailed := scan(paths, ignoreMissing, stderr)
	if len(opt.Names) > 0 {
		var err error
		skills, err = selectSkills(skills, opt.Names)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if len(skills) == 0 {
		fmt.Fprintln(stdout, "No skills found.")
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
	if args[0] == "track" {
		if len(skills) != 1 {
			fmt.Fprintln(stderr, "track requires exactly one unambiguous skill")
			return 1
		}
		if err := trackCopiedSkill(skills[0], opt.Source, opt.Ref, opt.SkillPath, state); err != nil {
			fmt.Fprintf(stderr, "%s: failed (%s)\n", skills[0].Name, oneLine(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "%s: tracked\n", skills[0].Name)
		return 0
	}
	gitFailed := processGit(args[0], skills, state, stdout, stderr)
	if scanFailed || gitFailed {
		return 1
	}
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

func loadConfig(explicit string) ([]string, bool, error) {
	path := explicit
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, false, fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(dir, "skillctl", "config.toml")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && explicit == "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, false, fmt.Errorf("create config directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(defaultConfig), 0o644); err != nil {
			return nil, false, fmt.Errorf("create default config: %w", err)
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("read config: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read config: %w", err)
	}
	var cfg config
	meta, err := toml.Decode(string(content), &cfg)
	if err != nil {
		return nil, false, fmt.Errorf("invalid config: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, false, fmt.Errorf("invalid config: unknown field %q", undecoded[0])
	}
	base := filepath.Dir(path)
	for i := range cfg.Paths {
		cfg.Paths[i] = resolvePath(cfg.Paths[i], base)
	}
	return cfg.Paths, string(content) == defaultConfig, nil
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

func scan(paths []string, ignoreMissing bool, stderr io.Writer) ([]skill, bool) {
	seen := &fileIdentitySet{}
	visitedDirs := &fileIdentitySet{}
	var skills []skill
	failed := false
	for _, root := range paths {
		root = resolvePath(root, ".")
		rootInfo, err := os.Stat(root)
		if err != nil {
			if ignoreMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			fmt.Fprintf(stderr, "%s: %v\n", root, err)
			failed = true
			continue
		}
		if visitedDirs.contains(rootInfo) {
			continue
		}
		realRoot, err := filepath.Abs(root)
		if err != nil {
			realRoot = root
		}
		err = walkFollowingLinks(root, visitedDirs, stderr, func(path string) error {
			dir := filepath.Dir(path)
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if seen.contains(info) {
				return nil
			}
			real, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			name, valid := readSkill(path, filepath.Base(real))
			if !valid {
				fmt.Fprintf(stderr, "%s: skipped (invalid skill)\n", path)
				return nil
			}
			seen.add(info)
			skills = append(skills, skill{Name: name, Path: real, ScanRoot: realRoot})
			return nil
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

type fileIdentitySet struct {
	items []os.FileInfo
}

func (s *fileIdentitySet) contains(info os.FileInfo) bool {
	for _, item := range s.items {
		if os.SameFile(item, info) {
			return true
		}
	}
	return false
}

func (s *fileIdentitySet) add(info os.FileInfo) {
	s.items = append(s.items, info)
}

func walkFollowingLinks(dir string, visited *fileIdentitySet, stderr io.Writer, visitSkill func(string) error) error {
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if visited.contains(dirInfo) {
		return nil
	}
	visited.add(dirInfo)
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
				fmt.Fprintf(stderr, "%s: skipped (broken link or missing target)\n", path)
				continue
			}
			return err
		}
		if info.IsDir() {
			if err := walkFollowingLinks(path, visited, stderr, visitSkill); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(stderr, "%s: skipped (broken link or missing target)\n", path)
					continue
				}
				return err
			}
			continue
		}
		if entry.Name() == "SKILL.md" {
			if err := visitSkill(path); err != nil {
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

func processGit(action string, skills []skill, state *trackedState, stdout, stderr io.Writer) bool {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(stderr, "git was not found in PATH")
		return true
	}
	repos := map[string]*repository{}
	var copied []skill
	failed := false
	for _, item := range skills {
		if _, tracked := state.find(item.Path); tracked {
			copied = append(copied, item)
			continue
		}
		root, err := gitOutput(item.Path, "rev-parse", "--show-toplevel")
		if err != nil {
			copied = append(copied, item)
			continue
		}
		root = filepath.Clean(root)
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
		if processRepository(action, repos[root], stdout, stderr) {
			failed = true
		}
	}
	if processTracked(action, copied, state, stdout, stderr) {
		failed = true
	}
	return failed
}

func processRepository(action string, repo *repository, stdout, stderr io.Writer) bool {
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
	if _, err := gitOutput(repo.Root, "fetch", "--prune", "--recurse-submodules=no", remote); err != nil {
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
	if dirtyOutput != "" {
		if behind > 0 {
			printSkills(stdout, repo.Skills, fmt.Sprintf("update available (behind %d commits), skipped (working tree is dirty)", behind))
		} else {
			printSkills(stdout, repo.Skills, "skipped (working tree is dirty)")
		}
		return false
	}
	if ahead > 0 && behind > 0 {
		printSkills(stdout, repo.Skills, "skipped (branch has diverged)")
		return false
	}
	if ahead > 0 {
		printSkills(stdout, repo.Skills, fmt.Sprintf("skipped (ahead by %d commits)", ahead))
		return false
	}
	if behind == 0 {
		printSkills(stdout, repo.Skills, "up to date")
		return false
	}
	if action == "check" {
		printSkills(stdout, repo.Skills, fmt.Sprintf("update available (behind %d commits)", behind))
		return false
	}
	if !repo.Allowed {
		printSkills(stdout, repo.Skills, "skipped (repository root is outside the scan path)")
		return false
	}
	oldHead, _ := gitOutput(repo.Root, "rev-parse", "--short", "HEAD")
	if _, err := gitOutput(repo.Root, "-c", "submodule.recurse=false", "pull", "--ff-only", "--no-rebase", "--recurse-submodules=no"); err != nil {
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

func printSkills(w io.Writer, skills []skill, message string) {
	for _, item := range skills {
		fmt.Fprintf(w, "%s: %s\n", item.Name, message)
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func readSkill(path, _ string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || scanner.Text() != "---" {
		return "", false
	}
	values := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			name := values["name"]
			return name, len(name) <= 64 && skillName.MatchString(name) && values["description"] != ""
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
	return "", false
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
	fmt.Fprintln(w, "Usage: skillctl <check|update|track> [options] [skill...]")
}
