package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexInventoryUsesExactInstalledVersion(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	root := filepath.Join(home, ".codex")
	for _, version := range []string{"1.0", "9.0"} {
		writeTestSkill(t, filepath.Join(root, "plugins", "cache", "market", "demo-plugin", version, "skills", "demo"), "demo", version)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[plugins.'demo-plugin@market']\nenabled=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := readNativePluginList
	t.Cleanup(func() { readNativePluginList = previous })
	readNativePluginList = func(context.Context, string) ([]byte, error) {
		return []byte(`{"installed":[{"pluginId":"demo-plugin@market","name":"demo-plugin","marketplaceName":"market","version":"1.0","installed":true,"enabled":true}]}`), nil
	}
	checked := runV2(t, "", "check", "--host", "codex")
	found := false
	for _, asset := range checked.Items {
		if asset.Plugin != nil {
			found = true
			if asset.Plugin.Version != "1.0" || !strings.Contains(asset.Path, "1.0") {
				t.Fatalf("picked stale cache as installed: %#v", asset)
			}
		}
	}
	if !found {
		t.Fatal("plugin absent")
	}
	readNativePluginList = func(context.Context, string) ([]byte, error) { t.Fatal("list contacted host CLI"); return nil, nil }
	listed := runV2(t, "", "list", "--host", "codex")
	count := 0
	for _, asset := range listed.Items {
		if asset.Plugin != nil {
			count++
			if asset.Plugin.Version != "1.0" {
				t.Fatal("list scanned uninstalled cache version")
			}
		}
	}
	if count != 1 {
		t.Fatalf("plugin count=%d", count)
	}
	writeTestSkill(t, filepath.Join(root, "plugins", "cache", "market", "demo-plugin", "1.0", "skills", "demo"), "demo", "local edit")
	modified := runV2(t, "", "list", "demo", "--host", "codex")
	if len(modified.Items) != 1 || modified.Items[0].Drift != "modified" {
		t.Fatalf("plugin local edit not detected: %#v", modified.Items)
	}
}

func TestClaudeDisabledPluginScopeAndWholePackageOperation(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	directory := filepath.Join(home, ".claude", "plugins", "cache", "market", "demo", "1.0")
	writeTestSkill(t, filepath.Join(directory, "skills", "first"), "first", "one")
	writeTestSkill(t, filepath.Join(directory, "skills", "second"), "second", "two")
	if err := os.MkdirAll(filepath.Join(directory, "commands"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "commands", "command.md"), []byte("component"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"version": 2, "plugins": map[string]any{"demo@market": []any{map[string]any{"scope": "user", "installPath": directory, "version": "1.0"}}}}
	data, _ := json.Marshal(metadata)
	index := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.WriteFile(index, data, 0600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"enabledPlugins":{"demo@market":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	listed := runV2(t, "", "list", "--host", "claude")
	if len(listed.Items) != 2 || listed.Items[0].State != "disabled" {
		t.Fatalf("disabled plugin lost: %#v", listed.Items)
	}
	previous := executeNativePlugin
	t.Cleanup(func() { executeNativePlugin = previous })
	calls := 0
	executeNativePlugin = func(ctx context.Context, p hostPlugin, action string) error {
		calls++
		if action != "enable" || p.Scope != "user" {
			return fmt.Errorf("wrong native operation")
		}
		return os.WriteFile(settings, []byte(`{"enabledPlugins":{"demo@market":true}}`), 0600)
	}
	var out, errout strings.Builder
	if code := run([]string{"enable", "first", "--host", "claude", "--json-version", "2"}, &out, &errout); code != 1 || calls != 0 {
		t.Fatalf("missing --package accepted: %d %s %s", code, &out, &errout)
	}
	if !strings.Contains(out.String(), "second") || !strings.Contains(out.String(), "command.md") {
		t.Fatalf("package effects omitted: %s", &out)
	}
	result := runV2(t, "", "enable", "first", "--host", "claude", "--package")
	if len(result.Items) != 1 || result.Items[0].State != "managed" {
		t.Fatalf("stale command inventory: %#v", result.Items)
	}
	if calls != 1 || result.Operation == nil || !result.Operation.Native.Verified {
		t.Fatalf("native result not verified: %#v", result)
	}
	enabled := runV2(t, "", "list", "--host", "claude")
	for _, asset := range enabled.Items {
		if asset.State != "managed" {
			t.Fatalf("wrong state: %s", asset.State)
		}
	}
}

func TestCodexUnsupportedInventorySchemaIsDiagnostic(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	previous := readNativePluginList
	t.Cleanup(func() { readNativePluginList = previous })
	readNativePluginList = func(context.Context, string) ([]byte, error) { return []byte(`{"plugins":[]}`), nil }
	result := runV2(t, "", "check", "--host", "codex")
	found := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "unsupported") {
			found = true
		}
	}
	if !found {
		t.Fatalf("schema mismatch silently ignored: %#v", result)
	}
}
