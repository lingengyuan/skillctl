package app

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type hostObservationContextKey struct{}
type hostObservation struct {
	plugins     []hostPlugin
	diagnostics []diagnostic
	observation *observation
}

type checkObservation struct {
	initial        trackedState
	plugins        []hostPlugin
	hostCache      string
	hostFiles      map[string]string
	codexRefreshed bool
}

// Host/network work happens before the snapshot lock. The critical section
// reads local state and bytes only, and never recovers an installation journal.
func loadCheckInventory(ctx context.Context, opt options) (*inventoryView, error) {
	_, _, _, timeout, err := loadConfig(opt.ConfigPath)
	if err != nil {
		return nil, err
	}
	if opt.Timeout > 0 {
		timeout = opt.Timeout
	}
	hostCache, err := stateFile(filepath.Join("hosts", "codex-plugins.json"))
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	hostFiles := map[string]string{}
	for _, path := range []string{hostCache, filepath.Join(codexHomePath(), "config.toml"), filepath.Join(cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")), "plugins", "installed_plugins.json")} {
		if digest, err := fingerprint(path); err == nil {
			hostFiles[path] = digest
		}
	}
	hostCtx, cancel := context.WithTimeout(ctx, timeout)
	plugins, diagnostics := discoverPlugins(hostCtx, opt, !opt.Offline, false)
	cancel()
	lock, err := acquireCommandLock()
	if err != nil {
		return nil, err
	}
	defer lock.release()
	operations, operationErr := readOperations()
	if operationErr == nil {
		operationErr = validateObservationOperations(operations)
	}
	observed := newObservation(ctx)
	for i := range plugins {
		plugin := &plugins[i]
		if plugin.BaselineDigest != "" || operationErr != nil || pluginHasPendingOperation(*plugin, operations) {
			continue
		}
		if digest, err := observed.hash(plugin.Directory); err == nil {
			plugin.BaselineDigest = digest
		}
	}
	hosts := &hostObservation{plugins: plugins, diagnostics: diagnostics, observation: observed}
	view, err := loadInventory(context.WithValue(ctx, hostObservationContextKey{}, hosts), opt, false, false)
	if err != nil {
		return nil, err
	}
	initial := *view.state
	initial.Skills = slices.Clone(view.state.Skills)
	initial.ProviderBaselines = slices.Clone(view.state.ProviderBaselines)
	view.checkObservation = &checkObservation{initial: initial, plugins: plugins, hostCache: hostCache, hostFiles: hostFiles}
	_, configErr := os.Stat(filepath.Join(codexHomePath(), "config.toml"))
	view.checkObservation.codexRefreshed = configErr == nil && !opt.Offline && len(opt.Paths) == 0 && (opt.ConfigPath == "" || opt.Project != "")
	for _, d := range diagnostics {
		if d.Code == "host_inventory_unavailable" {
			view.checkObservation.codexRefreshed = false
		}
	}
	markPendingOperations(view, operations, operationErr)
	return view, nil
}

func validateObservationOperations(operations []operationRecord) error {
	for _, operation := range operations {
		if !slices.Contains([]string{"preparing", "prepared", "applying", "external_applying", "native_applying", "recovery_required", "committed", "no_change", "rolled_back"}, operation.State) {
			return fmt.Errorf("operation %s has an unknown state", operation.ID)
		}
		if operationPending(operation) && len(operation.Steps) == 0 && (operation.Native == nil || operation.Native.Before.Directory == "") {
			return fmt.Errorf("unfinished operation %s has no verifiable target scope", operation.ID)
		}
	}
	return nil
}

func operationPending(operation operationRecord) bool {
	return slices.Contains([]string{"prepared", "applying", "external_applying", "native_applying", "recovery_required"}, operation.State)
}

func operationAffects(operation operationRecord, path string) bool {
	for _, step := range operation.Steps {
		if fsutil.Within(step.Path, path) || fsutil.Within(path, step.Path) {
			return true
		}
	}
	if operation.Native != nil {
		root := operation.Native.Before.Directory
		if root != "" && (fsutil.Within(root, path) || fsutil.Within(path, root)) {
			return true
		}
	}
	return false
}

func pluginHasPendingOperation(plugin hostPlugin, operations []operationRecord) bool {
	for _, operation := range operations {
		if operationPending(operation) && operationAffects(operation, plugin.Directory) {
			return true
		}
	}
	return false
}

func markPendingOperations(view *inventoryView, operations []operationRecord, readErr error) {
	if readErr != nil {
		view.Diagnostics = append(view.Diagnostics, diagnostic{Code: "operation_state_unknown", Level: "error", Message: readErr.Error()})
	}
	for i := range view.Items {
		asset := &view.Items[i]
		reason := ""
		if readErr != nil {
			reason = "operation_state_unknown"
		}
		for _, operation := range operations {
			if !operationPending(operation) {
				continue
			}
			matched := operationAffects(operation, asset.Path)
			for _, binding := range asset.Bindings {
				matched = matched || operationAffects(operation, binding.Path)
			}
			if !matched {
				continue
			}
			reason = "recovery_required"
			if operation.State == "prepared" {
				reason = "pending_operation"
			}
			view.Diagnostics = append(view.Diagnostics, diagnostic{Code: reason, Level: "error", Path: asset.Path, Message: fmt.Sprintf("operation %s is %s; check does not recover installation transactions", operation.ID, operation.State)})
		}
		if reason != "" {
			asset.State, asset.ReasonCode, asset.Drift = "blocked", reason, "indeterminate"
			asset.Upstream = upstreamObservation{Status: "not-checked", Reason: reason}
			*asset = completeAsset(*asset)
		}
	}
}

// Verified observations merge under a fresh short lock. A concurrent writer,
// changed baseline or changed installation leaves the observation unpersisted.
func commitCheckObservations(ctx context.Context, view *inventoryView) []diagnostic {
	observation := view.checkObservation
	if observation == nil {
		return nil
	}
	warning := func(path string, err error) diagnostic {
		return diagnostic{Code: "observation_not_saved", Level: "warning", Path: path, Message: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return []diagnostic{warning("", err)}
	}
	lock, err := acquireCommandLock()
	if err != nil {
		return []diagnostic{warning("", err)}
	}
	defer lock.release()
	operations, err := readOperations()
	if err == nil {
		err = validateObservationOperations(operations)
	}
	if err != nil {
		return []diagnostic{warning("", err)}
	}
	current, err := loadTrackedState()
	if err != nil {
		return []diagnostic{warning("", err)}
	}
	var diagnostics []diagnostic
	changed := false
	for _, entry := range view.state.Skills {
		before, hadBefore := observation.initial.find(entry.Path)
		if hadBefore && *before == entry {
			continue
		}
		latest, hasLatest := current.find(entry.Path)
		if hadBefore != hasLatest || hadBefore && *latest != *before {
			diagnostics = append(diagnostics, warning(entry.Path, fmt.Errorf("source state changed during check")))
			continue
		}
		blocked := false
		for _, operation := range operations {
			blocked = blocked || operationPending(operation) && operationAffects(operation, entry.Path)
		}
		if blocked {
			diagnostics = append(diagnostics, warning(entry.Path, fmt.Errorf("installation has an unfinished operation")))
			continue
		}
		digest, err := fsutil.HashDirectoryContext(ctx, entry.Path)
		if err != nil || digest != entry.InstalledHash {
			diagnostics = append(diagnostics, warning(entry.Path, fmt.Errorf("installed content changed during check")))
			continue
		}
		current.put(entry)
		changed = true
	}
	for _, baseline := range view.state.ProviderBaselines {
		before, hadBefore := observation.initial.findProviderBaseline(baseline.Path, baseline.Provider)
		if hadBefore && *before == baseline {
			continue
		}
		latest, hasLatest := current.findProviderBaseline(baseline.Path, baseline.Provider)
		if hadBefore != hasLatest || hadBefore && *latest != *before {
			continue
		}
		blocked := false
		for _, operation := range operations {
			blocked = blocked || operationPending(operation) && operationAffects(operation, baseline.Path)
		}
		if blocked {
			continue
		}
		if digest, err := fsutil.HashDirectoryContext(ctx, baseline.Path); err == nil && digest == baseline.InstalledHash {
			changed = current.putProviderBaseline(baseline) || changed
		}
	}
	if changed {
		if err := current.save(); err != nil {
			diagnostics = append(diagnostics, warning(current.path, err))
		}
	}
	hostUnchanged := true
	for path, expected := range observation.hostFiles {
		actual, err := fingerprint(path)
		if err != nil || actual != expected {
			hostUnchanged = false
			break
		}
	}
	if !hostUnchanged {
		return diagnostics
	}
	var codex []hostPlugin
	baselinePath, err := stateFile(filepath.Join("hosts", "plugin-baselines.json"))
	if err != nil {
		return append(diagnostics, warning("", err))
	}
	baselines := map[string]string{}
	data, err := os.ReadFile(baselinePath)
	if err == nil {
		err = json.Unmarshal(data, &baselines)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return append(diagnostics, warning(baselinePath, err))
	}
	baselineChanged := false
	for _, plugin := range observation.plugins {
		if pluginHasPendingOperation(plugin, operations) {
			continue
		}
		if plugin.Host == "codex" && !plugin.Cached {
			codex = append(codex, plugin)
		}
		key := stableID(plugin.Host, plugin.ID, plugin.Scope, plugin.Project, plugin.Version)
		if _, exists := baselines[key]; exists || plugin.BaselineDigest == "" {
			continue
		}
		if digest, err := fsutil.HashDirectoryContext(ctx, plugin.Directory); err == nil && digest == plugin.BaselineDigest {
			baselines[key] = digest
			baselineChanged = true
		}
	}
	if baselineChanged {
		if err := saveJSON(baselinePath, baselines); err != nil {
			diagnostics = append(diagnostics, warning(baselinePath, err))
		}
	}
	if observation.codexRefreshed {
		if err := saveJSON(observation.hostCache, struct {
			Plugins []hostPlugin `json:"plugins"`
		}{codex}); err != nil {
			diagnostics = append(diagnostics, warning(observation.hostCache, err))
		}
	}
	return diagnostics
}
