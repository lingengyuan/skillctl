package app

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

func inspectionSourceRequests(view *inventoryView, selected []skillAsset) []sourceRequest {
	var requests []sourceRequest
	for _, asset := range selected {
		if asset.Source == nil || asset.Source.Kind != "git" || asset.State == "invalid" || asset.State == "blocked" || asset.State == "broken" || asset.State == "pinned" {
			continue
		}
		if asset.Provider == "git-worktree" {
			continue
		}
		requests = append(requests, sourceRequest{Source: asset.Source.URL, Ref: asset.Source.Ref, Skills: []string{asset.Name}})
	}
	return requests
}

func inspectionSkills(view *inventoryView, selected []skillAsset, opt options) []skill {
	var result []skill
	if opt.Offline {
		return result
	}
	for _, asset := range selected {
		if asset.Plugin != nil || asset.State == "blocked" || asset.State == "invalid" || asset.State == "broken" || asset.State == "pinned" {
			continue
		}
		pkg := view.catalog.find(asset.ID)
		if pkg != nil && (!pkg.External || pkg.Pin != "" || asset.State == "disabled") {
			continue
		}
		result = append(result, skillsForAssets(view, []skillAsset{asset}, false)...)
	}
	return result
}

func replaceInstallationReports(reports, replacements []report) []report {
	for _, replacement := range replacements {
		for i := range reports {
			if fsutil.SamePath(reports[i].Path, replacement.Path) {
				reports[i] = replacement
				break
			}
		}
	}
	return reports
}

func recoverSelectedSources(ctx context.Context, view *inventoryView, selected []skillAsset, opt options, progress io.Writer) ([]skillAsset, []diagnostic) {
	var pending []skill
	for _, asset := range selected {
		if asset.Provider == "local-authoring" && asset.State == "untracked" && view.catalog.byPath(asset.Path) == nil {
			pending = append(pending, skillsForAssets(view, []skillAsset{asset}, false)...)
		}
	}
	if len(pending) == 0 {
		return selected, nil
	}
	timeout := view.timeout
	if opt.RecoveryTimeout > 0 {
		timeout = opt.RecoveryTimeout
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(progress, "Checking structured install evidence for %d unresolved installations...\n", len(pending))
	var errors bytes.Buffer
	trackFromInstallHistory(recoveryCtx, view.timeout, pending, view.state, view.manifests, view.managed, false, progress, &errors)
	provenance, _ := newProvenanceIndex(view.skills, view.state, view.manifests, view.managed)
	for i, asset := range selected {
		if asset.Provider != "local-authoring" {
			continue
		}
		for _, item := range pending {
			if fsutil.SamePath(asset.Path, item.Path) {
				selected[i] = describeSkillObserved(item, provenance, nil, view.observation)
				break
			}
		}
	}
	if errors.Len() > 0 {
		return selected, []diagnostic{{Code: "source_recovery_incomplete", Level: "warning", Message: oneLine(errors.String())}}
	}
	return selected, nil
}

func printInspectionSummary(w io.Writer, summary *inspectionSummary) {
	if summary == nil {
		return
	}
	fmt.Fprintf(w, "%d names, %d installations: %d with upstream updates, %d locally modified, %d without a confirmed source, %d failed.\n", summary.Names, summary.Installations, summary.Outdated, summary.Modified, summary.Untracked, summary.Failed)
}

func printInspectionReports(w io.Writer, reports []report, verbose bool) {
	if verbose {
		printReports(w, reports)
		return
	}
	var actionable []report
	managed := 0
	for _, r := range reports {
		if (r.State == "managed" || r.State == "disabled") && r.Error == "" && r.Drift != "modified" {
			managed++
			continue
		}
		actionable = append(actionable, r)
	}
	printReports(w, actionable)
	if managed > 0 {
		fmt.Fprintf(w, "%d host-managed entries; use --verbose for details.\n", managed)
	}
}
