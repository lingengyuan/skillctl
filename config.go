package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var defaultConfig = `# Directories are scanned recursively for SKILL.md files.
# Relative paths are resolved from this file. Missing roots are skipped unless
# required = true is set explicitly.

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

[[roots]]
path = "~/.trae-cn/skills"
host = "trae-cn"
scope = "user"

[[roots]]
path = "~/.ghcp-appmod/skills"
host = "ghcp-appmod"
scope = "user"

[[roots]]
path = "~/.trae/skills"
host = "trae"
scope = "user"

[[roots]]
path = "~/.codebuddy/skills"
host = "codebuddy"
scope = "user"

[[roots]]
path = "~/.kiro/skills"
host = "kiro"
scope = "user"

[[roots]]
path = "~/.qoder/skills"
host = "qoder"
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
	Path     string `toml:"path"`
	Host     string `toml:"host"`
	Scope    string `toml:"scope"`
	Required bool   `toml:"required"`
	Project  string `toml:"project"`
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

func loadConfig(explicit string) ([]scanRoot, []manifest, []managedRoot, time.Duration, error) {
	path := explicit
	if path == "" {
		dir, err := skillctlDirectory()
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(dir, "config.toml")
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && explicit == "" {
		content = []byte(defaultConfig)
	} else if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("read config: %w", err)
	}
	if string(content) == legacyDefaultConfig {
		content = []byte(defaultConfig)
	}
	var cfg config
	meta, err := toml.Decode(string(content), &cfg)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("invalid config: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, nil, nil, 0, fmt.Errorf("invalid config: unknown field %q", undecoded[0])
	}
	networkTimeout := defaultNetworkTimeout
	if cfg.NetworkTimeout != "" {
		networkTimeout, err = time.ParseDuration(cfg.NetworkTimeout)
		if err != nil || networkTimeout <= 0 {
			return nil, nil, nil, 0, fmt.Errorf("invalid config: network_timeout must be a positive duration")
		}
	}
	base := filepath.Dir(path)
	for _, legacyPath := range cfg.Paths {
		cfg.Roots = append(cfg.Roots, scanRoot{Path: legacyPath, Host: "legacy", Scope: "user"})
	}
	if len(cfg.Roots) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("invalid config: at least one root is required")
	}
	for i := range cfg.Roots {
		if cfg.Roots[i].Path == "" || cfg.Roots[i].Host == "" || cfg.Roots[i].Scope == "" {
			return nil, nil, nil, 0, fmt.Errorf("invalid config: roots require path, host, and scope")
		}
		if cfg.Roots[i].Path == "~/.codex/skills" {
			cfg.Roots[i].Path = filepath.Join(codexHomePath(), "skills")
		}
		if cfg.Roots[i].Path == "~/.claude/skills" && os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			cfg.Roots[i].Path = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "skills")
		}
		cfg.Roots[i].Path = resolvePath(cfg.Roots[i].Path, base)
		if cfg.Roots[i].Project != "" {
			cfg.Roots[i].Project = resolvePath(cfg.Roots[i].Project, base)
		}
	}
	if len(cfg.Manifests) == 0 {
		if lockPath, lockErr := activeVercelLockPath(); lockErr == nil {
			cfg.Manifests = append(cfg.Manifests, manifest{Kind: "vercel-skills-lock-v3", Path: lockPath, InstallRoot: "~/.agents/skills"})
		}
	}
	for i := range cfg.Manifests {
		if cfg.Manifests[i].Kind != "vercel-skills-lock-v3" {
			return nil, nil, nil, 0, fmt.Errorf("invalid config: unsupported manifest kind %q", cfg.Manifests[i].Kind)
		}
		if cfg.Manifests[i].Path == "" || cfg.Manifests[i].InstallRoot == "" {
			return nil, nil, nil, 0, fmt.Errorf("invalid config: manifests require path and install_root")
		}
		if isDefaultVercelLockPath(cfg.Manifests[i].Path) {
			if lockPath, lockErr := activeVercelLockPath(); lockErr == nil {
				cfg.Manifests[i].Path = lockPath
			}
		}
		cfg.Manifests[i].Path = resolvePath(cfg.Manifests[i].Path, base)
		cfg.Manifests[i].InstallRoot = resolvePath(cfg.Manifests[i].InstallRoot, base)
	}
	for i := range cfg.ManagedRoots {
		if cfg.ManagedRoots[i].Path == "" || cfg.ManagedRoots[i].Owner == "" {
			return nil, nil, nil, 0, fmt.Errorf("invalid config: managed_roots require path and owner")
		}
		if cfg.ManagedRoots[i].Path == "~/.codex/skills/.system" {
			cfg.ManagedRoots[i].Path = filepath.Join(codexHomePath(), "skills", ".system")
		}
		cfg.ManagedRoots[i].Path = resolvePath(cfg.ManagedRoots[i].Path, base)
	}

	return cfg.Roots, cfg.Manifests, cfg.ManagedRoots, networkTimeout, nil
}

// SKILLCTL_HOME isolates all persistent skillctl state, including its shared
// store. Host configuration and credentials remain owned by their host.
func skillctlDirectory() (string, error) {
	if path := os.Getenv("SKILLCTL_HOME"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skillctl"), nil
}

func isDefaultVercelLockPath(value string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	return value == "~/.agents/.skill-lock.json"
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
