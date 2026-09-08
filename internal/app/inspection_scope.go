package app

import "github.com/lingengyuan/skillctl/internal/fsutil"

type inspectionSummary struct {
	Names         int `json:"names"`
	Installations int `json:"installations"`
	Untracked     int `json:"untracked"`
	Modified      int `json:"modified"`
	Outdated      int `json:"outdated"`
	Failed        int `json:"failed"`
}

func summarizeAssets(assets []skillAsset) *inspectionSummary {
	summary := &inspectionSummary{Installations: len(assets)}
	names := map[string]bool{}
	for _, asset := range assets {
		names[asset.Name] = true
		if asset.State == "untracked" {
			summary.Untracked++
		}
		if asset.Drift == "modified" {
			summary.Modified++
		}
		if asset.Upstream.Status == "outdated" {
			summary.Outdated++
		}
		if asset.State == "error" || asset.State == "invalid" || asset.State == "broken" || asset.State == "ambiguous" {
			summary.Failed++
		}
	}
	summary.Names = len(names)
	return summary
}

// Keep all diagnostics visible; only failures affecting the selected operation
// units gate a targeted action. Unscoped errors remain global.
func (v *inventoryView) selectedFailed(selected []skillAsset) bool {
	for _, d := range v.Diagnostics {
		if d.Level != "error" {
			continue
		}
		if d.Path == "" {
			return true
		}
		for _, asset := range selected {
			unit := asset.Path
			if asset.Plugin != nil {
				unit = asset.Plugin.Directory
			}
			if asset.Provider == "git-worktree" {
				if root, found := findGitRoot(asset.Path, map[string]gitRootResult{}); found {
					unit = root
				}
			}
			if fsutil.Within(unit, d.Path) || fsutil.Within(d.Path, unit) {
				return true
			}
			for _, binding := range asset.Bindings {
				if fsutil.Within(d.Path, binding.Path) || fsutil.Within(binding.Path, d.Path) {
					return true
				}
			}
		}
	}
	for _, asset := range selected {
		if asset.State == "invalid" || asset.State == "broken" || asset.State == "ambiguous" || asset.State == "error" {
			return true
		}
	}
	return false
}
