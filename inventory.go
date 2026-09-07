package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type capability struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

type skillAsset struct {
	ID           string                `json:"id"`
	ContentID    string                `json:"contentId"`
	Name         string                `json:"name"`
	Description  string                `json:"description,omitempty"`
	Path         string                `json:"path"`
	Source       *sourceSpec           `json:"source,omitempty"`
	Provider     string                `json:"provider"`
	Owner        string                `json:"owner"`
	Revision     string                `json:"revision,omitempty"`
	Digest       string                `json:"digest,omitempty"`
	State        string                `json:"state"`
	ReasonCode   string                `json:"reasonCode,omitempty"`
	Drift        string                `json:"drift"`
	Bindings     []skillBinding        `json:"bindings"`
	Capabilities map[string]capability `json:"capabilities"`
	Evidence     []string              `json:"evidence,omitempty"`
	Plugin       *hostPlugin           `json:"plugin,omitempty"`
	Error        string                `json:"error,omitempty"`
}

type inventoryView struct {
	Items       []skillAsset
	Diagnostics []diagnostic
	roots       []scanRoot
	manifests   []manifest
	managed     []managedRoot
	skills      []skill
	state       *trackedState
	stateErr    error
	catalog     *packageCatalog
	timeout     time.Duration
	scanFailed  bool
}

func loadInventory(ctx context.Context, opt options, refreshHosts, persist bool) (*inventoryView, error) {
	roots, manifests, managed, timeout, err := loadConfig(opt.ConfigPath)
	if err != nil {
		return nil, err
	}
	if opt.Timeout > 0 {
		timeout = opt.Timeout
	}
	roots = augmentRoots(roots, opt)
	catalog, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	if len(opt.Paths) == 0 && opt.ConfigPath == "" {
		roots = append(roots, catalog.roots()...)
	}
	hostCtx, cancel := context.WithTimeout(ctx, timeout)
	plugins, diagnostics := discoverPlugins(hostCtx, opt, refreshHosts, persist)
	cancel()
	pluginScanRoots, pluginManaged, issues := pluginRoots(plugins)
	roots, managed, diagnostics = append(roots, pluginScanRoots...), append(managed, pluginManaged...), append(diagnostics, issues...)
	var scanErrors bytes.Buffer
	items, failed := scan(roots, &scanErrors)
	state, stateErr := loadTrackedState()
	if stateErr != nil {
		if !opt.allowInvalidState {
			return nil, stateErr
		}
		path, err := stateFile("sources.json")
		if err != nil {
			return nil, err
		}
		state = &trackedState{Version: 1, path: path}
		diagnostics = append(diagnostics, diagnostic{Code: "tracked_state_invalid", Level: "error", Path: path, Message: stateErr.Error()})
	}
	state.readOnly = !persist || stateErr != nil
	view := &inventoryView{roots: roots, manifests: manifests, managed: managed, skills: items, state: state, stateErr: stateErr, catalog: catalog, timeout: timeout, scanFailed: failed, Diagnostics: diagnostics, Items: []skillAsset{}}
	provenance, manifestErrors := newProvenanceIndex(items, state, manifests, managed)
	for path, message := range manifestErrors {
		view.Diagnostics = append(view.Diagnostics, diagnostic{Code: "provider_manifest_invalid", Message: message, Path: path, Level: "error"})
	}
	for _, item := range items {
		if pkg := catalog.byPath(item.Path); pkg != nil {
			continue
		}
		asset := describeSkill(item, provenance, plugins)
		if item.Invalid != "" {
			view.Diagnostics = append(view.Diagnostics, diagnostic{Code: item.IssueCode, Message: item.Invalid, Path: item.Path, Level: "error"})
		}
		if assetMatches(asset, opt) {
			view.Items = append(view.Items, asset)
		}
	}
	for _, pkg := range catalog.Packages {
		if !packageWithinRoots(pkg, roots) {
			continue
		}
		asset := describePackage(pkg)
		if assetMatches(asset, opt) {
			view.Items = append(view.Items, asset)
		}
	}

	for _, asset := range view.Items {
		if asset.State == "broken" {
			view.scanFailed = true
			view.Diagnostics = append(view.Diagnostics, diagnostic{Code: asset.ReasonCode, Message: "installed content or binding is unavailable", Path: asset.Path, Level: "error"})
		}
	}
	slices.SortFunc(view.Items, func(a, b skillAsset) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID), strings.Compare(a.Path, b.Path))
	})
	return view, nil
}

func packageWithinRoots(pkg managedPackage, roots []scanRoot) bool {
	for _, binding := range pkg.Bindings {
		for _, root := range roots {
			if within(canonicalLocation(root.Path), canonicalLocation(binding.Path)) {
				return true
			}
		}
	}
	return false
}

func assetMatches(asset skillAsset, opt options) bool {
	if len(opt.Hosts) == 0 && len(opt.Scopes) == 0 {
		return true
	}
	for _, binding := range asset.Bindings {
		if bindingMatches(binding, opt) {
			return true
		}
	}
	return false
}

func bindingMatches(binding skillBinding, opt options) bool {
	return (len(opt.Hosts) == 0 || slices.Contains(opt.Hosts, binding.Host)) && (len(opt.Scopes) == 0 || slices.Contains(opt.Scopes, binding.Scope)) && (opt.Project == "" || binding.Scope != "project" || samePath(binding.Project, projectDirectory(opt.Project)))
}

func describeSkill(item skill, p *provenanceIndex, plugins []hostPlugin) skillAsset {
	asset := skillAsset{ID: "local-" + stableID(canonicalPathKey(item.Path)), ContentID: "content-" + stableID(canonicalPathKey(item.Path)), Name: item.Name, Path: item.Path, Provider: "local-authoring", Owner: "user", State: "untracked", ReasonCode: "missing_update_source", Drift: "unknown", Bindings: slices.Clone(item.Bindings)}
	if len(asset.Bindings) == 0 {
		asset.Bindings = []skillBinding{{Path: item.Path, Host: item.Host, Scope: item.Scope, Enabled: true, Mode: "directory"}}
	}
	if document, err := readSkillDocument(filepath.Join(item.Path, "SKILL.md")); err == nil {
		asset.Description = document.Description
	}
	if item.Invalid != "" {
		asset.State, asset.ReasonCode, asset.Error = "invalid", item.IssueCode, item.Invalid
		asset.Capabilities = assetCapabilities(asset)
		return asset
	}
	if item.Broken {
		asset.State, asset.ReasonCode = "broken", "broken_link"
		asset.Capabilities = assetCapabilities(asset)
		return asset
	}
	for _, plugin := range plugins {
		if within(physicalLocation(plugin.Directory), item.Path) {
			rel, _ := filepath.Rel(physicalLocation(plugin.Directory), item.Path)
			asset.ID = "skill-" + stableID(plugin.Host, plugin.ID, filepath.ToSlash(rel))
			asset.Owner, asset.Provider, asset.State, asset.ReasonCode = plugin.Host, "host-plugin", "managed", "host_managed"
			asset.Revision, asset.Plugin, asset.Evidence = plugin.Version, new(plugin), []string{plugin.Evidence}
			for i := range asset.Bindings {
				asset.Bindings[i].Enabled = plugin.Enabled
			}
			if !plugin.Enabled {
				asset.State, asset.ReasonCode = "disabled", "host_disabled"
			}
			if plugin.BaselineDigest != "" {
				asset.Digest = plugin.BaselineDigest
				if hash, err := hashDirectory(plugin.Directory); err == nil {
					asset.Drift = "clean"
					if hash != plugin.BaselineDigest {
						asset.State, asset.ReasonCode, asset.Drift = "modified", "local_changes", "modified"
					}
				}
			}
			asset.Capabilities = assetCapabilities(asset)
			return asset
		}
	}
	claims := p.claims(item)
	switch {
	case claims.count() > 1:
		asset.Provider, asset.Owner, asset.State, asset.ReasonCode = "ambiguous", "unknown", "ambiguous", "ambiguous_provenance"
	case claims.managedOwner != "" || claims.hasHost:
		asset.Provider, asset.Owner, asset.State, asset.ReasonCode = "host-managed", "host", "managed", "host_managed"
		asset.Evidence = append(claims.managedEvidence, claims.host.Evidence...)
		asset.Revision = claims.host.Revision
	case claims.hasTracked:
		source := sourceSpec{Kind: "git", URL: claims.tracked.Source, SkillPath: claims.tracked.SkillPath, Ref: claims.tracked.Ref}
		asset.Source, asset.ID, asset.Provider, asset.Owner = new(source), sourceIdentity(source), "skillctl-track-v1", "skillctl"
		asset.State, asset.ReasonCode, asset.Digest = "unknown", "not_checked", claims.tracked.InstalledHash
		asset.Revision = "sha256:" + claims.tracked.InstalledHash
		asset.Evidence = []string{p.state.path}
	case claims.hasVercel:
		entry := claims.vercel.Entry
		source := sourceSpec{Kind: entry.SourceType, URL: entry.SourceURL, SkillPath: entry.SkillPath, Ref: entry.Ref}
		if entry.SourceType == "github" {
			source.Kind = "git"
		}
		if entry.SourceType == "well-known" {
			source.URL, source.SkillPath, source.Artifact = entry.SourceBaseURL, claims.vercel.Name, entry.SourceURL
		}
		asset.Source, asset.ID, asset.Provider, asset.Owner = new(source), sourceIdentity(source), "vercel-skills-lock-v3", "provider"
		asset.Revision, asset.State, asset.ReasonCode = cmp.Or(entry.WellKnownDigest, entry.SkillFolderHash), "unknown", "not_checked"
		asset.Evidence = claims.vercelEvidence
	case claims.gh.Found:
		asset.Provider, asset.Owner = "gh-skill", "provider"
		if claims.gh.Err != nil {
			asset.State, asset.ReasonCode, asset.Error = "invalid", "invalid_provider_metadata", claims.gh.Err.Error()
			break
		}
		claim := claims.gh.Claim
		source := sourceSpec{Kind: "git", URL: ghRepositoryURL(claim.Repository), SkillPath: claim.SkillPath, Ref: claim.Ref}
		if claim.LocalPath != "" {
			source.Kind, source.URL = "local", claim.LocalPath
		}
		asset.Source, asset.ID, asset.Revision = new(source), sourceIdentity(source), claim.TreeSHA
		asset.State, asset.ReasonCode = "unknown", "not_checked"
		if claim.Pinned {
			asset.State, asset.ReasonCode = "pinned", "pinned_revision"
		}
	default:
		if root, found := findGitRoot(item.Path, map[string]gitRootResult{}); found {
			rel, err := filepath.Rel(root, item.Path)
			if err == nil && gitTracks(root, filepath.Join(rel, "SKILL.md")) {
				remote, _ := gitOutput(root, "remote", "get-url", "origin")
				if remote != "" {
					source := sourceSpec{Kind: "git", URL: remote, SkillPath: filepath.ToSlash(rel)}
					asset.Source, asset.ID = new(source), sourceIdentity(source)
				}
				asset.Provider, asset.Owner, asset.State, asset.ReasonCode = "git-worktree", "repository", "unknown", "not_checked"
				asset.Revision, _ = gitOutput(root, "rev-parse", "HEAD")
				asset.Evidence = []string{root}
			}
		}
	}
	if asset.Digest != "" {
		if hash, err := hashDirectory(item.Path); err == nil {
			asset.Drift = "clean"
			if hash != asset.Digest {
				asset.Drift, asset.State, asset.ReasonCode = "modified", "modified", "local_changes"
			}
		}
	}
	if asset.Source != nil {
		if err := validateSourceURL(asset.Source.URL); err != nil {
			asset.Source = nil
			asset.Error = err.Error()
			asset.State, asset.ReasonCode = "blocked", "credential_in_source"
		}
	}
	asset.Capabilities = assetCapabilities(asset)
	return asset
}

func describePackage(pkg managedPackage) skillAsset {
	asset := skillAsset{ID: pkg.ID, ContentID: "content-" + stableID(canonicalPathKey(pkg.Directory)), Name: pkg.Name, Path: currentPackageDirectory(pkg), Source: new(pkg.Source), Provider: "skillctl-store", Owner: "skillctl", Revision: pkg.Revision, Digest: pkg.Digest, State: "unknown", ReasonCode: "not_checked", Drift: "clean", Bindings: []skillBinding{}}
	if pkg.External {
		asset.Provider = cmp.Or(pkg.Provider, "external-local")
		asset.Owner = cmp.Or(pkg.Owner, "user")
		if pkg.Provider == "skillctl-track-v1" {
			asset.Owner = "skillctl"
		}
	}
	if doc, err := readSkillDocument(filepath.Join(asset.Path, "SKILL.md")); err == nil {
		asset.Description = doc.Description
	}
	active := false
	for _, binding := range pkg.Bindings {
		asset.Bindings = append(asset.Bindings, binding.skillBinding)
		active = active || binding.Enabled
	}
	if !active {
		asset.State, asset.ReasonCode = "disabled", "disabled_by_user"
	}
	if pkg.Pin != "" {
		asset.State, asset.ReasonCode = "pinned", "pinned_revision"
	}
	if hash, err := hashDirectory(asset.Path); err != nil {
		asset.State, asset.ReasonCode, asset.Error = "broken", "content_unavailable", err.Error()
	} else if hash != pkg.Digest {
		asset.State, asset.ReasonCode, asset.Drift = "modified", "local_changes", "modified"
	}
	for _, binding := range pkg.Bindings {
		if !binding.Enabled {
			continue
		}
		if binding.Mode == "link" {
			real, err := filepath.EvalSymlinks(binding.Path)
			if err != nil || !samePath(real, pkg.Directory) {
				asset.State, asset.ReasonCode = "broken", "binding_changed"
			}
		} else {
			hash, err := hashDirectory(binding.Path)
			if err != nil {
				asset.State, asset.ReasonCode = "broken", "binding_missing"
			} else if hash != binding.Digest {
				asset.State, asset.ReasonCode, asset.Drift = "modified", "local_changes", "modified"
			}
		}
	}
	asset.Capabilities = assetCapabilities(asset)
	return asset
}

func assetCapabilities(asset skillAsset) map[string]capability {
	result := map[string]capability{}
	for _, command := range []string{"check", "diff", "update", "enable", "disable", "remove", "pin", "rollback"} {
		result[command] = capability{Supported: false, Reason: "operation is unavailable for this owner", Unit: "content"}
	}
	if asset.State == "invalid" || asset.State == "ambiguous" || asset.State == "broken" {
		return result
	}
	result["check"] = capability{Supported: true, Unit: "content"}
	if asset.Plugin != nil {
		result["check"] = capability{Supported: true, Unit: "plugin"}
		for _, command := range []string{"update", "enable", "disable", "remove"} {
			supported := asset.Plugin.Host == "claude" || (asset.Plugin.Host == "codex" && command == "remove")
			reason := "host does not expose this operation through a supported CLI"
			if supported {
				reason = "requires --package; affects the entire plugin and its components"
			}
			if command == "update" && asset.Drift == "modified" {
				supported = false
				reason = "local modifications must be preserved"
			}
			result[command] = capability{Supported: supported, Reason: reason, Unit: "plugin"}
		}
		return result
	}
	if asset.Provider == "host-managed" {
		return result
	}
	if asset.Source != nil {
		result["diff"] = capability{Supported: true, Unit: "content"}
		result["update"] = capability{Supported: true, Unit: "content"}
	}
	if asset.Provider == "local-authoring" || asset.Provider == "skillctl-store" || asset.Provider == "external-local" || asset.Provider == "skillctl-track-v1" {
		for _, command := range []string{"enable", "disable", "remove", "pin", "rollback"} {
			result[command] = capability{Supported: true, Unit: "content"}
		}
	}
	if slices.Contains([]string{"git-worktree", "vercel-skills-lock-v3", "gh-skill"}, asset.Provider) {
		for _, command := range []string{"enable", "disable", "remove"} {
			result[command] = capability{Supported: true, Unit: "binding"}
		}
		if asset.Provider != "git-worktree" {
			result["pin"] = capability{Supported: true, Unit: "content"}
			result["rollback"] = capability{Supported: true, Unit: "content"}
		}
	}
	if asset.Provider == "git-worktree" {
		for _, command := range []string{"disable", "remove"} {
			result[command] = capability{Supported: true, Unit: "binding", Reason: "only Agent links can be removed; repository directories stay owned by Git"}
		}
		result["update"] = capability{Supported: true, Unit: "repository"}
	}
	if asset.State == "modified" {
		result["update"] = capability{Supported: false, Reason: "local modifications must be preserved", Unit: "content"}
	}
	return result
}

func selectAssets(all []skillAsset, names []string, allMatches bool) ([]skillAsset, error) {
	if len(names) == 0 {
		return all, nil
	}
	var selected []skillAsset
	seen := map[string]bool{}
	for _, name := range names {
		var matches []skillAsset
		ids := map[string]bool{}
		for _, asset := range all {
			if asset.ID == name || asset.ContentID == name || asset.Name == name {
				matches = append(matches, asset)
				ids[asset.ID] = true
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("skill not found: %s", name)
		}
		if len(ids) > 1 && !allMatches {
			return nil, fmt.Errorf("skill name %q is ambiguous; use its id, --host/--scope/--path, or --all-matches", name)
		}
		for _, asset := range matches {
			if !seen[asset.ContentID] {
				selected = append(selected, asset)
				seen[asset.ContentID] = true
			}
		}
	}
	return selected, nil
}
