package main

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type agentHost struct {
	Name        string
	UserPath    string
	ProjectPath string
}

func canonicalHost(name string) string {
	switch strings.ToLower(name) {
	case "claude-code":
		return "claude"
	case "github-copilot":
		return "copilot"
	case "gemini-cli":
		return "gemini"
	default:
		return strings.ToLower(name)
	}
}

func agentHosts() []agentHost {
	home, _ := os.UserHomeDir()
	xdg := cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	return []agentHost{
		{"codex", filepath.Join(home, ".agents", "skills"), ".agents/skills"},
		{"claude", filepath.Join(cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")), "skills"), ".claude/skills"},
		{"cursor", filepath.Join(home, ".cursor", "skills"), ".cursor/skills"},
		{"copilot", filepath.Join(home, ".copilot", "skills"), ".github/skills"},
		{"gemini", filepath.Join(home, ".gemini", "skills"), ".gemini/skills"},
		{"opencode", filepath.Join(xdg, "opencode", "skills"), ".opencode/skills"},
	}
}

func projectDirectory(explicit string) string {
	if explicit != "" {
		return resolvePath(explicit, ".")
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		if _, err := os.Stat(filepath.Join(current, "skillctl.toml")); err == nil {
			return current
		}
		if filepath.Dir(current) == current {
			return dir
		}
	}
}

func projectRoots(project string) []scanRoot {
	if project == "" {
		return nil
	}
	var roots []scanRoot
	for _, host := range agentHosts() {
		paths := []string{host.ProjectPath}
		// These hosts also discover the shared project directory. Treat it as
		// multiple bindings to the same content rather than a seventh host.
		if host.Name != "claude" && host.ProjectPath != ".agents/skills" {
			paths = append(paths, ".agents/skills")
		}
		if host.Name == "codex" || host.Name == "cursor" {
			paths = append(paths, ".codex/skills")
		}
		if host.Name == "cursor" || host.Name == "copilot" || host.Name == "opencode" {
			paths = append(paths, ".claude/skills")
		}
		for _, path := range paths {
			roots = append(roots, scanRoot{Path: filepath.Join(project, filepath.FromSlash(path)), Host: host.Name, Scope: "project", Project: project})
		}
	}
	return roots
}

func augmentRoots(roots []scanRoot, opt options) []scanRoot {
	if len(opt.Paths) > 0 {
		var result []scanRoot
		for _, path := range opt.Paths {
			result = append(result, scanRoot{Path: resolvePath(path, "."), Host: "manual", Scope: "local", Required: true})
		}
		return result
	}
	// Explicit config files keep their discovery boundary unless --project is
	// requested. Default configuration includes the current project.
	if opt.ConfigPath == "" {
		home, _ := os.UserHomeDir()
		for _, host := range []string{"codex", "cursor", "copilot", "gemini", "opencode"} {
			roots = append(roots, scanRoot{Path: filepath.Join(home, ".agents", "skills"), Host: host, Scope: "user"})
		}
		for _, host := range []string{"cursor", "opencode"} {
			roots = append(roots, scanRoot{Path: filepath.Join(home, ".claude", "skills"), Host: host, Scope: "user"})
		}
		roots = append(roots, scanRoot{Path: filepath.Join(home, ".codex", "skills"), Host: "cursor", Scope: "user"})
	}
	if opt.ConfigPath == "" || opt.Project != "" {
		project := projectDirectory(opt.Project)
		roots = append(slices.Clone(roots), projectRoots(project)...)
		if cwd, err := os.Getwd(); err == nil && opt.Project == "" && within(project, cwd) {
			for dir := cwd; !samePath(dir, project); dir = filepath.Dir(dir) {
				for _, root := range projectRoots(dir) {
					root.Project = project
					roots = append(roots, root)
				}
			}
		}
	}
	for i := range roots {
		roots[i].Host = canonicalHost(roots[i].Host)
	}
	return roots
}
