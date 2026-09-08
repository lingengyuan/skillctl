package app

import (
	"context"
	"io"
	"slices"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

// Provider operations already own their journals and write-time verification.
// Group only to keep a shared repository or manifest protected as a whole.
type inspectionUnit struct {
	ID     string
	assets []skillAsset
	ready  bool
}

func inspectionUnitKey(asset skillAsset) string {
	if asset.Provider == "git-worktree" {
		if root, found := findGitRoot(asset.Path, map[string]gitRootResult{}); found {
			return root
		}
	}
	if asset.Provider == "vercel-skills-lock-v3" && len(asset.Evidence) > 0 {
		return asset.Evidence[0]
	}
	return asset.Path
}

func planAndExecuteInspection(ctx context.Context, view *inventoryView, selected []skillAsset, reports []report, updates map[string]preparedPackage, session *sourceSession, plan *lifecyclePlan, result *commandResult, opt options, progress io.Writer) ([]skillAsset, []report, bool) {
	failed, catalogBlocked := false, false
	var catalogAssets []skillAsset
	var units []inspectionUnit
	for _, asset := range selected {
		if asset.Plugin != nil {
			continue
		}
		if pkg := view.catalog.find(asset.ID); pkg != nil && !pkg.External {
			if asset.Drift == "modified" {
				failed = true
			}
			if !asset.Update.Eligible || view.selectedFailed([]skillAsset{asset}) {
				continue
			}
			prepared, found := updates[pkg.ID]
			if !found {
				continue
			}
			if err := planPackageUpdate(plan, pkg, prepared); err != nil {
				result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "update_blocked", Level: "error", Path: asset.Path, Message: err.Error()})
				failed, catalogBlocked = true, true
			} else {
				catalogAssets = append(catalogAssets, asset)
			}
			continue
		}
		key := inspectionUnitKey(asset)
		index := slices.IndexFunc(units, func(unit inspectionUnit) bool { return unit.ID == key })
		if index < 0 {
			units = append(units, inspectionUnit{ID: key})
			index = len(units) - 1
		}
		units[index].assets = append(units[index].assets, asset)
	}
	if err := plan.finish(view.catalog); err != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "plan_failed", Level: "error", Message: err.Error()})
		failed, catalogBlocked = true, true
	}
	for i := range units {
		unit := &units[i]
		blocked := view.selectedFailed(unit.assets)
		for _, asset := range unit.assets {
			blocked = blocked || asset.Drift == "modified" || asset.State == "blocked"
			unit.ready = unit.ready || asset.Update.Eligible
		}
		unit.ready = unit.ready && !blocked
		if !unit.ready {
			continue
		}
		plan.Changes = append(plan.Changes, plannedChange{Path: unit.ID, Action: "provider-update", Target: unit.assets[0].Provider})
		plan.Affected = appendUniqueString(plan.Affected, "update unit: "+unit.ID)
		for _, asset := range unit.assets {
			for _, binding := range asset.Bindings {
				if binding.Enabled {
					plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
				}
			}
		}
	}
	sortedAffected(plan)
	// Preparation above is read-only. Each existing transaction verifies local
	// content again before writing and independently records its final outcome.
	if !opt.DryRun && !catalogBlocked && len(catalogAssets) > 0 && ctx.Err() == nil {
		operation, err := prepareOperation("update", plan.mutations, plan.Affected)
		recordContextOperation(ctx, operation)
		if err == nil {
			err = operation.apply()
		}
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "operation_failed", Level: "error", Message: err.Error()})
			failed = true
		} else {
			for _, asset := range catalogAssets {
				for i := range reports {
					if fsutil.SamePath(reports[i].Path, asset.Path) {
						reports[i].Status, reports[i].State, reports[i].ReasonCode, reports[i].UpdateAvailable = "updated", "current", "", false
						reports[i].Upstream = observedUpstream(false)
						if pkg := view.catalog.find(asset.ID); pkg != nil {
							reports[i].Revision, reports[i].Path = pkg.Revision, pkg.Directory
						}
					}
				}
			}
		}
	}
	for _, unit := range units {
		if opt.DryRun || !unit.ready {
			continue
		}
		if ctx.Err() != nil {
			failed = true
			break
		}
		updated, updateFailed := inspectDetailed(ctx, view.timeout, "update", skillsForAssets(view, unit.assets, false), view.state, view.manifests, view.managed, progress)
		reports = replaceInstallationReports(reports, updated)
		failed = failed || updateFailed
	}
	for _, operation := range session.operations {
		result.Operations = append(result.Operations, *operation)
	}
	if len(result.Operations) == 1 {
		result.Operation = new(result.Operations[0])
	}
	if len(result.Operations) > 0 {
		fresh, err := loadInventory(ctx, opt, false, false)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "post_update_inventory_failed", Level: "error", Message: err.Error()})
			failed = true
		} else {
			for i, asset := range selected {
				for _, current := range fresh.Items {
					if fsutil.SamePath(asset.Path, current.Path) || asset.Provider == "skillctl-store" && asset.ID == current.ID {
						selected[i] = current
						break
					}
				}
			}
		}
	}
	for i := range selected {
		for _, r := range reports {
			if fsutil.SamePath(selected[i].Path, r.Path) {
				selected[i] = applyInspectionReport(selected[i], r)
				break
			}
		}
	}
	return selected, reports, failed
}

func recordContextOperation(ctx context.Context, operation *operationRecord) {
	if session, ok := ctx.Value(sourceSessionContextKey{}).(*sourceSession); ok && operation != nil {
		session.operations = append(session.operations, operation)
	}
}
