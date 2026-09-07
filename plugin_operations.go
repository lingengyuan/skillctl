package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
	"uuid"
)

type nativeOperation struct {
	Host     string      `json:"host"`
	Action   string      `json:"action"`
	Before   hostPlugin  `json:"before"`
	After    *hostPlugin `json:"after,omitempty"`
	Verified bool        `json:"verified"`
}

var executeNativePlugin = func(ctx context.Context, plugin hostPlugin, action string) error {
	args := []string{"plugin", action, plugin.ID}
	if plugin.Host == "codex" {
		args = append(args, "--json")
	} else {
		if action == "remove" {
			args[1] = "uninstall"
			args = append(args, "--keep-data")
		}
		args = append(args, "--scope", plugin.Scope)
	}
	cmd := exec.CommandContext(ctx, plugin.Host, args...)
	if plugin.Project != "" {
		cmd.Dir = plugin.Project
	}
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("native operation timeout: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s plugin %s failed: %s", plugin.Host, action, oneLine(string(output)))
	}
	return nil
}

func runPluginOperation(ctx context.Context, command string, view *inventoryView, selected []skillAsset, opt options, stdout, stderr io.Writer) int {
	result := commandResult{Command: command, Items: selected, Plan: &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}}
	fail := func(code string, err error) int {
		result.Diagnostics = append(result.Diagnostics, diagnostic{Code: code, Message: err.Error(), Level: "error"})
		return outputResult(result, opt, stdout, stderr, true)
	}
	plugins := []hostPlugin{}
	seen := map[string]bool{}
	for _, asset := range selected {
		if asset.Plugin == nil {
			return fail("mixed_operation_units", fmt.Errorf("select plugin skills separately from standalone skills"))
		}
		plugin := *asset.Plugin
		key := plugin.Host + "\x00" + plugin.ID + "\x00" + plugin.Scope + "\x00" + plugin.Project
		if seen[key] {
			continue
		}
		seen[key] = true
		if !asset.Capabilities[command].Supported {
			return fail("host_operation_unsupported", fmt.Errorf("%s: %s", plugin.ID, asset.Capabilities[command].Reason))
		}
		if plugin.Scope == "managed" {
			return fail("host_policy_managed", fmt.Errorf("%s is installed by host policy", plugin.ID))
		}
		plugins = append(plugins, plugin)
		affected, err := pluginAffected(plugin)
		if err != nil {
			return fail("plugin_content_unavailable", err)
		}
		result.Plan.Affected = append(result.Plan.Affected, affected...)
		result.Plan.Changes = append(result.Plan.Changes, plannedChange{Path: plugin.Directory, Action: "native-" + command, Target: plugin.ID})
	}
	sortedAffected(result.Plan)
	if !opt.Package {
		return fail("package_scope_required", fmt.Errorf("this command affects entire plugin packages and all listed components; use --package"))
	}
	if opt.DryRun {
		return outputResult(result, opt, stdout, stderr, false)
	}
	for _, plugin := range plugins {
		if plugin.Cached {
			current, err := verifiedNativePlugin(ctx, plugin)
			if err != nil {
				return fail("host_inventory_unavailable", err)
			}
			if current == nil || current.Version != plugin.Version {
				return fail("host_state_changed", fmt.Errorf("native package changed after planning: %s", plugin.ID))
			}
			plugin = *current
		}
		if command == "enable" && plugin.Enabled || command == "disable" && !plugin.Enabled {
			continue
		}
		history, err := stateFile("history")
		if err != nil {
			return fail("history_failed", err)
		}
		id := uuid.NewV7().String()
		operation := &operationRecord{SchemaVersion: 1, ID: id, Command: command, Created: time.Now().UTC(), State: "native_applying", Affected: result.Plan.Affected, Native: &nativeOperation{Host: plugin.Host, Action: command, Before: plugin}, directory: filepath.Join(history, id)}
		before, err := fingerprint(plugin.Directory)
		if err != nil {
			return fail("native_backup_failed", err)
		}
		snapshot := filepath.Join(operation.directory, "native-before")
		if err := copySnapshot(plugin.Directory, snapshot); err != nil {
			return fail("native_backup_failed", err)
		}
		if saved, err := fingerprint(snapshot); err != nil || saved != before {
			return fail("native_backup_failed", fmt.Errorf("native package backup does not match %s", plugin.ID))
		}
		if current, err := fingerprint(plugin.Directory); err != nil || current != before {
			return fail("native_state_changed", fmt.Errorf("native package changed during backup: %s", plugin.ID))
		}
		if err := operation.save(); err != nil {
			return fail("history_failed", err)
		}
		opctx, cancel := context.WithTimeout(ctx, view.timeout)
		execErr := executeNativePlugin(opctx, plugin, command)
		cancel()
		verifyCtx, verifyCancel := context.WithTimeout(ctx, view.timeout)
		after, verifyErr := verifiedNativePlugin(verifyCtx, plugin)
		verifyCancel()
		operation.Native.After = after
		if verifyErr == nil && nativeResultMatches(operation.Native) {
			operation.State, operation.Native.Verified = "committed", true
		} else if verifyErr == nil && after != nil && nativeSameState(plugin, *after) {
			operation.State = "rolled_back"
		} else {
			operation.State = "recovery_required"
		}
		if execErr != nil {
			operation.Error = execErr.Error()
		}
		if verifyErr != nil {
			operation.Error = errors.Join(execErr, verifyErr).Error()
		}
		if err := operation.save(); err != nil {
			return fail("history_failed", err)
		}
		result.Operation = operation
		result.Operations = append(result.Operations, *operation)
		if operation.State != "committed" {
			return fail("native_operation_unverified", fmt.Errorf("operation %s is %s: %s", operation.ID, operation.State, operation.Error))
		}
	}
	fresh, err := loadInventory(ctx, opt, false, true)
	if err != nil {
		return fail("native_inventory_failed", err)
	}
	result.Items = []skillAsset{}
	for _, asset := range fresh.Items {
		for _, prior := range selected {
			if asset.ID == prior.ID {
				result.Items = append(result.Items, asset)
				break
			}
		}
	}
	return outputResult(result, opt, stdout, stderr, false)
}

func pluginAffected(plugin hostPlugin) ([]string, error) {
	result := []string{plugin.Host + " plugin " + plugin.ID + " (" + plugin.Scope + ")"}
	entries, err := os.ReadDir(plugin.Directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(plugin.Directory, entry.Name())
		if entry.IsDir() {
			children, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			if len(children) == 0 {
				result = append(result, path+"/")
			}
			for _, child := range children {
				result = append(result, filepath.Join(path, child.Name()))
			}
		} else {
			result = append(result, path)
		}
	}
	slices.Sort(result)
	return result, nil
}

func verifiedNativePlugin(ctx context.Context, previous hostPlugin) (*hostPlugin, error) {
	var plugins []hostPlugin
	var diagnostics []diagnostic
	if previous.Host == "codex" {
		plugins, diagnostics = discoverCodexPlugins(ctx, true, true)
	} else {
		plugins, diagnostics = discoverClaudePlugins(previous.Project)
	}
	for _, d := range diagnostics {
		if d.Level == "error" || d.Level == "warning" {
			return nil, fmt.Errorf("%s: %s", d.Code, d.Message)
		}
	}
	for _, plugin := range plugins {
		if plugin.ID == previous.ID && plugin.Scope == previous.Scope && plugin.Project == previous.Project {
			if plugin.Cached {
				return nil, fmt.Errorf("native inventory could not be refreshed")
			}
			info, err := os.Stat(plugin.Directory)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("native package directory is unavailable: %s", plugin.Directory)
			}
			if _, _, issues := pluginRoots([]hostPlugin{plugin}); len(issues) > 0 {
				return nil, fmt.Errorf("native package verification: %s", issues[0].Message)
			}
			if _, err := hashDirectory(plugin.Directory); err != nil {
				return nil, fmt.Errorf("native package content: %w", err)
			}
			return new(plugin), nil
		}
	}
	return nil, nil
}

func nativeSameState(a, b hostPlugin) bool {
	return a.ID == b.ID && a.Version == b.Version && a.Enabled == b.Enabled && samePath(a.Directory, b.Directory)
}

func nativeResultMatches(operation *nativeOperation) bool {
	after := operation.After
	switch operation.Action {
	case "remove":
		return after == nil
	case "enable":
		return after != nil && after.Enabled
	case "disable":
		return after != nil && !after.Enabled
	case "update":
		return after != nil && after.ID == operation.Before.ID && after.Version != ""
	}
	return false
}

func rollbackNative(ctx context.Context, id string, opt options, stdout, stderr io.Writer) (int, bool) {
	operations, err := readOperations()
	if err != nil {
		return 0, false
	}
	for _, record := range operations {
		if id != "latest" && record.ID != id {
			continue
		}
		if record.State != "committed" {
			if id == "latest" {
				continue
			}
			return 0, false
		}
		if record.Native == nil {
			return 0, false
		}
		before := record.Native.Before
		if record.Native.Action != "enable" && record.Native.Action != "disable" {
			return outputResult(commandResult{Command: "rollback", Diagnostics: []diagnostic{{Code: "native_rollback_unsupported", Level: "error", Message: "host CLI cannot restore an exact plugin version; history retains the previous identity and revision"}}}, opt, stdout, stderr, true), true
		}
		current, err := verifiedNativePlugin(ctx, before)
		if err != nil || current == nil || record.Native.After == nil || !nativeSameState(*current, *record.Native.After) {
			return outputResult(commandResult{Command: "rollback", Diagnostics: []diagnostic{{Code: "native_state_changed", Level: "error", Message: "native package changed since this operation"}}}, opt, stdout, stderr, true), true
		}
		asset := skillAsset{ID: "plugin-" + before.ID, Name: before.ID, Plugin: current, State: "managed", Owner: before.Host, Provider: "host-plugin"}
		asset.Capabilities = assetCapabilities(asset)
		command := "disable"
		if before.Enabled {
			command = "enable"
		}
		opt.Package = true
		return runPluginOperation(ctx, command, &inventoryView{timeout: defaultNetworkTimeout}, []skillAsset{asset}, opt, stdout, stderr), true
	}
	return 0, false
}

func recoverNative(r *operationRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultNetworkTimeout)
	defer cancel()
	after, err := verifiedNativePlugin(ctx, r.Native.Before)
	if err != nil {
		return err
	}
	r.Native.After = after
	if nativeResultMatches(r.Native) {
		r.State, r.Native.Verified = "committed", true
	} else if after != nil && nativeSameState(r.Native.Before, *after) {
		r.State = "rolled_back"
	} else {
		return fmt.Errorf("native package state requires attention: %s", r.Native.Before.ID)
	}
	return r.save()
}
