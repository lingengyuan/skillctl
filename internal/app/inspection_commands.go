package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func runInspection(ctx context.Context, view *inventoryView, selected []skillAsset, command string, opt options, stdout, stderr io.Writer) int {
	result := commandResult{Command: command, Items: selected, Diagnostics: view.Diagnostics}
	failed := view.scanFailed
	previousOperations := map[string]bool{}
	if command == "update" && !opt.DryRun {
		operations, err := readOperations()
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "history_unavailable", Level: "error", Message: err.Error()})
			return outputResult(result, opt, stdout, stderr, true)
		}
		for _, operation := range operations {
			previousOperations[operation.ID] = true
		}
	}
	progress := io.Writer(io.Discard)
	if !opt.JSON {
		progress = stderr
	}
	legacyItems := skillsForAssets(view, selected, true)
	fmt.Fprintf(progress, "Found %d unique skills (%d installations).\nChecking %d unique skills (%d installations)...\n", uniqueSkillCount(legacyItems), len(legacyItems), uniqueSkillCount(legacyItems), len(legacyItems))
	// History is evidence only. A failed candidate leaves the source unknown and
	// must not turn an unrelated check into a failed operation.
	if !opt.NoHistory && !opt.Offline {
		pending := []skill{}
		for _, asset := range selected {
			if asset.Provider == "local-authoring" && asset.State == "untracked" && view.catalog.byPath(asset.Path) == nil {
				pending = append(pending, skillsForAssets(view, []skillAsset{asset}, false)...)
			}
		}
		var evidenceErrors bytes.Buffer
		if len(pending) > 0 {
			fmt.Fprintf(progress, "Recovering sources for %d skills from structured install history...\n", len(pending))
		}
		trackFromInstallHistory(ctx, view.timeout, pending, view.state, view.manifests, view.managed, false, progress, &evidenceErrors)
		if evidenceErrors.Len() > 0 {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "source_recovery_incomplete", Level: "warning", Message: oneLine(evidenceErrors.String())})
		}
	}
	provenance, _ := newProvenanceIndex(view.skills, view.state, view.manifests, view.managed)
	for i, asset := range selected {
		if asset.Provider == "local-authoring" {
			for _, item := range view.skills {
				if fsutil.SamePath(item.Path, asset.Path) {
					selected[i] = describeSkill(item, provenance, nil)
					break
				}
			}
		}
	}
	updates, cleanup, err := prepareCatalogUpdates(ctx, view, selected, opt)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "source_unavailable", Level: "error", Message: err.Error()})
		return outputResult(result, opt, stdout, stderr, true)
	}
	defer cleanup()
	plan := &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}
	reports := []report{}
	legacy := []skill{}
	pluginAssets := []skillAsset{}
	for i := range selected {
		asset := &selected[i]
		pkg := view.catalog.find(asset.ID)
		if asset.ReasonCode == "credential_in_source" {
			reports = append(reports, reportFromAsset(*asset))
			failed = true
			continue
		}
		if asset.Plugin != nil {
			if command == "update" && len(opt.Names) > 0 {
				pluginAssets = append(pluginAssets, *asset)
			}
			reports = append(reports, reportFromAsset(*asset))
			continue
		}
		if pkg != nil && pkg.Pin != "" {
			asset.State, asset.ReasonCode = "pinned", "pinned_revision"
			reports = append(reports, reportFromAsset(*asset))
			continue
		}
		if pkg != nil && pkg.External && asset.State == "disabled" {
			reports = append(reports, reportFromAsset(*asset))
			continue
		}
		if command == "update" && pkg != nil && pkg.External && asset.Drift == "modified" {
			reports = append(reports, reportFromAsset(*asset))
			failed = true
			continue
		}
		if pkg == nil || pkg.External {
			if opt.Offline {
				if asset.Source != nil && asset.State == "unknown" {
					asset.ReasonCode = "offline_not_checked"
				}
				reports = append(reports, reportFromAsset(*asset))
			} else {
				legacy = append(legacy, skillsForAssets(view, []skillAsset{*asset}, false)...)
			}
			continue
		}
		prepared, hasUpdate := updates[pkg.ID]
		if asset.State == "broken" || asset.State == "modified" {
			failed = true
		} else if pkg.Pin != "" {
			asset.State, asset.ReasonCode = "pinned", "pinned_revision"
		} else if hasUpdate {
			asset.State, asset.ReasonCode = "current", ""
			if opt.Offline && pkg.Source.Kind != "local" {
				asset.State, asset.ReasonCode = "unknown", "offline_cached_revision"
			}
			if prepared.Digest != pkg.Digest {
				asset.State, asset.ReasonCode = "outdated", "upstream_changed"
				if command == "update" {
					if err := planPackageUpdate(plan, pkg, prepared); err != nil {
						result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "update_blocked", Level: "error", Message: err.Error()})
						failed = true
					}
				}
			}
		}
		reports = append(reports, reportFromAsset(*asset))
	}
	if len(pluginAssets) > 0 {
		// A selected native package is a separate transaction unit.
		if len(pluginAssets) != len(selected) {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "mixed_operation_units", Level: "error", Message: "select native plugin packages separately from standalone skills"})
			return outputResult(result, opt, stdout, stderr, true)
		}
		return runPluginOperation(ctx, command, view, selected, opt, stdout, stderr)
	}
	if command == "update" {
		if err := plan.finish(view.catalog); err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "plan_failed", Level: "error", Message: err.Error()})
			return outputResult(result, opt, stdout, stderr, true)
		}
		if !failed && !opt.DryRun && len(plan.mutations) > 0 {
			op, err := prepareOperation(command, plan.mutations, plan.Affected)
			if err == nil {
				result.Operation = op
				err = op.apply()
			}
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "operation_failed", Level: "error", Message: err.Error()})
				failed = true
			} else {
				for i := range selected {
					if pkg := view.catalog.find(selected[i].ID); pkg != nil && !pkg.External {
						selected[i] = describePackage(*pkg)
						if pkg.Pin == "" {
							selected[i].State, selected[i].ReasonCode = "current", ""
						}
					}
				}
				for i := range reports {
					if pkg := view.catalog.byPath(reports[i].Path); pkg != nil && !pkg.External {
						reports[i].Status = "updated"
						reports[i].State, reports[i].ReasonCode, reports[i].UpdateAvailable = "current", "", false
						reports[i].Revision = pkg.Revision
					}
				}
			}
		}
		result.Plan = plan
	}
	if len(legacy) > 0 {
		action := command
		if opt.DryRun || failed {
			action = "check"
		}
		legacyReports, legacyFailed := inspectDetailed(ctx, view.timeout, action, legacy, view.state, view.manifests, view.managed, progress)
		failed = failed || legacyFailed
		for _, r := range legacyReports {
			if command == "update" && opt.DryRun && r.UpdateAvailable && r.Drift != "modified" {
				unit := r.Path
				if r.Provider == "git-worktree" {
					if root, found := findGitRoot(r.Path, map[string]gitRootResult{}); found {
						unit = root
					}
				}
				change := plannedChange{Path: unit, Action: "provider-update", Target: r.Provider}
				if !slices.Contains(plan.Changes, change) {
					plan.Changes = append(plan.Changes, change)
				}
				plan.Affected = appendUniqueString(plan.Affected, "update unit: "+unit)
				for _, asset := range selected {
					if fsutil.SamePath(asset.Path, r.Path) || r.Provider == "git-worktree" && fsutil.Within(unit, asset.Path) {
						for _, binding := range asset.Bindings {
							if binding.Enabled {
								plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
							}
						}
					}
				}
			}
			for i := range selected {
				if fsutil.SamePath(selected[i].Path, r.Path) {
					selected[i].State, selected[i].ReasonCode, selected[i].Drift, selected[i].Revision, selected[i].Error = r.State, r.ReasonCode, r.Drift, r.Revision, r.Error
					selected[i].Provider, selected[i].Owner = r.Provider, r.Owner
				}
			}
		}
		reports = append(reports, legacyReports...)
	}
	if command == "update" && !opt.DryRun {
		operations, historyErr := readOperations()
		if historyErr != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "history_unavailable", Level: "error", Message: historyErr.Error()})
			failed = true
		}
		for _, operation := range operations {
			if !previousOperations[operation.ID] {
				result.Operations = append(result.Operations, operation)
				for _, step := range operation.Steps {
					if step.Before != step.After {
						change := plannedChange{Path: step.Path, Action: step.Action}
						if !slices.Contains(plan.Changes, change) {
							plan.Changes = append(plan.Changes, change)
						}
					}
				}
				for _, affected := range operation.Affected {
					plan.Affected = appendUniqueString(plan.Affected, affected)
				}
			}
		}
		if result.Operation == nil && len(result.Operations) == 1 {
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
						if fsutil.SamePath(current.Path, asset.Path) || current.ID == asset.ID && current.ContentID == asset.ContentID {
							current.State, current.ReasonCode, current.Error = asset.State, asset.ReasonCode, asset.Error
							selected[i] = current
							for j := range reports {
								if fsutil.SamePath(reports[j].Path, asset.Path) {
									reports[j].Revision = current.Revision
								}
							}
							break
						}
					}
				}
			}
		}
	}
	result.Items = selected
	if opt.JSONVersion == 2 {
		return outputResult(result, opt, stdout, stderr, failed)
	}
	reports = finalizeReports(mergeReportsByIdentity(reports))
	if opt.JSON {
		for _, d := range result.Diagnostics {
			fmt.Fprintf(stderr, "%s: %s\n", d.Code, d.Message)
		}
		if err := writeJSON(stdout, reports); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		printReports(stdout, reports)
		printTrackRepairHint(stdout, reports)
		if command == "update" && len(plan.Changes) > 0 {
			for _, a := range plan.Affected {
				fmt.Fprintf(stdout, "  Affects %s\n", a)
			}
			if result.Operation != nil {
				fmt.Fprintf(stdout, "Operation %s: %s\n", result.Operation.ID, result.Operation.State)
			}
		}
		for _, d := range result.Diagnostics {
			fmt.Fprintf(stderr, "%s: %s\n", d.Code, d.Message)
		}
		if len(reports) == 0 {
			fmt.Fprintln(stdout, "No skills found.")
		}
	}
	if failed {
		return 1
	}
	return 0
}

func reportFromAsset(asset skillAsset) report {
	status := asset.State
	switch asset.State {
	case "current":
		status = "up to date"
	case "outdated":
		status = "update available"
	case "untracked":
		status = "local/untracked (no update source)"
	case "managed":
		status = "managed by " + asset.Owner
	}
	item := skill{Name: asset.Name, Path: asset.Path}
	if len(asset.Bindings) > 0 {
		item.Host, item.Scope = asset.Bindings[0].Host, asset.Bindings[0].Scope
	}
	r := reportFor(item, asset.Provider, asset.Owner, asset.Evidence, asset.Drift, status, asset.State == "outdated", "report-only", asset.Error)
	r.State, r.ReasonCode, r.Revision = asset.State, asset.ReasonCode, asset.Revision
	return r
}

type skillDiff struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Baseline string `json:"baseline"`
	Text     string `json:"diff"`
}

func runDiff(ctx context.Context, view *inventoryView, selected []skillAsset, opt options, stdout, stderr io.Writer) int {
	result := commandResult{Command: "diff", Items: selected, Diagnostics: view.Diagnostics}
	diffs := []skillDiff{}
	failed := false
	for _, asset := range selected {
		if asset.Source == nil || asset.Plugin != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "diff_unavailable", Level: "warning", Path: asset.Path, Message: asset.Name + ": no standalone source is available"})
			continue
		}
		source := *asset.Source
		opctx, cancel := context.WithTimeout(ctx, view.timeout)
		prepared, cleanup, err := preparePackages(opctx, source, []string{asset.Name}, opt.Offline)
		cancel()
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "diff_failed", Level: "error", Message: err.Error()})
			failed = true
			continue
		}
		for _, pkg := range prepared {
			if pkg.Name != asset.Name {
				continue
			}
			cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", asset.Path, pkg.Directory)
			output, err := cmd.CombinedOutput()
			if exit, ok := errors.AsType[*exec.ExitError](err); err != nil && (!ok || exit.ExitCode() != 1) {
				result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "diff_failed", Level: "error", Message: oneLine(string(output))})
				failed = true
				break
			}
			text := strings.ReplaceAll(string(output), pkg.Directory, "upstream/"+pkg.Name)
			diffs = append(diffs, skillDiff{ID: asset.ID, Name: asset.Name, Baseline: pkg.Revision, Text: text})
		}
		cleanup()
	}
	slices.SortFunc(diffs, func(a, b skillDiff) int { return strings.Compare(a.ID, b.ID) })
	result.Result = diffs
	if opt.JSON {
		return outputResult(result, opt, stdout, stderr, failed)
	}
	for _, diff := range diffs {
		fmt.Fprintf(stdout, "%s [%s] against %s\n%s", diff.Name, diff.ID, diff.Baseline, diff.Text)
		if diff.Text == "" {
			fmt.Fprintln(stdout, "No content differences.")
		}
	}
	for _, d := range result.Diagnostics {
		fmt.Fprintf(stderr, "%s: %s\n", d.Code, d.Message)
	}
	if failed {
		return 1
	}
	return 0
}
