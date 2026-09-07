package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Level   string `json:"level"`
}

type hostPlugin struct {
	BaselineDigest string    `json:"baselineDigest,omitempty"`
	ID             string    `json:"id"`
	Host           string    `json:"host"`
	Version        string    `json:"version"`
	Directory      string    `json:"directory"`
	Scope          string    `json:"scope"`
	Project        string    `json:"project,omitempty"`
	Enabled        bool      `json:"enabled"`
	Evidence       string    `json:"evidence"`
	Observed       time.Time `json:"observed"`
	Cached         bool      `json:"cached"`
}

type codexPluginList struct {
	Installed []struct {
		PluginID        string `json:"pluginId"`
		Name            string `json:"name"`
		MarketplaceName string `json:"marketplaceName"`
		Version         string `json:"version"`
		Installed       bool   `json:"installed"`
		Enabled         bool   `json:"enabled"`
	} `json:"installed"`
}

var readNativePluginList = func(ctx context.Context, host string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, host, "plugin", "list", "--json")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("host inventory timeout: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("%s plugin list failed: %w", host, err)
	}
	return out, nil
}

func discoverPlugins(ctx context.Context, opt options, refresh, persist bool) ([]hostPlugin, []diagnostic) {
	var plugins []hostPlugin
	var diagnostics []diagnostic
	if len(opt.Paths) > 0 || (opt.ConfigPath != "" && opt.Project == "") {
		return plugins, diagnostics
	}
	codex, issues := discoverCodexPlugins(ctx, refresh && !opt.Offline, persist)
	plugins, diagnostics = append(plugins, codex...), append(diagnostics, issues...)
	claude, issues := discoverClaudePlugins(projectDirectory(opt.Project))
	plugins, diagnostics = append(plugins, claude...), append(diagnostics, issues...)
	plugins, issues = applyPluginBaselines(plugins, persist)
	diagnostics = append(diagnostics, issues...)
	return plugins, diagnostics
}

func discoverCodexPlugins(ctx context.Context, refresh, persist bool) ([]hostPlugin, []diagnostic) {
	hostHome := codexHomePath()
	configPath := filepath.Join(hostHome, "config.toml")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	cachePath, err := stateFile(filepath.Join("hosts", "codex-plugins.json"))
	if err != nil {
		return nil, []diagnostic{{Code: "host_inventory_unavailable", Message: err.Error(), Level: "warning"}}
	}
	var cached struct {
		Plugins []hostPlugin `json:"plugins"`
	}
	data, cacheErr := os.ReadFile(cachePath)
	if cacheErr == nil {
		cacheErr = json.Unmarshal(data, &cached)
	}
	var issues []diagnostic
	if refresh {
		data, err := readNativePluginList(ctx, "codex")
		if err == nil {
			var raw map[string]any
			err = json.Unmarshal(data, &raw)
			if err == nil {
				if _, ok := raw["installed"]; !ok {
					err = fmt.Errorf("unsupported Codex plugin list schema")
				}
			}
			var result codexPluginList
			if err == nil {
				err = json.Unmarshal(data, &result)
			}
			if err == nil {
				cached.Plugins = nil
				for _, item := range result.Installed {
					if !item.Installed {
						continue
					}
					if !safeComponent(item.Name) || !safeComponent(item.MarketplaceName) || !safeComponent(item.Version) || item.PluginID != item.Name+"@"+item.MarketplaceName {
						issues = append(issues, diagnostic{Code: "invalid_host_plugin", Message: "invalid plugin identity in Codex inventory", Level: "warning"})
						continue
					}
					root := filepath.Join(hostHome, "plugins", "cache", item.MarketplaceName, item.Name, item.Version)
					cached.Plugins = append(cached.Plugins, hostPlugin{ID: item.PluginID, Host: "codex", Version: item.Version, Directory: root, Scope: "user", Enabled: item.Enabled, Evidence: "codex plugin list --json", Observed: time.Now().UTC()})
				}
				if persist {
					if err := saveJSON(cachePath, cached); err != nil {
						issues = append(issues, diagnostic{Code: "host_cache_write_failed", Message: err.Error(), Level: "warning"})
					}
				}
				return cached.Plugins, issues
			}
		}
		issues = append(issues, diagnostic{Code: "host_inventory_unavailable", Message: fmt.Sprint(err), Path: configPath, Level: "warning"})
	}
	if cacheErr != nil {
		if !errors.Is(cacheErr, os.ErrNotExist) {
			issues = append(issues, diagnostic{Code: "host_cache_invalid", Message: cacheErr.Error(), Path: cachePath, Level: "warning"})
		}
		issues = append(issues, diagnostic{Code: "host_inventory_not_cached", Message: "Codex plugin inventory is unavailable locally; run skillctl check to refresh it", Path: configPath, Level: "warning"})
		return nil, issues
	}
	var config struct {
		Plugins map[string]struct {
			Enabled *bool `toml:"enabled"`
		} `toml:"plugins"`
	}
	_, _ = toml.DecodeFile(configPath, &config)
	for i := range cached.Plugins {
		cached.Plugins[i].Cached = true
		if entry, ok := config.Plugins[cached.Plugins[i].ID]; ok && entry.Enabled != nil {
			cached.Plugins[i].Enabled = *entry.Enabled
		}
	}
	issues = append(issues, diagnostic{Code: "host_inventory_cached", Message: "Codex plugins are shown from the last verified host inventory", Path: cachePath, Level: "info"})
	return cached.Plugins, issues
}

func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`)
}

func discoverClaudePlugins(project string) ([]hostPlugin, []diagnostic) {
	home, _ := os.UserHomeDir()
	hostHome := cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
	path := filepath.Join(hostHome, "plugins", "installed_plugins.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []diagnostic{{Code: "host_inventory_unavailable", Message: err.Error(), Path: path, Level: "warning"}}
	}
	var index struct {
		Version int `json:"version"`
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
			Version     string `json:"version"`
			ProjectPath string `json:"projectPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &index); err != nil || index.Version != 2 {
		return nil, []diagnostic{{Code: "host_inventory_unsupported", Message: "unsupported Claude installed_plugins.json schema", Path: path, Level: "warning"}}
	}
	enabled := map[string]bool{}
	settingsPaths := []string{filepath.Join(hostHome, "settings.json")}
	if project != "" {
		settingsPaths = append(settingsPaths, filepath.Join(project, ".claude", "settings.json"), filepath.Join(project, ".claude", "settings.local.json"))
	}
	var issues []diagnostic
	for _, settingsPath := range settingsPaths {
		content, err := os.ReadFile(settingsPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var settings struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		}
		if err != nil {
			issues = append(issues, diagnostic{Code: "host_settings_unreadable", Message: err.Error(), Path: settingsPath, Level: "warning"})
			continue
		}
		if err := json.Unmarshal(content, &settings); err != nil {
			issues = append(issues, diagnostic{Code: "host_settings_invalid", Message: err.Error(), Path: settingsPath, Level: "warning"})
			continue
		}
		for id, value := range settings.EnabledPlugins {
			enabled[id] = value
		}
	}
	var plugins []hostPlugin
	for id, records := range index.Plugins {
		for _, record := range records {
			if record.Scope != "user" && record.Scope != "managed" && (project == "" || !samePath(project, record.ProjectPath)) {
				continue
			}
			if !filepath.IsAbs(record.InstallPath) {
				issues = append(issues, diagnostic{Code: "invalid_host_plugin", Message: "plugin installPath must be absolute", Path: path, Level: "warning"})
				continue
			}
			plugins = append(plugins, hostPlugin{ID: id, Host: "claude", Version: record.Version, Directory: record.InstallPath, Scope: record.Scope, Project: record.ProjectPath, Enabled: enabled[id], Evidence: path, Observed: time.Now().UTC()})
		}
	}
	slices.SortFunc(plugins, func(a, b hostPlugin) int { return strings.Compare(a.ID, b.ID) })
	return plugins, issues
}

func pluginRoots(plugins []hostPlugin) ([]scanRoot, []managedRoot, []diagnostic) {
	var roots []scanRoot
	var managed []managedRoot
	var issues []diagnostic
	for _, plugin := range plugins {
		if _, err := os.Stat(plugin.Directory); err != nil {
			issues = append(issues, diagnostic{Code: "plugin_content_unavailable", Message: "host-installed plugin content is not available locally", Path: plugin.Directory, Level: "warning"})
			continue
		}
		manifestDir := ".codex-plugin"
		if plugin.Host == "claude" {
			manifestDir = ".claude-plugin"
		}
		data, err := os.ReadFile(filepath.Join(plugin.Directory, manifestDir, "plugin.json"))
		var manifest struct {
			Skills any `json:"skills"`
		}
		paths := []string{"skills"}
		if err == nil {
			if err := json.Unmarshal(data, &manifest); err != nil {
				issues = append(issues, diagnostic{Code: "plugin_manifest_invalid", Message: err.Error(), Path: plugin.Directory, Level: "warning"})
				continue
			}
			switch value := manifest.Skills.(type) {
			case string:
				paths = append(paths, value)
			case []any:
				for _, item := range value {
					if path, ok := item.(string); ok {
						paths = append(paths, path)
					}
				}
			}
		}
		seen := map[string]bool{}
		for _, path := range paths {
			root := filepath.Join(plugin.Directory, filepath.FromSlash(path))
			if filepath.IsAbs(path) || !within(plugin.Directory, root) {
				issues = append(issues, diagnostic{Code: "plugin_path_unsupported", Message: "plugin skill path escapes its package", Path: root, Level: "warning"})
				continue
			}
			if seen[root] {
				continue
			}
			seen[root] = true
			roots = append(roots, scanRoot{Path: root, Host: plugin.Host, Scope: plugin.Scope, Project: plugin.Project})
		}
		managed = append(managed, managedRoot{Path: plugin.Directory, Owner: plugin.Host + " plugin " + plugin.ID})
	}
	return roots, managed, issues
}

// Baselines record observed host-owned bytes. They protect changes made after
// observation; host ownership alone is not treated as a content hash.
func applyPluginBaselines(plugins []hostPlugin, persist bool) ([]hostPlugin, []diagnostic) {
	path, err := stateFile(filepath.Join("hosts", "plugin-baselines.json"))
	if err != nil {
		return plugins, []diagnostic{{Code: "host_baseline_unavailable", Message: err.Error(), Level: "warning"}}
	}
	baselines := map[string]string{}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &baselines)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return plugins, []diagnostic{{Code: "host_baseline_invalid", Message: err.Error(), Level: "warning"}}
	}
	changed := false
	for i := range plugins {
		plugin := &plugins[i]
		key := stableID(plugin.Host, plugin.ID, plugin.Scope, plugin.Project, plugin.Version)
		if baseline, ok := baselines[key]; ok {
			plugin.BaselineDigest = baseline
			continue
		}
		if persist {
			if digest, err := hashDirectory(plugin.Directory); err == nil {
				baselines[key] = digest
				plugin.BaselineDigest = digest
				changed = true
			}
		}
	}
	if changed {
		if err := saveJSON(path, baselines); err != nil {
			return plugins, []diagnostic{{Code: "host_baseline_write_failed", Message: err.Error(), Level: "warning"}}
		}
	}
	return plugins, nil
}
