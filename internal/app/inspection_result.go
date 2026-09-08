package app

import "time"

type localObservation struct {
	Status          string `json:"status"`
	BaselineKind    string `json:"baselineKind,omitempty"`
	DigestAlgorithm string `json:"digestAlgorithm,omitempty"`
}

type upstreamObservation struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Reason    string    `json:"reason,omitempty"`
}

type updateEligibility struct {
	Supported bool   `json:"supported"`
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason,omitempty"`
}

type documentValidation struct {
	Parse        string `json:"parse"`
	Portability  string `json:"portability"`
	DeclaredName string `json:"declaredName,omitempty"`
	Message      string `json:"message,omitempty"`
}

// completeAsset derives compatibility capabilities and richer observations
// from one result, without performing file, network or state reads.
func completeAsset(asset skillAsset) skillAsset {
	asset.Local.Status = asset.Drift
	if asset.Local.Status == "" {
		asset.Local.Status = "unknown"
	}
	if asset.Digest != "" {
		asset.Local.DigestAlgorithm = "skillctl-content-v1"
		asset.Local.BaselineKind = "verified-artifact"
		if asset.Plugin != nil {
			asset.Local.BaselineKind = "observed"
		}
	}
	if asset.Revision != "" && asset.Source != nil && (asset.Provider == "gh-skill" || asset.Provider == "vercel-skills-lock-v3") {
		asset.Local.BaselineKind, asset.Local.DigestAlgorithm = "provider-tree", "skillctl-content-v1"
		if asset.Provider == "gh-skill" {
			asset.Local.DigestAlgorithm = "gh-document-v1"
		}
		if asset.Source.Kind == "well-known" {
			asset.Local.BaselineKind = "verified-artifact"
		}
	}
	if asset.Validation.Parse == "" {
		asset.Validation.Parse = "unknown"
	}
	if asset.Validation.Portability == "" {
		asset.Validation.Portability = "unknown"
	}
	if asset.Upstream.Status == "" {
		asset.Upstream.Status = "not-checked"
		if asset.Source == nil {
			asset.Upstream.Status = "not-applicable"
		}
	}
	asset.Capabilities = assetCapabilities(asset)
	capability := asset.Capabilities["update"]
	providerAsset := asset
	providerAsset.State, providerAsset.Drift = "unknown", "unknown"
	supported := assetCapabilities(providerAsset)["update"].Supported
	asset.Update = updateEligibility{Supported: supported, Eligible: capability.Supported, Reason: capability.Reason}
	if asset.Source != nil && asset.State == "modified" {
		asset.Update.Supported, asset.Update.Eligible, asset.Update.Reason = true, false, "local_changes"
	}
	if asset.Plugin != nil && asset.State != "invalid" && asset.State != "broken" {
		asset.Update.Supported = asset.Plugin.Host == "claude"
	}
	if asset.Source != nil && asset.State == "pinned" {
		asset.Update.Supported, asset.Update.Eligible, asset.Update.Reason = true, false, "pinned_revision"
	}
	if asset.Source != nil && asset.Upstream.Status != "outdated" && asset.Update.Eligible {
		asset.Update.Eligible, asset.Update.Reason = false, "upstream_not_checked"
		if asset.Upstream.Status == "current" {
			asset.Update.Reason = "already_current"
		}
	}
	if asset.Update.Eligible && asset.Drift == "unknown" {
		asset.Update.Eligible, asset.Update.Reason = false, "local_baseline_unavailable"
	}
	if asset.Plugin != nil && asset.Plugin.Scope == "managed" {
		asset.Update.Eligible, asset.Update.Reason = false, "host_policy_managed"
	}
	return asset
}

func applyInspectionReport(asset skillAsset, report report) skillAsset {
	asset.State, asset.ReasonCode, asset.Drift = report.State, report.ReasonCode, report.Drift
	asset.Revision, asset.Error, asset.Provider, asset.Owner = report.Revision, report.Error, report.Provider, report.Owner
	asset.Upstream = report.Upstream
	if report.Error != "" {
		asset.Upstream = upstreamObservation{Status: "unknown", Reason: report.ReasonCode}
	}
	return completeAsset(asset)
}

func observedUpstream(available bool) upstreamObservation {
	status := "current"
	if available {
		status = "outdated"
	}
	return upstreamObservation{Status: status, CheckedAt: time.Now().UTC()}
}
