package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type skillListEntry struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Aliases    []string `json:"aliases,omitempty"`
	Host       string   `json:"host"`
	Scope      string   `json:"scope"`
	Broken     bool     `json:"broken"`
	LinkTarget string   `json:"linkTarget,omitempty"`
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
		if _, err := fmt.Fprintf(w, "%s [%s, %s] %s%s\n", item.Name, item.Host, item.Scope, item.Path, status); err != nil {
			return err
		}
	}
	return nil
}
