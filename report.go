package main

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// report is deliberately a value object: rendering does not need to know how
// an adapter discovered ownership.
type report struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	Identity        string               `json:"identity"`
	Path            string               `json:"path"`
	Aliases         []string             `json:"aliases,omitempty"`
	ScanRoot        string               `json:"scanRoot"`
	Host            string               `json:"host"`
	Scope           string               `json:"scope"`
	Provider        string               `json:"provider"`
	Owner           string               `json:"owner"`
	Evidence        []string             `json:"evidence"`
	Revision        string               `json:"revision,omitempty"`
	Drift           string               `json:"drift"`
	State           string               `json:"state"`
	ReasonCode      string               `json:"reasonCode,omitempty"`
	Status          string               `json:"status"`
	UpdateAvailable bool                 `json:"updateAvailable"`
	Executor        string               `json:"executor"`
	Error           string               `json:"error,omitempty"`
	Installations   []reportInstallation `json:"installations,omitempty"`
	FailureGroup    string               `json:"-"`
	FailureSource   string               `json:"failureSource,omitempty"`
	FailureStage    string               `json:"failureStage,omitempty"`
}

type reportInstallation struct {
	Path            string `json:"path"`
	Host            string `json:"host"`
	Scope           string `json:"scope"`
	Provider        string `json:"provider"`
	Owner           string `json:"owner"`
	Revision        string `json:"revision,omitempty"`
	Drift           string `json:"drift"`
	Status          string `json:"status"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Executor        string `json:"executor"`
	Error           string `json:"error,omitempty"`
}

func reportFor(item skill, provider, owner string, evidence []string, drift, status string, available bool, executor, err string) report {
	return report{SchemaVersion: 1, Identity: item.Name, Path: item.Path, Aliases: item.Aliases, ScanRoot: item.ScanRoot, Host: item.Host, Scope: item.Scope, Provider: provider, Owner: owner, Evidence: evidence, Drift: drift, Status: status, UpdateAvailable: available, Executor: executor, Error: err}
}

func attachSourceFailure(r *report, err error) {
	key, label, stage, ok := sourceErrorDetails(err)
	if !ok {
		return
	}
	r.FailureGroup = key
	r.FailureSource = label
	r.FailureStage = stage
}

func finalizeReports(reports []report) []report {
	for i := range reports {
		reports[i].SchemaVersion = 1
		if reports[i].State == "" {
			reports[i].State, reports[i].ReasonCode = classifyReport(reports[i])
		}
	}
	return reports
}

// State is supplied by adapters. This fallback uses structured fields only;
// presentation text and translated error messages never determine business state.
func classifyReport(r report) (string, string) {
	if r.Error != "" {
		return "error", "provider_error"
	}
	if r.Provider == "ambiguous" {
		return "ambiguous", "ambiguous_provenance"
	}
	if r.Drift == "broken" {
		return "broken", "broken_link"
	}
	if r.Drift == "modified" {
		return "modified", "local_changes"
	}
	if r.UpdateAvailable {
		return "outdated", "upstream_changed"
	}
	return "unknown", "not_checked"
}

func checkedReport(r *report) {
	r.State, r.ReasonCode = "current", ""
	if r.Drift == "modified" {
		r.State, r.ReasonCode = "modified", "local_changes"
	} else if r.UpdateAvailable {
		r.State, r.ReasonCode = "outdated", "upstream_changed"
	} else if r.Drift == "unknown" {
		r.State, r.ReasonCode = "unknown", "local_baseline_unavailable"
	}
	if r.Error != "" {
		r.State, r.ReasonCode = "error", "provider_error"
	}
}

func printReport(w io.Writer, r report, showPath bool) {
	name := r.Identity
	if r.Path != "" && (showPath || r.Status == "ambiguous provenance" || strings.HasPrefix(r.Status, "broken") || r.Error != "") {
		name += " [" + r.Path + "]"
	}
	status := r.Status
	if r.Error != "" && !strings.Contains(status, r.Error) {
		status += " (" + r.Error + ")"
	}
	fmt.Fprintf(w, "%s [%s, %s]: %s\n", name, r.Provider, r.Owner, status)
	if !reportNeedsInstallationDetails(r) {
		return
	}
	for _, installation := range r.Installations {
		installationStatus := installation.Status
		if installation.Error != "" && !strings.Contains(installationStatus, installation.Error) {
			installationStatus += " (" + installation.Error + ")"
		}
		fmt.Fprintf(w, "  %s [%s, %s]: %s\n", installation.Path, installation.Provider, installation.Owner, installationStatus)
	}
}

func reportNeedsInstallationDetails(r report) bool {
	if len(r.Installations) < 2 {
		return false
	}
	first := r.Installations[0]
	for _, installation := range r.Installations[1:] {
		if installation.Provider != first.Provider || installation.Owner != first.Owner || installation.Status != first.Status || installation.Error != first.Error || installation.Drift != first.Drift || installation.Revision != first.Revision {
			return true
		}
	}
	return false
}

// printReports collapses one shared source failure into one diagnostic while
// retaining per-installation detail in JSON. This prevents a repository outage
// from flooding text output with the same timeout for every skill it contains.
func printReports(w io.Writer, reports []report) {
	groups := map[string][]report{}
	for _, item := range reports {
		if item.FailureGroup != "" {
			groups[item.FailureGroup] = append(groups[item.FailureGroup], item)
		}
	}
	printed := map[string]bool{}
	for _, item := range reports {
		group := groups[item.FailureGroup]
		if item.FailureGroup == "" || len(group) < 2 {
			printReport(w, item, false)
			continue
		}
		if printed[item.FailureGroup] {
			continue
		}
		printed[item.FailureGroup] = true
		names := make([]string, 0, len(group))
		for _, affected := range group {
			names = append(names, affected.Identity)
		}
		slices.Sort(names)
		source := item.FailureSource
		if source == "" {
			source = "remote source"
		}
		stage := ""
		if item.FailureStage != "" {
			stage = " during " + item.FailureStage
		}
		fmt.Fprintf(w, "Remote source %s failed%s: %s\n", source, stage, item.Error)
		fmt.Fprintf(w, "  Affected skills (%d): %s\n", len(names), strings.Join(names, ", "))
	}
}

type reportSink struct {
	reports []report
}

func mergeReportsByIdentity(reports []report) []report {
	groups := make(map[string][]report)
	for _, item := range reports {
		groups[item.Identity] = append(groups[item.Identity], item)
	}
	identities := slices.Sorted(maps.Keys(groups))
	merged := make([]report, 0, len(identities))
	for _, identity := range identities {
		group := groups[identity]
		slices.SortStableFunc(group, func(a, b report) int {
			return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Provider, b.Provider))
		})
		merged = append(merged, mergeReportGroup(group))
	}
	return merged
}

func mergeReportGroup(group []report) report {
	merged := group[0]
	if len(group) == 1 {
		return merged
	}

	// A logical report spanning distinct installations has no single truthful
	// path. Paths remain available in aliases/installations instead of borrowing
	// the first path and accidentally attaching another installation's error.
	merged.Path = ""
	merged.ScanRoot = ""
	merged.Aliases = nil
	merged.Installations = make([]reportInstallation, 0, len(group))
	providers := map[string]bool{}
	owners := map[string]bool{}
	hosts := map[string]bool{}
	scopes := map[string]bool{}
	executors := map[string]bool{}
	statuses := map[string]bool{}
	drifts := map[string]bool{}
	revisions := map[string]bool{}
	errors := map[string]bool{}
	failureGroups := map[string]bool{}
	failureSources := map[string]bool{}
	failureStages := map[string]bool{}
	for _, item := range group {
		providers[item.Provider] = true
		owners[item.Owner] = true
		hosts[item.Host] = true
		scopes[item.Scope] = true
		executors[item.Executor] = true
		statuses[item.Status] = true
		drifts[item.Drift] = true
		revisions[item.Revision] = true
		if item.Error != "" {
			errors[item.Error] = true
		}
		if item.FailureGroup != "" {
			failureGroups[item.FailureGroup] = true
		}
		if item.FailureSource != "" {
			failureSources[item.FailureSource] = true
		}
		if item.FailureStage != "" {
			failureStages[item.FailureStage] = true
		}
		merged.Installations = append(merged.Installations, reportInstallation{
			Path:            item.Path,
			Host:            item.Host,
			Scope:           item.Scope,
			Provider:        item.Provider,
			Owner:           item.Owner,
			Revision:        item.Revision,
			Drift:           item.Drift,
			Status:          item.Status,
			UpdateAvailable: item.UpdateAvailable,
			Executor:        item.Executor,
			Error:           item.Error,
		})
		for _, path := range append([]string{item.Path}, item.Aliases...) {
			merged.Aliases = appendUnique(merged.Aliases, path)
		}
		for _, evidence := range item.Evidence {
			merged.Evidence = appendUnique(merged.Evidence, evidence)
		}
		merged.UpdateAvailable = merged.UpdateAvailable || item.UpdateAvailable
	}

	if len(providers) > 1 {
		merged.Provider = "multiple"
	}
	if len(owners) > 1 {
		merged.Owner = "multiple"
	}
	if len(hosts) > 1 {
		merged.Host = "multiple"
	}
	if len(scopes) > 1 {
		merged.Scope = "multiple"
	}
	if len(executors) > 1 {
		merged.Executor = "report-only"
	}
	if len(statuses) > 1 {
		if merged.UpdateAvailable {
			merged.Status = "update available (multiple installations)"
		} else {
			merged.Status = "multiple installations"
		}
	}
	if len(drifts) > 1 {
		merged.Drift = "unknown"
		for _, item := range group {
			if item.Drift == "modified" {
				merged.Drift = "modified"
				break
			}
		}
	}
	if len(revisions) > 1 {
		merged.Revision = ""
	}
	if len(errors) > 0 {
		values := slices.Sorted(maps.Keys(errors))
		merged.Error = strings.Join(values, "; ")
	}
	if len(failureGroups) == 1 {
		for value := range maps.Keys(failureGroups) {
			merged.FailureGroup = value
		}
	} else {
		merged.FailureGroup = ""
	}
	if len(failureSources) == 1 {
		for value := range maps.Keys(failureSources) {
			merged.FailureSource = value
		}
	} else {
		merged.FailureSource = ""
	}
	if len(failureStages) == 1 {
		for value := range maps.Keys(failureStages) {
			merged.FailureStage = value
		}
	} else {
		merged.FailureStage = ""
	}
	states := map[string]int{"error": 10, "invalid": 9, "broken": 8, "ambiguous": 7, "modified": 6, "blocked": 5, "outdated": 4, "unknown": 3, "untracked": 3, "pinned": 2, "disabled": 2, "managed": 1, "current": 0}
	merged.State, merged.ReasonCode = "current", ""
	for _, item := range group {
		state, reason := item.State, item.ReasonCode
		if state == "" {
			state, reason = classifyReport(item)
		}
		if states[state] >= states[merged.State] {
			merged.State, merged.ReasonCode = state, reason
		}
	}
	return merged
}

func newReportSink(items []skill, state *trackedState) *reportSink {
	s := &reportSink{}
	for _, item := range items {
		p, o, e := "local-authoring", "user", "report-only"
		var evidence []string
		if entry, ok := state.findSkill(item); ok {
			p, o, e = "skillctl-track-v1", "skillctl", "staged-replacement"
			evidence = []string{entry.Source}
		}
		drift := "unknown"
		if p != "local-authoring" {
			drift = "clean"
		}
		r := reportFor(item, p, o, evidence, drift, "local/untracked (no update source)", false, e, "")
		if entry, ok := state.findSkill(item); ok {
			r.Revision = entry.InstalledHash
		}
		s.reports = append(s.reports, r)
	}
	return s
}

func (s *reportSink) markGit(items []skill, root string) {
	revision, _ := gitOutput(root, "rev-parse", "HEAD")
	for _, item := range items {
		for i := range s.reports {
			if samePath(s.reports[i].Path, item.Path) && s.reports[i].Provider == "local-authoring" {
				s.reports[i].Provider = "git-worktree"
				s.reports[i].Owner = "repository"
				s.reports[i].Executor = "git-ff-only"
				s.reports[i].Evidence = []string{root}
				s.reports[i].Revision = revision
				s.reports[i].Drift = "clean"
			}
		}
	}
}

func (s *reportSink) failure(item skill, message string) {
	s.set([]skill{item}, "failed", "error", "provider_error", false)
	for i := range s.reports {
		if samePath(s.reports[i].Path, item.Path) {
			s.reports[i].Error = message
		}
	}
}

func (s *reportSink) Write(p []byte) (int, error) { return len(p), nil }

func (s *reportSink) set(items []skill, message, state, reason string, available bool) {
	for _, item := range items {
		for i := range s.reports {
			if samePath(s.reports[i].Path, item.Path) {
				r := &s.reports[i]
				r.Status, r.State, r.ReasonCode, r.UpdateAvailable = message, state, reason, available
				if state == "modified" {
					r.Drift = "modified"
				}
				if state == "error" {
					r.Error = message
				}
				break
			}
		}
	}
}
