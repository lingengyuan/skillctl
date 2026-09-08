package app

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
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
	Local        localObservation      `json:"local"`
	Upstream     upstreamObservation   `json:"upstream"`
	Update       updateEligibility     `json:"update"`
	Validation   documentValidation    `json:"validation"`
}

type inventoryView struct {
	Items            []skillAsset
	Diagnostics      []diagnostic
	roots            []scanRoot
	manifests        []manifest
	managed          []managedRoot
	skills           []skill
	state            *trackedState
	stateErr         error
	catalog          *packageCatalog
	timeout          time.Duration
	scanFailed       bool
	observation      *observation
	checkObservation *checkObservation
	provenance       *provenanceIndex
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
	var plugins []hostPlugin
	var diagnostics []diagnostic
	if supplied, ok := ctx.Value(hostObservationContextKey{}).(*hostObservation); ok {
		plugins, diagnostics = supplied.plugins, supplied.diagnostics
	} else {
		plugins, diagnostics = discoverPlugins(hostCtx, opt, refreshHosts, persist)
	}
	cancel()
	pluginScanRoots, pluginManaged, issues := pluginRoots(plugins)
	roots, managed, diagnostics = append(roots, pluginScanRoots...), append(managed, pluginManaged...), append(diagnostics, issues...)
	var scanErrors bytes.Buffer
	items, _ := scanContext(ctx, roots, &scanErrors)
	failed := false
	for i := range items {
		item := &items[i]
		if item.Portability != "" && installedPlugin(item.Path, plugins) != nil {
			item.Invalid, item.IssueCode = "", ""
		}
		failed = failed || item.Invalid != ""
	}
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
	view.observation = newObservation(ctx)
	if supplied, ok := ctx.Value(hostObservationContextKey{}).(*hostObservation); ok {
		view.observation = supplied.observation
	}
	provenance, manifestErrors := newProvenanceIndex(items, state, manifests, managed)
	view.provenance = provenance
	for path, message := range manifestErrors {
		view.Diagnostics = append(view.Diagnostics, diagnostic{Code: "provider_manifest_invalid", Message: message, Path: path, Level: "error"})
	}
	for _, item := range items {
		if pkg := catalog.byPath(item.Path); pkg != nil {
			continue
		}
		asset := describeSkillObserved(item, provenance, plugins, view.observation)
		if item.Invalid != "" {
			view.Diagnostics = append(view.Diagnostics, diagnostic{Code: item.IssueCode, Message: item.Invalid, Path: item.Path, Level: "error"})
		}
		if item.Portability != "" && item.Invalid == "" {
			view.Diagnostics = append(view.Diagnostics, diagnostic{Code: "skill_portability", Message: item.Portability, Path: item.Path, Level: "warning"})
		}
		if assetMatches(asset, opt) {
			view.Items = append(view.Items, asset)
		}
	}
	for _, pkg := range catalog.Packages {
		if !packageWithinRoots(pkg, roots) {
			continue
		}
		asset := describePackageObserved(pkg, view.observation)
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
			if fsutil.Within(fsutil.BindingPath(root.Path), fsutil.BindingPath(binding.Path)) {
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
	return (len(opt.Hosts) == 0 || slices.Contains(opt.Hosts, binding.Host)) && (len(opt.Scopes) == 0 || slices.Contains(opt.Scopes, binding.Scope)) && (opt.Project == "" || binding.Scope != "project" || fsutil.SamePath(binding.Project, projectDirectory(opt.Project)))
}

func installedPlugin(path string, plugins []hostPlugin) *hostPlugin {
	for _, plugin := range plugins {
		if fsutil.Within(fsutil.PhysicalPath(plugin.Directory), path) {
			return new(plugin)
		}
	}
	return nil
}

func describeSkill(item skill, p *provenanceIndex, plugins []hostPlugin) skillAsset {
	return describeSkillObserved(item, p, plugins, newObservation(context.Background()))
}

func describeSkillObserved(item skill, p *provenanceIndex, plugins []hostPlugin, observed *observation) skillAsset {
	asset := skillAsset{ID: "local-" + stableID(fsutil.PathKey(item.Path)), ContentID: "content-" + stableID(fsutil.PathKey(item.Path)), Name: item.Name, Path: item.Path, Provider: "local-authoring", Owner: "user", State: "untracked", ReasonCode: "missing_update_source", Drift: "unknown", Bindings: slices.Clone(item.Bindings)}
	if len(asset.Bindings) == 0 {
		asset.Bindings = []skillBinding{{Path: item.Path, Host: item.Host, Scope: item.Scope, Enabled: true, Mode: "directory"}}
	}
	if item.Document != nil {
		asset.Description = item.Document.Description
		asset.Validation.DeclaredName = item.Document.Name
	} else if document, err := skilldoc.Read(filepath.Join(item.Path, "SKILL.md")); err == nil {
		asset.Description = document.Description
		asset.Validation.DeclaredName = document.Name
	}
	asset.Validation.Parse, asset.Validation.Portability = "valid", "valid"
	if item.Portability != "" {
		asset.Validation.Portability, asset.Validation.Message = "warning", item.Portability
	}
	for _, plugin := range plugins {
		if fsutil.Within(fsutil.PhysicalPath(plugin.Directory), item.Path) {
			rel, _ := filepath.Rel(fsutil.PhysicalPath(plugin.Directory), item.Path)
			asset.ID = "skill-" + stableID(plugin.Host, plugin.ID, filepath.ToSlash(rel))
			asset.Owner, asset.Provider, asset.State, asset.ReasonCode = plugin.Host, "host-plugin", "managed", "host_managed"
			asset.Revision, asset.Plugin, asset.Evidence = plugin.Version, new(plugin), []string{plugin.Evidence}
			for i := range asset.Bindings {
				asset.Bindings[i].Enabled = plugin.Enabled
			}
			if !plugin.Enabled {
				asset.State, asset.ReasonCode = "disabled", "host_disabled"
			}
			if item.Invalid != "" && item.Portability == "" {
				asset.State, asset.ReasonCode, asset.Error = "invalid", item.IssueCode, item.Invalid
				asset.Validation.Parse = "invalid"
				return completeAsset(asset)
			}
			if item.Broken {
				asset.State, asset.ReasonCode = "broken", "broken_link"
				return completeAsset(asset)
			}
			if plugin.BaselineDigest != "" {
				asset.Digest = plugin.BaselineDigest
				if hash, err := observed.hash(plugin.Directory); err == nil {
					asset.Drift = "clean"
					if hash != plugin.BaselineDigest {
						asset.State, asset.ReasonCode, asset.Drift = "modified", "local_changes", "modified"
					}
				} else {
					asset.State, asset.ReasonCode, asset.Error = "error", "content_hash_failed", err.Error()
				}
			}
			asset.Capabilities = assetCapabilities(asset)
			return completeAsset(asset)
		}
	}
	if item.Invalid != "" {
		asset.State, asset.ReasonCode, asset.Error = "invalid", item.IssueCode, item.Invalid
		if item.Portability == "" {
			asset.Validation.Parse = "invalid"
		}
		return completeAsset(asset)
	}
	if item.Broken {
		asset.State, asset.ReasonCode = "broken", "broken_link"
		return completeAsset(asset)
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
				remote, _ := gitstore.Output(root, "remote", "get-url", "origin")
				if remote != "" {
					source := sourceSpec{Kind: "git", URL: remote, SkillPath: filepath.ToSlash(rel)}
					asset.Source, asset.ID = new(source), sourceIdentity(source)
				}
				asset.Provider, asset.Owner, asset.State, asset.ReasonCode = "git-worktree", "repository", "unknown", "not_checked"
				asset.Revision, _ = gitstore.Output(root, "rev-parse", "HEAD")
				asset.Evidence = []string{root}
			}
		}
	}
	if asset.Digest != "" {
		if hash, err := observed.hash(item.Path); err == nil {
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
	return completeAsset(asset)
}

func describePackage(pkg managedPackage) skillAsset {
	return describePackageObserved(pkg, newObservation(context.Background()))
}

func describePackageObserved(pkg managedPackage, observed *observation) skillAsset {
	asset := skillAsset{ID: pkg.ID, ContentID: "content-" + stableID(fsutil.PathKey(pkg.Directory)), Name: pkg.Name, Path: currentPackageDirectory(pkg), Source: new(pkg.Source), Provider: "skillctl-store", Owner: "skillctl", Revision: pkg.Revision, Digest: pkg.Digest, State: "unknown", ReasonCode: "not_checked", Drift: "clean", Bindings: []skillBinding{}}
	if pkg.External {
		asset.Provider = cmp.Or(pkg.Provider, "external-local")
		asset.Owner = cmp.Or(pkg.Owner, "user")
		if pkg.Provider == "skillctl-track-v1" {
			asset.Owner = "skillctl"
		}
	}
	if doc, err := skilldoc.Read(filepath.Join(asset.Path, "SKILL.md")); err == nil {
		asset.Description = doc.Description
		asset.Validation = documentValidation{Parse: "valid", Portability: "valid", DeclaredName: doc.Name}
		if err := skilldoc.Validate(doc); err != nil {
			asset.Validation.Portability, asset.Validation.Message = "invalid", err.Error()
		}
	} else {
		asset.Validation = documentValidation{Parse: "invalid", Portability: "unknown", Message: err.Error()}
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
	if hash, err := observed.hash(asset.Path); err != nil {
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
			if err != nil || !fsutil.SamePath(real, pkg.Directory) {
				asset.State, asset.ReasonCode = "broken", "binding_changed"
			}
		} else {
			hash, err := observed.hash(binding.Path)
			if err != nil {
				asset.State, asset.ReasonCode = "broken", "binding_missing"
			} else if hash != binding.Digest {
				asset.State, asset.ReasonCode, asset.Drift = "modified", "local_changes", "modified"
			}
		}
	}
	asset.Capabilities = assetCapabilities(asset)
	return completeAsset(asset)
}

func assetCapabilities(asset skillAsset) map[string]capability {
	result := map[string]capability{}
	for _, command := range []string{"check", "diff", "update", "enable", "disable", "remove", "pin", "rollback"} {
		result[command] = capability{Supported: false, Reason: "operation is unavailable for this owner", Unit: "content"}
	}
	if asset.State == "invalid" || asset.State == "ambiguous" || asset.State == "broken" || asset.State == "error" || asset.State == "blocked" {
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
