package app

import (
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
	failed := view.selectedFailed(selected)
	progress := io.Writer(io.Discard)
	if !opt.JSON {
		progress = stderr
	}
	session := newSourceSession(ctx, view.timeout, progress)
	defer session.close()
	ctx = context.WithValue(ctx, sourceSessionContextKey{}, session)
	session.ctx = ctx
	session.observation = view.observation
	initial := summarizeAssets(selected)
	fmt.Fprintf(progress, "Found %d names (%d installations).\n", initial.Names, initial.Installations)

	// Register known sources together before spending any optional history budget.
	if !opt.Offline {
		session.prefetch(inspectionSourceRequests(view, selected))
	}
	legacy := inspectionSkills(view, selected, opt)
	reports := make([]report, 0, len(selected))
	for _, asset := range selected {
		reports = append(reports, reportFromAsset(asset))
	}
	if len(legacy) > 0 {
		checked, checkFailed := inspectDetailed(ctx, view.timeout, "check", legacy, view.state, view.manifests, view.managed, progress)
		reports = replaceInstallationReports(reports, checked)
		failed = failed || checkFailed
	}
	updates, cleanup, sourceFailures := prepareCatalogUpdates(ctx, view, selected, opt)
	defer cleanup()
	for i := range selected {
		asset := &selected[i]
		pkg := view.catalog.find(asset.ID)
		if pkg == nil || pkg.External || asset.Plugin != nil {
			continue
		}
		if pkg.Pin != "" {
			asset.State, asset.ReasonCode = "pinned", "pinned_revision"
		} else if err := sourceFailures[pkg.ID]; err != nil {
			asset.State, asset.ReasonCode, asset.Error = "error", "source_unavailable", err.Error()
			asset.Upstream = upstreamObservation{Status: "unknown", Reason: "source_unavailable"}
			failed = true
		} else if prepared, found := updates[pkg.ID]; found {
			if opt.Offline && pkg.Source.Kind != "local" {
				asset.Upstream = upstreamObservation{Status: "not-checked", Reason: "offline_cached_revision"}
			} else {
				asset.Upstream = observedUpstream(prepared.Digest != pkg.Digest)
			}
			if asset.Drift == "clean" && asset.State != "broken" {
				asset.State, asset.ReasonCode = "current", ""
				if asset.Upstream.Status == "outdated" {
					asset.State, asset.ReasonCode = "outdated", "upstream_changed"
				}
				if asset.Upstream.Status == "not-checked" {
					asset.State, asset.ReasonCode = "unknown", "offline_cached_revision"
				}
			}
		}
		reports = replaceInstallationReports(reports, []report{reportFromAsset(*asset)})
	}
	if !opt.NoHistory && !opt.Offline && ctx.Err() == nil {
		var warnings []diagnostic
		selected, warnings = recoverSelectedSources(ctx, view, selected, opt, progress)
		result.Diagnostics = append(result.Diagnostics, warnings...)
		var recovered []skill
		for _, asset := range selected {
			for _, prior := range reports {
				if prior.State == "untracked" && asset.Source != nil && fsutil.SamePath(prior.Path, asset.Path) {
					recovered = append(recovered, skillsForAssets(view, []skillAsset{asset}, false)...)
					break
				}
			}
		}
		if len(recovered) > 0 {
			checked, checkFailed := inspectDetailed(ctx, view.timeout, "check", recovered, view.state, view.manifests, view.managed, progress)
			reports = replaceInstallationReports(reports, checked)
			failed = failed || checkFailed
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
	plan := &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}
	if command == "update" {
		var pluginAssets []skillAsset
		for _, asset := range selected {
			if asset.Plugin != nil && len(opt.Names) > 0 {
				pluginAssets = append(pluginAssets, asset)
			}
		}
		if len(pluginAssets) > 0 {
			if len(pluginAssets) != len(selected) {
				result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "mixed_operation_units", Level: "error", Message: "select native plugin packages separately from standalone skills"})
				return outputResult(result, opt, stdout, stderr, true)
			}
			return runPluginOperation(ctx, command, view, selected, opt, stdout, stderr)
		}
		var executionFailed bool
		selected, reports, executionFailed = planAndExecuteInspection(ctx, view, selected, reports, updates, session, plan, &result, opt, progress)
		failed = failed || executionFailed
		result.Plan = plan
	}
	if ctx.Err() != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "command_incomplete", Level: "error", Message: ctx.Err().Error()})
		failed = true
	}
	if command == "check" && !opt.DryRun && !opt.Offline {
		result.Diagnostics = append(result.Diagnostics, commitCheckObservations(ctx, view)...)
	}
	result.Items, result.Summary = selected, summarizeAssets(selected)
	if opt.JSONVersion == 2 {
		return outputResult(result, opt, stdout, stderr, failed)
	}
	installationReports := finalizeReports(reports)
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
		printInspectionSummary(stdout, result.Summary)
		if opt.Verbose {
			reports = installationReports
		}
		printInspectionReports(stdout, reports, opt.Verbose)
		printTrackRepairHint(stdout, installationReports)
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
	r := reportFor(item, asset.Provider, asset.Owner, asset.Evidence, asset.Drift, status, asset.State == "outdated" || asset.Upstream.Status == "outdated", "report-only", asset.Error)
	r.State, r.ReasonCode, r.Revision, r.Upstream = asset.State, asset.ReasonCode, asset.Revision, asset.Upstream
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
