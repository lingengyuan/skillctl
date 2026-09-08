package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
	"os"
	"path/filepath"
	"slices"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type lifecyclePlan struct {
	Command   string          `json:"command"`
	Affected  []string        `json:"affected"`
	Changes   []plannedChange `json:"changes"`
	mutations []pathMutation
	updated   map[string]bool
}

type plannedChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
}

func (p *lifecyclePlan) add(change pathMutation) error {
	for _, old := range p.mutations {
		if fsutil.SamePath(old.Path, change.Path) {
			if old.Kind == change.Kind && old.Target == change.Target && old.Source == change.Source && string(old.Data) == string(change.Data) {
				return nil
			}
			return fmt.Errorf("conflicting operations target %s", change.Path)
		}
	}
	p.mutations = append(p.mutations, change)
	p.Changes = append(p.Changes, plannedChange{Path: change.Path, Action: change.Kind, Target: change.Target})
	return nil
}

func (p *lifecyclePlan) finish(catalog *packageCatalog) error {
	previous, err := loadCatalog()
	if err != nil {
		return err
	}
	if string(catalogBytes(previous)) == string(catalogBytes(catalog)) {
		return nil
	}
	change, err := catalog.mutation()
	if err != nil {
		return err
	}
	return p.add(change)
}

func defaultInstallScope(opt options) (string, error) {
	if len(opt.Scopes) > 1 {
		return "", fmt.Errorf("installation requires exactly one scope")
	}
	if len(opt.Scopes) == 1 {
		if opt.Scopes[0] != "user" && opt.Scopes[0] != "project" {
			return "", fmt.Errorf("installation scope must be user or project")
		}
		return opt.Scopes[0], nil
	}
	if opt.Project != "" {
		return "project", nil
	}
	return "user", nil
}

func targetBindings(opt options, roots []scanRoot, name string) ([]skillBinding, error) {
	if !skilldoc.ValidName(name) || len(name) > 64 {
		return nil, fmt.Errorf("invalid skill directory name: %s", name)
	}
	scope, err := defaultInstallScope(opt)
	if err != nil {
		return nil, err
	}
	hosts := slices.Clone(opt.Hosts)
	if len(hosts) == 0 {
		for _, host := range agentHosts() {
			detectionPath := filepath.Dir(host.UserPath)
			if host.Name == "codex" {
				detectionPath = codexHomePath()
			}
			if _, err := os.Stat(detectionPath); err == nil {
				hosts = append(hosts, host.Name)
			}
		}
		if len(hosts) == 0 {
			return nil, fmt.Errorf("no Agent detected; choose one with --host")
		}
	}
	var result []skillBinding
	project := ""
	if scope == "project" {
		project = projectDirectory(opt.Project)
	}
	for _, host := range hosts {
		var candidates []scanRoot
		for _, root := range roots {
			if root.Host == host && root.Scope == scope && (scope != "project" || root.Project == "" || fsutil.SamePath(root.Project, project)) {
				candidates = append(candidates, root)
			}
		}
		preferred := ""
		for _, known := range agentHosts() {
			if known.Name == host {
				preferred = known.UserPath
				if scope == "project" {
					preferred = filepath.Join(project, filepath.FromSlash(known.ProjectPath))
				}
				break
			}
		}
		var root string
		for _, candidate := range candidates {
			if fsutil.SamePath(candidate.Path, preferred) {
				root = candidate.Path
				break
			}
		}
		if root == "" && len(candidates) == 1 {
			root = candidates[0].Path
		}
		if root == "" && len(candidates) > 1 {
			return nil, fmt.Errorf("multiple roots for %s/%s; select a configuration with one install target", host, scope)
		}
		if root == "" {
			root = preferred
		}
		if root == "" {
			return nil, fmt.Errorf("unknown Agent %q; configure a root for this host", host)
		}
		mode := "link"
		if opt.Copy || !directoryLinksAvailable() {
			mode = "copy"
		}
		path := filepath.Join(root, name)
		result = append(result, skillBinding{Path: path, Host: host, Scope: scope, Project: project, Enabled: true, Mode: mode})
		// Expose implicit consumers of shared directories in the plan too.
		for _, candidate := range roots {
			if fsutil.SamePath(candidate.Path, root) && candidate.Host != host && candidate.Scope == scope {
				binding := skillBinding{Path: path, Host: candidate.Host, Scope: scope, Project: project, Enabled: true, Mode: mode}
				if !slices.Contains(result, binding) {
					result = append(result, binding)
				}
			}
		}
	}
	return slices.CompactFunc(result, func(a, b skillBinding) bool { return a == b }), nil
}

func directoryLinksAvailable() bool {
	dir, err := os.MkdirTemp("", "skillctl-link-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	return os.Symlink(dir, filepath.Join(dir, "probe")) == nil
}

func planInstallPackage(plan *lifecyclePlan, catalog *packageCatalog, prepared preparedPackage, bindings []skillBinding, environment string) error {
	id := sourceIdentity(prepared.Source)
	existing := catalog.find(id)
	directory, err := packageStorePath(id, prepared.Digest)
	for _, host := range agentHosts() {
		if fsutil.Within(fsutil.PhysicalPath(host.UserPath), fsutil.PhysicalPath(directory)) {
			return fmt.Errorf("shared store must be outside Agent skill directories; move SKILLCTL_HOME")
		}
	}
	for _, binding := range bindings {
		if fsutil.Within(fsutil.PhysicalPath(filepath.Dir(binding.Path)), fsutil.PhysicalPath(directory)) {
			return fmt.Errorf("shared store must be outside Agent skill directories; move SKILLCTL_HOME")
		}
	}
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Digest != prepared.Digest {
			return fmt.Errorf("%s is already installed at another revision; use update or install with a distinct --ref", prepared.Name)
		}
		if !plan.updated[existing.ID] {
			if err := verifyPackageUnmodified(*existing); err != nil {
				return err
			}
		}
		directory = existing.Directory
	}
	if hash, err := fsutil.HashDirectory(directory); err == nil {
		if hash != prepared.Digest {
			return fmt.Errorf("cached content was modified: %s", directory)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		change, err := mutation(directory, "directory")
		if err != nil {
			return err
		}
		change.Source, change.ContentDigest = prepared.Directory, prepared.Digest
		if err := plan.add(change); err != nil {
			return err
		}
	}
	pkg := managedPackage{ID: id, Name: prepared.Name, Source: prepared.Source, Revision: prepared.Revision, Digest: prepared.Digest, Directory: directory, Bindings: []managedBinding{}}
	if existing != nil {
		pkg = *existing
		pkg.Bindings = slices.Clone(existing.Bindings)
	}
	for _, binding := range bindings {
		index := slices.IndexFunc(pkg.Bindings, func(b managedBinding) bool {
			return fsutil.SamePath(b.Path, binding.Path) && b.Host == binding.Host && b.Scope == binding.Scope
		})
		if index >= 0 && pkg.Bindings[index].Enabled {
			if environment == "" {
				pkg.Bindings[index].Manual = true
			}
			continue
		}
		change, err := mutation(binding.Path, binding.Mode)
		if err != nil {
			return err
		}

		needsChange := change.Expected == "missing"
		if !needsChange {
			compatible := false
			for _, known := range pkg.Bindings {
				if fsutil.SamePath(known.Path, binding.Path) && known.Enabled {
					compatible = true
				}
			}
			if !compatible {
				previous := catalog.byPath(binding.Path)
				if environment == "" || previous == nil || previous.ID == pkg.ID {
					return fmt.Errorf("installation would overwrite an existing path: %s", binding.Path)
				}
				if err := verifyPackageUnmodified(*previous); err != nil {
					return err
				}
				for _, other := range previous.Bindings {
					if !fsutil.SamePath(other.Path, binding.Path) {
						continue
					}
					if other.Manual || !slices.Contains(other.Environments, environment) || len(other.Environments) > 1 {
						return fmt.Errorf("target %s is also used outside this environment", binding.Path)
					}
				}
				for i := range previous.Bindings {
					other := &previous.Bindings[i]
					if fsutil.SamePath(other.Path, binding.Path) {
						other.Enabled = false
						other.Environments = slices.DeleteFunc(other.Environments, func(value string) bool { return value == environment })
					}
				}
				needsChange = true
			}
		}
		if needsChange {
			if binding.Mode == "copy" {
				change.Kind, change.Source, change.ContentDigest = "directory", prepared.Directory, prepared.Digest
			} else {
				change.Kind, change.Target = "link", directory
			}
			if err := plan.add(change); err != nil {
				return err
			}
		}

		record := managedBinding{skillBinding: binding, Digest: prepared.Digest, Manual: environment == ""}
		if environment != "" {
			record.Environments = []string{environment}
		}
		if index >= 0 {
			record.Manual = pkg.Bindings[index].Manual || record.Manual
			record.Environments = append(record.Environments, pkg.Bindings[index].Environments...)
		}
		if index >= 0 {
			pkg.Bindings[index] = record
		} else {
			pkg.Bindings = append(pkg.Bindings, record)
		}
		plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
	}
	if existing == nil {
		catalog.Packages = append(catalog.Packages, pkg)
	} else {
		*existing = pkg
	}
	return nil
}

func verifyPackageUnmodified(pkg managedPackage) error {
	directory := currentPackageDirectory(pkg)
	hash, err := fsutil.HashDirectory(directory)
	if err != nil {
		return fmt.Errorf("read installed %s: %w", pkg.Name, err)
	}
	if hash != pkg.Digest {
		return fmt.Errorf("local modifications block changes to %s (%s)", pkg.Name, directory)
	}
	for _, binding := range pkg.Bindings {
		if !binding.Enabled {
			continue
		}
		if binding.Mode == "link" {
			real, err := filepath.EvalSymlinks(binding.Path)
			if err != nil || !fsutil.SamePath(real, pkg.Directory) {
				return fmt.Errorf("binding changed: %s", binding.Path)
			}
		} else {
			hash, err := fsutil.HashDirectory(binding.Path)
			if err != nil || hash != binding.Digest {
				return fmt.Errorf("local modifications or missing content at %s", binding.Path)
			}
		}
	}
	return nil
}

func registerExternal(catalog *packageCatalog, asset skillAsset) (*managedPackage, error) {
	if pkg := catalog.byPath(asset.Path); pkg != nil {
		return pkg, nil
	}
	if !slices.Contains([]string{"local-authoring", "skillctl-track-v1", "git-worktree", "vercel-skills-lock-v3", "gh-skill"}, asset.Provider) {
		return nil, fmt.Errorf("%s is managed by %s; this operation requires its native adapter", asset.Name, asset.Provider)
	}
	digest, err := fsutil.HashDirectory(asset.Path)
	if err != nil {
		return nil, err
	}
	source := sourceSpec{Kind: "local", URL: asset.Path, SkillPath: "."}
	if asset.Source != nil {
		source = *asset.Source
	}
	pkg := managedPackage{ID: sourceIdentity(source), Name: asset.Name, Source: source, Revision: "sha256:" + digest, Digest: digest, Directory: asset.Path, External: true, Provider: asset.Provider, Owner: asset.Owner, Bindings: []managedBinding{}}
	if existing := catalog.find(pkg.ID); existing != nil {
		pkg.ID += "-" + stableID(asset.Path)[:8]
	}
	for _, binding := range asset.Bindings {
		pkg.Bindings = append(pkg.Bindings, managedBinding{skillBinding: binding, Digest: digest})
	}
	catalog.Packages = append(catalog.Packages, pkg)
	return &catalog.Packages[len(catalog.Packages)-1], nil
}

func planBindingChange(command string, view *inventoryView, selected []skillAsset, opt options) (*lifecyclePlan, error) {
	plan := &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}
	seen := map[string]bool{}
	removedInstallations := []skill{}
	for _, asset := range selected {
		capCommand := command
		if command == "unpin" {
			capCommand = "pin"
		}
		if cap := asset.Capabilities[capCommand]; !cap.Supported {
			return nil, fmt.Errorf("%s: %s", asset.Name, cap.Reason)
		}
		pkg := view.catalog.find(asset.ID)
		if pkg == nil {
			var err error
			pkg, err = registerExternal(view.catalog, asset)
			if err != nil {
				return nil, err
			}
		}
		if seen[pkg.ID] {
			continue
		}
		seen[pkg.ID] = true
		if err := verifyPackageUnmodified(*pkg); err != nil {
			return nil, err
		}
		if command == "pin" || command == "unpin" {
			if command == "pin" && opt.Ref != "" && opt.Ref != pkg.Revision {
				return nil, fmt.Errorf("pin fixes the installed revision; install the requested --ref as a separate copy first")
			}
			pin := ""
			if command == "pin" {
				pin = pkg.Revision
			}
			if pkg.Pin != pin {
				pkg.Pin = pin
				change, err := view.catalog.mutation()
				if err != nil {
					return nil, err
				}
				plan.mutations = []pathMutation{change}
				plan.Changes = []plannedChange{{Path: change.Path, Action: "file"}}
			}
			continue
		}
		if command == "enable" {
			matched := false
			for _, binding := range pkg.Bindings {
				if bindingMatches(binding.skillBinding, opt) {
					matched = true
					break
				}
			}
			if !matched && len(opt.Hosts) > 0 {
				bindings, err := targetBindings(opt, view.roots, pkg.Name)
				if err != nil {
					return nil, err
				}
				for _, binding := range bindings {
					binding.Enabled = false
					pkg.Bindings = append(pkg.Bindings, managedBinding{skillBinding: binding, Digest: pkg.Digest})
				}
			}
		}
		for i := range pkg.Bindings {
			binding := &pkg.Bindings[i]
			if !bindingMatches(binding.skillBinding, opt) {
				continue
			}
			if pkg.Provider == "git-worktree" && binding.Mode != "link" && command != "enable" {
				return nil, fmt.Errorf("%s belongs to a Git repository; this operation can change Agent links only", binding.Path)
			}
			if command == "enable" && binding.Enabled {
				continue
			}
			if command != "enable" && !binding.Enabled && command != "remove" {
				continue
			}
			if command != "enable" {
				for _, other := range pkg.Bindings {
					if other.Enabled && !bindingMatches(other.skillBinding, opt) && (fsutil.SamePath(other.Path, binding.Path) || fsutil.SamePath(binding.Path, pkg.Directory)) {
						return nil, fmt.Errorf("%s is shared with %s; cannot disable only the selected Agent", binding.Path, other.Host)
					}
				}
			}
			if command == "enable" {
				change, err := mutation(binding.Path, "link")
				if err != nil {
					return nil, err
				}
				if change.Expected != "missing" {
					return nil, fmt.Errorf("enable would overwrite %s", binding.Path)
				}
				if binding.Mode == "link" {
					change.Target = pkg.Directory
				} else {
					change.Kind, change.Source = "directory", currentPackageDirectory(*pkg)

				}
				if err := plan.add(change); err != nil {
					return nil, err
				}
				binding.Enabled = true
			} else {
				if binding.Enabled {
					if binding.Mode != "link" {
						stash, err := stateFile(filepath.Join("disabled", stableID(pkg.ID, binding.Path)))
						if err != nil {
							return nil, err
						}
						copyChange, err := mutation(stash, "directory")
						if err != nil {
							return nil, err
						}
						copyChange.Source = binding.Path
						if err := plan.add(copyChange); err != nil {
							return nil, err
						}
						binding.DisabledPath = stash
					}
					change, err := mutation(binding.Path, "remove")
					if err != nil {
						return nil, err
					}
					if err := plan.add(change); err != nil {
						return nil, err
					}
				}
				binding.Enabled = false
			}
			plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
		}
		if command == "remove" {
			for _, binding := range pkg.Bindings {
				if bindingMatches(binding.skillBinding, opt) && fsutil.SamePath(binding.Path, pkg.Directory) {
					removedInstallations = append(removedInstallations, skill{Name: pkg.Name, Path: pkg.Directory, Broken: true})
					break
				}
			}
			pkg.Bindings = slices.DeleteFunc(pkg.Bindings, func(binding managedBinding) bool { return bindingMatches(binding.skillBinding, opt) })
		}
	}
	if command == "remove" {
		if err := planRemovedProviderRecords(plan, view, removedInstallations); err != nil {
			return nil, err
		}
	}
	if command == "pin" || command == "unpin" {
		return plan, nil
	}
	if err := plan.finish(view.catalog); err != nil {
		return nil, err
	}
	return plan, nil
}

func planPackageUpdate(plan *lifecyclePlan, pkg *managedPackage, prepared preparedPackage) error {
	if pkg.Pin != "" {
		return nil
	}
	if err := verifyPackageUnmodified(*pkg); err != nil {
		return err
	}
	if prepared.Digest == pkg.Digest {
		return nil
	}
	directory, err := packageStorePath(pkg.ID, prepared.Digest)
	if err != nil {
		return err
	}
	change, err := mutation(directory, "directory")
	if err != nil {
		return err
	}
	if change.Expected == "missing" {
		change.Source, change.ContentDigest = prepared.Directory, prepared.Digest
		if err := plan.add(change); err != nil {
			return err
		}
	} else {
		if hash, err := fsutil.HashDirectory(directory); err != nil || hash != prepared.Digest {
			return fmt.Errorf("cached revision was modified: %s", directory)
		}
	}
	for i := range pkg.Bindings {
		binding := &pkg.Bindings[i]
		if binding.Enabled {
			change, err := mutation(binding.Path, "link")
			if err != nil {
				return err
			}
			if binding.Mode == "link" {
				change.Target = directory
			} else {
				change.Kind, change.Source, change.ContentDigest = "directory", prepared.Directory, prepared.Digest
			}
			if err := plan.add(change); err != nil {
				return err
			}
			plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
		}
		binding.Digest = prepared.Digest
		if !pkg.External {
			binding.DisabledPath = ""
		}
	}
	pkg.Directory, pkg.Digest, pkg.Revision = directory, prepared.Digest, prepared.Revision
	if plan.updated == nil {
		plan.updated = map[string]bool{}
	}
	plan.updated[pkg.ID] = true
	return nil
}

func prepareCatalogUpdates(ctx context.Context, view *inventoryView, selected []skillAsset, opt options) (map[string]preparedPackage, func(), map[string]error) {
	result := map[string]preparedPackage{}
	failures := map[string]error{}
	var cleanupFunctions []func()
	cleanup := func() {
		for _, fn := range cleanupFunctions {
			fn()
		}
	}
	groups := map[string][]managedPackage{}
	for _, asset := range selected {
		pkg := view.catalog.find(asset.ID)
		if pkg == nil || pkg.External || pkg.Pin != "" {
			continue
		}
		key := pkg.Source.Kind + "\x00" + pkg.Source.URL + "\x00" + pkg.Source.Ref
		groups[key] = append(groups[key], *pkg)
	}
	for _, group := range groups {
		source := group[0].Source
		source.SkillPath = ""
		var names []string
		for _, pkg := range group {
			names = appendUniqueString(names, pkg.Name)
		}
		operationCtx, cancel := context.WithTimeout(ctx, view.timeout)
		packages, clean, err := preparePackages(operationCtx, source, names, opt.Offline)
		cancel()
		if err != nil {
			for _, pkg := range group {
				failures[pkg.ID] = err
			}
			continue
		}
		cleanupFunctions = append(cleanupFunctions, clean)
		for _, pkg := range group {
			found := false
			for _, prepared := range packages {
				if prepared.Name == pkg.Name && filepath.ToSlash(prepared.Source.SkillPath) == filepath.ToSlash(pkg.Source.SkillPath) {
					prepared.Source = pkg.Source
					result[pkg.ID] = prepared
					found = true
					break
				}
			}
			if !found {
				failures[pkg.ID] = fmt.Errorf("upstream removed skill %s at %s", pkg.Name, pkg.Source.SkillPath)
			}
		}
	}
	return result, cleanup, failures
}

func catalogBytes(catalog *packageCatalog) []byte { data, _ := json.Marshal(catalog); return data }

func sortedAffected(plan *lifecyclePlan) {
	slices.Sort(plan.Affected)
	plan.Affected = slices.Compact(plan.Affected)
}
