package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingengyuan/skillctl/internal/installhistory"
)

func TestInspectionKeepsLocalAndUpstreamFacts(t *testing.T) {
	for _, available := range []bool{false, true} {
		r := report{Provider: "skillctl-track-v1", Owner: "skillctl", Drift: "modified", UpdateAvailable: available}
		checkedReport(&r)
		asset := applyInspectionReport(skillAsset{ContentID: "content-path", Source: &sourceSpec{Kind: "git"}}, r)
		want := "current"
		if available {
			want = "outdated"
		}
		if asset.Local.Status != "modified" || asset.Upstream.Status != want || !asset.Update.Supported || asset.Update.Eligible || asset.Capabilities["update"].Supported || asset.ContentID != "content-path" {
			t.Fatalf("lost independent facts: %+v", asset)
		}
	}
	asset := applyInspectionReport(skillAsset{Source: &sourceSpec{Kind: "git"}}, report{State: "modified", Drift: "modified"})
	if asset.Upstream.Status != "not-checked" {
		t.Fatalf("invented remote success: %+v", asset)
	}
}

func TestHistoryEligibilityRequiresSuccessAndTargetCorrelation(t *testing.T) {
	item := skill{Name: "demo", Path: "/installed/demo", Host: "codex", Scope: "user"}
	for _, test := range []struct {
		candidate installhistory.Candidate
		want      bool
	}{
		{installhistory.Candidate{Source: "source", Outcome: "succeeded"}, false},
		{installhistory.Candidate{Source: "source", Name: "demo", Outcome: "failed"}, false},
		{installhistory.Candidate{Source: "source", Name: "demo", Outcome: "unknown"}, false},
		{installhistory.Candidate{Source: "source", Name: "demo", Outcome: "succeeded"}, true},
		{installhistory.Candidate{Source: "source", Outcome: "succeeded", Destination: "/installed"}, true},
		{installhistory.Candidate{Source: "source", Name: "demo", Outcome: "succeeded", Destination: "/unrelated"}, false},
	} {
		got := eligibleHistoryCandidates(item, map[string][]installhistory.Candidate{test.candidate.Name: {test.candidate}})
		if (len(got) > 0) != test.want {
			t.Errorf("candidate %+v: %v", test.candidate, got)
		}
	}
}

func TestPluginPortabilityPreservesIdentityAndHashesPackageOnce(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		writeTestSkill(t, filepath.Join(root, name), strings.ToUpper(name), "content")
	}
	items, failed := scan([]scanRoot{{Path: root}}, &bytes.Buffer{})
	if !failed || len(items) != 2 {
		t.Fatal("strict scan must retain portability issue")
	}
	observed := newObservation(t.Context())
	baseline, err := observed.hash(root)
	if err != nil {
		t.Fatal(err)
	}
	observed = newObservation(t.Context())
	plugins := []hostPlugin{{Host: "codex", ID: "fixture", Directory: root, Version: "1.2.3", Enabled: true, BaselineDigest: baseline}}
	for _, item := range items {
		asset := describeSkillObserved(item, nil, plugins, observed)
		if asset.Provider != "host-plugin" || asset.Revision != "1.2.3" || asset.Validation.Portability != "warning" || asset.Validation.DeclaredName != strings.ToUpper(item.Name) || asset.State != "managed" {
			t.Fatalf("lost host identity: %+v", asset)
		}
	}
	if len(observed.hashes) != 1 {
		t.Fatalf("package read %d times", len(observed.hashes))
	}
}

func TestSelectedFailuresAndInstallationSummary(t *testing.T) {
	selected := []skillAsset{{Name: "duplicate", Path: "/good", State: "untracked", Provider: "local-authoring"}}
	view := &inventoryView{scanFailed: true, Diagnostics: []diagnostic{{Level: "error", Path: "/unrelated", Code: "invalid_skill"}}}
	if view.selectedFailed(selected) {
		t.Fatal("unrelated installation blocked target")
	}
	view.Diagnostics[0].Path = "/good"
	if !view.selectedFailed(selected) {
		t.Fatal("selected failure was ignored")
	}
	selected = append(selected, skillAsset{Name: "duplicate", Path: "/other", State: "untracked", Provider: "local-authoring"})
	summary := summarizeAssets(selected)
	if summary.Names != 1 || summary.Installations != 2 || summary.Untracked != 2 {
		t.Fatalf("wrong counts: %+v", summary)
	}
	var text bytes.Buffer
	printTrackRepairHint(&text, []report{reportFromAsset(selected[0]), reportFromAsset(selected[1])})
	if !strings.Contains(text.String(), "2 skills") {
		t.Fatal(text.String())
	}
}

func TestSourceFailureCannotRetainCurrentObservation(t *testing.T) {
	r := report{Provider: "gh-skill", Drift: "clean"}
	checkedReport(&r)
	r.State, r.Error, r.ReasonCode = "error", "fixture fetch failed", "provider_error"
	asset := applyInspectionReport(skillAsset{Source: &sourceSpec{Kind: "git"}}, r)
	legacy := finalizeReports([]report{r})
	if asset.Upstream.Status != "unknown" || legacy[0].Upstream.Status != "unknown" || asset.Update.Eligible {
		t.Fatalf("false remote success: %+v %+v", asset, legacy)
	}
}
