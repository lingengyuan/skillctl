package app

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type skillListEntry struct {
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	Aliases    []string       `json:"aliases,omitempty"`
	Host       string         `json:"host"`
	Scope      string         `json:"scope"`
	Broken     bool           `json:"broken"`
	LinkTarget string         `json:"linkTarget,omitempty"`
	Bindings   []skillBinding `json:"bindings,omitempty"`
	Error      string         `json:"error,omitempty"`
	ReasonCode string         `json:"reasonCode,omitempty"`
}

// listHosts returns every host consuming the entry, in stable order. Shared
// directories are read by several Agents, and showing only one of them hides
// where a skill is actually exposed.
func listHosts(item skillListEntry) []string {
	if len(item.Bindings) == 0 {
		if item.Host == "" {
			return nil
		}
		return []string{item.Host}
	}
	var hosts []string
	for _, binding := range item.Bindings {
		if binding.Host != "" && !slices.Contains(hosts, binding.Host) {
			hosts = append(hosts, binding.Host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

// listScope returns the scopes the entry is bound to, so a shared entry is not
// misreported as belonging to only one environment.
func listScope(item skillListEntry) string {
	scopes := []string{}
	for _, binding := range item.Bindings {
		if binding.Scope != "" && !slices.Contains(scopes, binding.Scope) {
			scopes = append(scopes, binding.Scope)
		}
	}
	if len(scopes) == 0 {
		return item.Scope
	}
	slices.Sort(scopes)
	return strings.Join(scopes, ",")
}

// listPath prefers the directory an Agent actually reads. The store copy is an
// implementation detail, and printing it makes the listed path useless for the
// one thing a user wants it for: finding the skill.
func listPath(item skillListEntry) string {
	for _, binding := range item.Bindings {
		if binding.Enabled && binding.Path != "" {
			return binding.Path
		}
	}
	return item.Path
}

func writeSkillList(w io.Writer, skills []skill, asJSON bool) error {
	entries := make([]skillListEntry, 0, len(skills))
	for _, item := range skills {
		entries = append(entries, skillListEntry{
			Name:       item.Name,
			Path:       item.Path,
			Aliases:    item.Aliases,
			Host:       item.Host,
			Scope:      item.Scope,
			Broken:     item.Broken,
			LinkTarget: item.LinkTarget,
			Bindings:   item.Bindings,
			Error:      item.Invalid,
			ReasonCode: item.IssueCode,
		})
	}
	if asJSON {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion int              `json:"schemaVersion"`
			Items         []skillListEntry `json:"items"`
		}{SchemaVersion: 1, Items: entries})
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No skills found.")
		return err
	}
	for _, item := range entries {
		status := ""
		if item.Broken {
			status = " broken -> " + item.LinkTarget
		}
		if item.Error != "" {
			status = " invalid: " + item.Error
		}
		hosts := listHosts(item)
		if len(hosts) == 0 {
			hosts = []string{item.Host}
		}
		if _, err := fmt.Fprintf(w, "%s [%s, %s] %s%s\n", item.Name, strings.Join(hosts, "+"), listScope(item), listPath(item), status); err != nil {
			return err
		}
	}
	return nil
}
