package main

import (
	"fmt"
	"io"
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

func duplicateName(reports []report, name string) bool {
	n := 0
	for _, r := range reports {
		if r.Identity == name {
			n++
		}
	}
	return n > 1
}

type reportSink struct {
	reports []report
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
