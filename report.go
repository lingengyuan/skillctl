package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// report is deliberately a value object: rendering does not need to know how
// an adapter discovered ownership.
type report struct {
	Identity        string   `json:"identity"`
	Path            string   `json:"path"`
	Aliases         []string `json:"aliases,omitempty"`
	ScanRoot        string   `json:"scanRoot"`
	Host            string   `json:"host"`
	Scope           string   `json:"scope"`
	Provider        string   `json:"provider"`
	Owner           string   `json:"owner"`
	Evidence        []string `json:"evidence"`
	Revision        string   `json:"revision,omitempty"`
	Drift           string   `json:"drift"`
	Status          string   `json:"status"`
	UpdateAvailable bool     `json:"updateAvailable"`
	Executor        string   `json:"executor"`
	Error           string   `json:"error,omitempty"`
}

func reportFor(item skill, provider, owner string, evidence []string, drift, status string, available bool, executor, err string) report {
	return report{Identity: item.Name, Path: item.Path, Aliases: item.Aliases, ScanRoot: item.ScanRoot, Host: item.Host, Scope: item.Scope, Provider: provider, Owner: owner, Evidence: evidence, Drift: drift, Status: status, UpdateAvailable: available, Executor: executor, Error: err}
}

func printReport(w io.Writer, r report, showPath bool) {
	name := r.Identity
	if showPath || r.Status == "ambiguous provenance" || strings.HasPrefix(r.Status, "broken") || r.Error != "" {
		name += " [" + r.Path + "]"
	}
	status := r.Status
	if r.Error != "" && !strings.Contains(status, r.Error) {
		status += " (" + r.Error + ")"
	}
	fmt.Fprintf(w, "%s [%s, %s]: %s\n", name, r.Provider, r.Owner, status)
}

type reportSink struct {
	reports []report
}

func mergeReportsByIdentity(reports []report) []report {
	groups := make(map[string][]report)
	for _, item := range reports {
		groups[item.Identity] = append(groups[item.Identity], item)
	}
	identities := make([]string, 0, len(groups))
	for identity := range groups {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	merged := make([]report, 0, len(identities))
	for _, identity := range identities {
		group := groups[identity]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Path == group[j].Path {
				return group[i].Provider < group[j].Provider
			}
			return group[i].Path < group[j].Path
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

	merged.Aliases = nil
	providers := map[string]bool{}
	owners := map[string]bool{}
	executors := map[string]bool{}
	statuses := map[string]bool{}
	drifts := map[string]bool{}
	revisions := map[string]bool{}
	errors := map[string]bool{}
	for _, item := range group {
		providers[item.Provider] = true
		owners[item.Owner] = true
		executors[item.Executor] = true
		statuses[item.Status] = true
		drifts[item.Drift] = true
		revisions[item.Revision] = true
		if item.Error != "" {
			errors[item.Error] = true
		}
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
		values := make([]string, 0, len(errors))
		for value := range errors {
			values = append(values, value)
		}
		sort.Strings(values)
		merged.Error = strings.Join(values, "; ")
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
	s.set([]skill{item}, "failed")
	for i := range s.reports {
		if samePath(s.reports[i].Path, item.Path) {
			s.reports[i].Error = message
		}
	}
}
func (s *reportSink) Write(p []byte) (int, error) { return len(p), nil }
func (s *reportSink) set(items []skill, message string) {
	for _, item := range items {
		for i := range s.reports {
			if samePath(s.reports[i].Path, item.Path) {
				s.reports[i].Status = message
				s.reports[i].UpdateAvailable = strings.HasPrefix(message, "update available")
				if strings.Contains(message, "modified") || strings.Contains(message, "dirty") {
					s.reports[i].Drift = "modified"
				}
				if strings.HasPrefix(message, "failed") {
					s.reports[i].Error = message
				}
				break
			}
		}
	}
}
