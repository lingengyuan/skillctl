package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type doctorFinding struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Skill   string `json:"skill,omitempty"`
}

func diagnose(roots []scanRoot, skills []skill, state *trackedState, stateErr error, fix bool) ([]doctorFinding, int, bool) {
	var findings []doctorFinding
	failed := false
	fixed := 0
	add := func(level, code, message, path, skill string) {
		findings = append(findings, doctorFinding{Level: level, Code: code, Message: message, Path: path, Skill: skill})
		if level == "error" {
			failed = true
		}
	}

	for _, root := range roots {
		if _, err := os.Stat(root.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) && !root.Required {
				add("info", "optional_root_missing", "optional skill root is not present", root.Path, "")
				continue
			}
			add("error", "root_unavailable", oneLine(err.Error()), root.Path, "")
		}
	}

	operations, historyErr := readOperations()
	if historyErr != nil {
		add("error", "operation_history_invalid", historyErr.Error(), "", "")
	} else {
		for _, operation := range operations {
			switch operation.State {
			case "preparing", "prepared", "applying", "external_applying", "native_applying", "recovery_required":
				add("error", "operation_recovery_required", operation.ID+": "+operation.State, operation.directory, "")
			}
		}
	}
	byName := map[string][]skill{}
	for _, item := range skills {
		byName[item.Name] = append(byName[item.Name], item)
		if item.Broken {
			add("error", "broken_link", "skill link target is unavailable: "+item.LinkTarget, item.Path, item.Name)
		}
		if item.Invalid != "" {
			add("error", item.IssueCode, item.Invalid, item.Path, item.Name)
		}
	}
	names := slices.Sorted(maps.Keys(byName))
	for _, name := range names {
		items := byName[name]
		canonical := map[string]bool{}
		var paths []string
		for _, item := range items {
			key := fsutil.PathKey(item.Path)
			if canonical[key] {
				continue
			}
			canonical[key] = true
			paths = append(paths, item.Path)
		}
		if len(paths) > 1 {
			slices.Sort(paths)
			add("warning", "duplicate_name", fmt.Sprintf("%d distinct installations share this name: %s", len(paths), strings.Join(paths, ", ")), "", name)
		}
	}

	if stateErr != nil {
		add("error", "tracked_state_invalid", oneLine(stateErr.Error()), "", "")
	} else if state != nil {
		catalog, catalogErr := loadCatalog()
		if catalogErr != nil {
			add("error", "inventory_invalid", catalogErr.Error(), "", "")
		}
		kept := make([]trackedEntry, 0, len(state.Skills))
		changed := false
		for _, entry := range state.Skills {
			if _, err := os.Stat(entry.Path); err == nil {
				kept = append(kept, entry)
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				add("warning", "tracked_path_unreadable", oneLine(err.Error()), entry.Path, "")
				kept = append(kept, entry)
				continue
			}
			if catalog != nil {
				if pkg := catalog.byPath(entry.Path); pkg != nil && len(pkg.Bindings) > 0 && currentPackageDirectory(*pkg) != pkg.Directory {
					kept = append(kept, entry)
					add("info", "tracked_installation_disabled", "source retained for disabled installation", entry.Path, "")
					continue
				}
			}
			if fix {
				changed = true
				fixed++
				add("fixed", "stale_tracked_source_removed", "removed stale tracked-source entry", entry.Path, "")
				continue
			}
			add("warning", "stale_tracked_source", "tracked-source entry points to a missing installation; run doctor --fix to remove it", entry.Path, "")
			kept = append(kept, entry)
		}
		if changed {
			state.Skills = kept
			change, err := mutation(state.path, "file")
			if err == nil {
				change.Data, err = trackedStateBytes(state)
			}
			if err == nil {
				var operation *operationRecord
				operation, err = prepareOperation("doctor remove stale sources", []pathMutation{change}, []string{state.path})
				if err == nil {
					err = operation.apply()
				}
			}
			if err != nil {
				fixed = 0
				add("error", "tracked_state_save_failed", oneLine(err.Error()), state.path, "")
			}
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		add("error", "git_missing", "Git was not found in PATH", "", "")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		add("info", "gh_missing", "GitHub CLI was not found; gh-managed skills remain report-only", "", "")
	}
	if _, err := exec.LookPath("skills"); err != nil {
		if _, npxErr := exec.LookPath("npx"); npxErr != nil {
			add("info", "vercel_cli_missing", "Vercel Skills CLI and npx were not found; Vercel-managed skills cannot be updated", "", "")
		}
	}

	if configDir, err := skillctlDirectory(); err == nil && !fix {
		lockPath := filepath.Join(configDir, "operation.lock")
		if operationLockActive(lockPath) {
			add("warning", "active_operation_lock", "another skillctl operation is running", lockPath, "")
		}
	}

	if len(findings) == 0 {
		add("ok", "healthy", "no local problems found", "", "")
	}
	return findings, fixed, failed
}

func writeDoctorReport(w io.Writer, findings []doctorFinding, fixed int, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion int             `json:"schemaVersion"`
			Fixed         int             `json:"fixed"`
			Findings      []doctorFinding `json:"findings"`
		}{SchemaVersion: 1, Fixed: fixed, Findings: findings})
	}
	for _, finding := range findings {
		context := ""
		if finding.Skill != "" {
			context += " skill=" + finding.Skill
		}
		if finding.Path != "" {
			context += " path=" + finding.Path
		}
		if _, err := fmt.Fprintf(w, "%s %s:%s %s\n", strings.ToUpper(finding.Level), finding.Code, context, finding.Message); err != nil {
			return err
		}
	}
	if fixed > 0 {
		_, err := fmt.Fprintf(w, "Fixed %d issue(s).\n", fixed)
		return err
	}
	return nil
}
