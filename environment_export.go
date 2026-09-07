package main

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

func runExport(view *inventoryView, opt options, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		return outputResult(commandResult{Command: "export", Diagnostics: []diagnostic{{Code: "export_failed", Level: "error", Message: err.Error()}}}, opt, stdout, stderr, true)
	}
	path := opt.Output
	if path == "" {
		path = opt.File
	}
	if path == "" {
		path = filepath.Join(projectDirectory(opt.Project), "skillctl.toml")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return fail(err)
	}
	selected, err := selectAssets(view.Items, opt.Names, opt.AllMatches || len(opt.Names) == 0)
	if err != nil {
		return fail(err)
	}
	plan := &lifecyclePlan{Command: "export", Affected: []string{}, Changes: []plannedChange{}}
	manifest := environmentManifest{Version: 1, Skills: []desiredSkill{}}
	lock := environmentLock{SchemaVersion: 1, Skills: []lockedSkill{}}
	seen := map[string]bool{}
	for _, asset := range selected {
		if asset.Plugin != nil || asset.Provider == "host-managed" || asset.State == "invalid" || asset.State == "broken" || asset.State == "ambiguous" {
			return fail(fmt.Errorf("%s: %s cannot be exported as a standalone skill; narrow the selection", asset.Name, asset.Provider))
		}
		digest, err := hashDirectory(asset.Path)
		if err != nil {
			return fail(err)
		}
		// Export each content copy independently, including copies with the same
		// upstream identity but different local edits.
		id := asset.ID
		if seen[id] {
			id += "-" + stableID(asset.ContentID)[:8]
		}
		seen[id] = true
		hosts := []string{}
		scope := "project"
		mode := "link"
		for _, binding := range asset.Bindings {
			if !bindingMatches(binding, opt) {
				continue
			}
			hosts = appendUniqueString(hosts, binding.Host)
			if binding.Mode == "copy" {
				mode = "copy"
			}
		}
		if len(opt.Hosts) > 0 {
			hosts = slices.Clone(opt.Hosts)
		}
		if len(opt.Scopes) > 0 {
			scope = opt.Scopes[0]
		}
		if len(hosts) == 0 {
			return fail(fmt.Errorf("%s: no portable target hosts; use --host", asset.Name))
		}
		source := sourceSpec{}
		revision := asset.Revision
		if asset.Source != nil {
			source = *asset.Source
		}
		pkg := view.catalog.byPath(asset.Path)
		portableRemote := pkg != nil && !pkg.External && source.Kind != "local" && !(source.Kind == "archive" && !strings.Contains(source.URL, "://")) && asset.Drift == "clean"
		portableRemote = portableRemote && validatePortableSource(source.URL) == nil
		if !portableRemote {
			relative := filepath.ToSlash(filepath.Join("skillctl-artifacts", id, digest))
			destination := filepath.Join(filepath.Dir(path), filepath.FromSlash(relative))
			if existing, err := hashDirectory(destination); err == nil {
				if existing != digest {
					return fail(fmt.Errorf("export artifact was modified: %s", destination))
				}
			} else {
				change, err := mutation(destination, "directory")
				if err != nil {
					return fail(err)
				}
				if change.Expected != "missing" {
					return fail(fmt.Errorf("export would overwrite %s", destination))
				}
				change.Source, change.ContentDigest = asset.Path, digest
				if err := plan.add(change); err != nil {
					return fail(err)
				}
			}
			source = sourceSpec{Kind: "local", URL: "./" + relative, SkillPath: "."}
			revision = "sha256:" + digest
		}
		item := desiredSkill{ID: id, Name: asset.Name, Source: source.URL, Kind: source.Kind, SkillPath: source.SkillPath, Ref: source.Ref, Hosts: hosts, Scope: scope, Mode: mode}
		manifest.Skills = append(manifest.Skills, item)
		lock.Skills = append(lock.Skills, lockedSkill{ID: id, Name: asset.Name, Definition: desiredDefinition(item), Source: source, Revision: revision, Digest: digest})
	}
	if err := validateEnvironment(&manifest); err != nil {
		return fail(err)
	}
	var content bytes.Buffer
	if err := toml.NewEncoder(&content).Encode(manifest); err != nil {
		return fail(err)
	}
	if err := addFileIfChanged(plan, path, content.Bytes()); err != nil {
		return fail(err)
	}
	lockBytes, err := json.Marshal(lock)
	if err != nil {
		return fail(err)
	}
	if err := addFileIfChanged(plan, filepath.Join(filepath.Dir(path), "skillctl.lock"), append(lockBytes, '\n')); err != nil {
		return fail(err)
	}
	return executePlan(plan, nil, opt, stdout, stderr)
}
