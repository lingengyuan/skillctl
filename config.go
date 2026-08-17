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

[[roots]]
path = "~/.trae-cn/skills"
host = "trae-cn"
scope = "user"

[[roots]]
path = "~/.ghcp-appmod/skills"
host = "ghcp-appmod"
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
