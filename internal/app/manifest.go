package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/lingengyuan/skillctl/internal/archive"
	"github.com/lingengyuan/skillctl/internal/cli"
	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type environmentManifest struct {
	Version  int                           `toml:"version" json:"version"`
	Skills   []desiredSkill                `toml:"skills" json:"skills"`
	Profiles map[string]environmentProfile `toml:"profiles,omitempty" json:"profiles,omitempty"`
}

type desiredSkill struct {
	ID        string   `toml:"id" json:"id"`
	Name      string   `toml:"name" json:"name"`
	Source    string   `toml:"source" json:"source"`
	Kind      string   `toml:"kind,omitempty" json:"kind,omitempty"`
	SkillPath string   `toml:"skill_path,omitempty" json:"skillPath,omitempty"`
	Ref       string   `toml:"ref,omitempty" json:"ref,omitempty"`
	Hosts     []string `toml:"hosts" json:"hosts"`
	Scope     string   `toml:"scope" json:"scope"`
	Mode      string   `toml:"mode,omitempty" json:"mode,omitempty"`
}

type environmentProfile struct {
	Skills []string `toml:"skills" json:"skills"`
}

type environmentLock struct {
	SchemaVersion int           `json:"schemaVersion"`
	Skills        []lockedSkill `json:"skills"`
}

type lockedSkill struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Definition string     `json:"definition"`
	Source     sourceSpec `json:"source"`
	Revision   string     `json:"revision"`
	Digest     string     `json:"digest"`
}

func manifestFile(opt options) (string, error) {
	path := opt.File
	if path == "" {
		path = filepath.Join(projectDirectory(opt.Project), "skillctl.toml")
	}
	return filepath.Abs(path)
}

func loadEnvironment(path string) (environmentManifest, environmentLock, error) {
	var manifest environmentManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, environmentLock{}, err
	}
	meta, err := toml.Decode(string(data), &manifest)
	if err != nil {
		return manifest, environmentLock{}, fmt.Errorf("invalid environment manifest: %w", err)
	}
	if len(meta.Undecoded()) > 0 {
		return manifest, environmentLock{}, fmt.Errorf("unknown manifest field: %s", meta.Undecoded()[0])
	}
	if err := validateEnvironment(&manifest); err != nil {
		return manifest, environmentLock{}, err
	}
	lock := environmentLock{SchemaVersion: 1, Skills: []lockedSkill{}}
	data, err = os.ReadFile(filepath.Join(filepath.Dir(path), "skillctl.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return manifest, lock, nil
	}
	if err != nil {
		return manifest, lock, err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return manifest, lock, fmt.Errorf("invalid lock file: %w", err)
	}
	if lock.SchemaVersion != 1 {
		return manifest, lock, fmt.Errorf("unsupported lock schema: %d", lock.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, entry := range lock.Skills {
		if entry.ID == "" || seen[entry.ID] || len(entry.Digest) != 64 || entry.Revision == "" {
			return manifest, lock, fmt.Errorf("invalid locked skill: %s", entry.ID)
		}
		if err := validatePortableSource(entry.Source.URL); err != nil {
			return manifest, lock, err
		}
		if err := validateSourceURL(entry.Source.Artifact); err != nil {
			return manifest, lock, err
		}
		seen[entry.ID] = true
	}
	return manifest, lock, nil
}

func validateEnvironment(manifest *environmentManifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported manifest version: %d", manifest.Version)
	}
	seen := map[string]bool{}
	for i := range manifest.Skills {
		item := &manifest.Skills[i]
		if item.ID == "" {
			item.ID = item.Name
		}
		if !safeComponent(item.ID) || seen[item.ID] || !skilldoc.ValidName(item.Name) || item.Source == "" {
			return fmt.Errorf("invalid or duplicate manifest skill: %s", item.ID)
		}
		seen[item.ID] = true
		if err := validatePortableSource(item.Source); err != nil {
			return err
		}
		if item.Scope == "" {
			item.Scope = "project"
		}
		if item.Scope != "project" && item.Scope != "user" {
			return fmt.Errorf("manifest scope must be project or user")
		}
		if len(item.Hosts) == 0 {
			return fmt.Errorf("%s: manifest requires explicit hosts", item.ID)
		}
		for j, host := range item.Hosts {
			item.Hosts[j] = cli.NormalizeHost(host)
		}
		if item.Mode == "" {
			item.Mode = "link"
		}
		if item.Mode != "link" && item.Mode != "copy" {
			return fmt.Errorf("unsupported binding mode: %s", item.Mode)
		}
		if filepath.IsAbs(item.SkillPath) || strings.Contains(item.SkillPath, "\\") || !fsutil.Within("/source", filepath.Join("/source", item.SkillPath)) {
			return fmt.Errorf("skill_path must remain inside its source")
		}
	}
	for name, profile := range manifest.Profiles {
		if !safeComponent(name) {
			return fmt.Errorf("invalid profile name")
		}
		for _, id := range profile.Skills {
			if !seen[id] {
				return fmt.Errorf("profile %s references unknown skill %s", name, id)
			}
		}
	}
	return nil
}

func validatePortableSource(source string) error {
	if err := validateSourceURL(source); err != nil {
		return err
	}
	if filepath.IsAbs(source) || strings.HasPrefix(source, "~") || strings.HasPrefix(source, "file:") || strings.Contains(source, "${") || strings.Contains(source, "$HOME") || strings.Contains(source, "\\") {
		return fmt.Errorf("shared configuration requires a relative local source or a credential-free remote URL")
	}
	if len(source) > 2 && source[1] == ':' {
		return fmt.Errorf("absolute Windows source paths are not portable")
	}
	return nil
}

func desiredDefinition(item desiredSkill) string {
	data, _ := json.Marshal(item)
	return stableID(string(data))
}

func environmentSource(item desiredSkill, base string) (sourceSpec, error) {
	value := item.Source
	if item.Kind == "local" || item.Kind == "archive" && !strings.Contains(value, "://") || strings.HasPrefix(value, ".") {
		value = filepath.Join(base, filepath.FromSlash(value))
	}
	source, err := parseSource(value, item.Ref, item.SkillPath)
	if err != nil {
		return source, err
	}
	if item.Kind != "" {
		source.Kind = item.Kind
	}
	return source, nil
}

func preparedLocked(ctx context.Context, entry lockedSkill, item desiredSkill, base string, offline bool) (preparedPackage, func(), error) {
	cleanup := func() {}
	source, err := environmentSource(item, base)
	if err != nil {
		return preparedPackage{}, cleanup, err
	}
	source.SkillPath = entry.Source.SkillPath
	source.Artifact = entry.Source.Artifact
	source.Kind = entry.Source.Kind
	// The immutable store is authoritative only after digest verification.
	directory, err := packageStorePath(sourceIdentity(source), entry.Digest)
	if err != nil {
		return preparedPackage{}, cleanup, err
	}
	if hash, err := fsutil.HashDirectory(directory); err == nil {
		if hash != entry.Digest {
			return preparedPackage{}, cleanup, fmt.Errorf("locked cached artifact digest mismatch: %s", entry.ID)
		}
		return preparedPackage{Name: entry.Name, Source: source, Revision: entry.Revision, Digest: entry.Digest, Directory: directory}, cleanup, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return preparedPackage{}, cleanup, err
	}
	resolved := source
	if resolved.Kind == "git" {
		resolved.Ref = entry.Revision
	}
	var packages []preparedPackage
	if resolved.Kind == "well-known" {
		if offline {
			return preparedPackage{}, cleanup, fmt.Errorf("offline: locked artifact is unavailable for %s", entry.ID)
		}
		body, err := readArtifact(ctx, resolved.Artifact, false)
		if err != nil {
			return preparedPackage{}, cleanup, err
		}
		if artifactRevision(body) != entry.Revision {
			return preparedPackage{}, cleanup, fmt.Errorf("locked artifact revision mismatch: %s", entry.ID)
		}
		dir, err := os.MkdirTemp("", "skillctl-locked-")
		if err != nil {
			return preparedPackage{}, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		if err := archive.Unpack(body, dir); err != nil {
			cleanup()
			return preparedPackage{}, func() {}, err
		}
		digest, err := fsutil.HashDirectory(dir)
		if err != nil {
			cleanup()
			return preparedPackage{}, func() {}, err
		}
		packages = []preparedPackage{{Name: entry.Name, Directory: dir, Revision: entry.Revision, Digest: digest}}
	} else {
		packages, cleanup, err = preparePackages(ctx, resolved, []string{entry.Name}, offline)
		if err != nil {
			return preparedPackage{}, cleanup, err
		}
	}
	if len(packages) != 1 || packages[0].Digest != entry.Digest || packages[0].Revision != entry.Revision {
		cleanup()
		return preparedPackage{}, func() {}, fmt.Errorf("locked revision or content digest mismatch: %s", entry.ID)
	}
	prepared := packages[0]
	prepared.Source = source
	return prepared, cleanup, nil
}

func activeProfilePath(path string) (string, error) {
	return stateFile(filepath.Join("profiles", stableID(fsutil.PathKey(path))+".json"))
}

func selectedProfile(manifest environmentManifest, path, name string) (map[string]bool, string, error) {
	if name == "" {
		statePath, err := activeProfilePath(path)
		if err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(statePath)
		if err == nil {
			var state struct {
				Profile string `json:"profile"`
			}
			if err := json.Unmarshal(data, &state); err != nil {
				return nil, "", err
			}
			name = state.Profile
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	selected := map[string]bool{}
	if name == "" || name == "all" {
		for _, item := range manifest.Skills {
			selected[item.ID] = true
		}
		return selected, "all", nil
	}
	profile, ok := manifest.Profiles[name]
	if !ok {
		return nil, "", fmt.Errorf("profile not found: %s", name)
	}
	for _, id := range profile.Skills {
		selected[id] = true
	}
	return selected, name, nil
}

func runManifestSync(ctx context.Context, command string, opt options, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		return outputResult(commandResult{Command: command, Diagnostics: []diagnostic{{Code: "environment_failed", Level: "error", Message: err.Error()}}}, opt, stdout, stderr, true)
	}
	path, err := manifestFile(opt)
	if err != nil {
		return fail(err)
	}
	manifest, lock, err := loadEnvironment(path)
	if err != nil {
		return fail(err)
	}
	selected, profile, err := selectedProfile(manifest, path, opt.Profile)
	if err != nil {
		return fail(err)
	}
	base := filepath.Dir(path)
	scanOpt := opt
	scanOpt.Project = base
	scanOpt.Hosts = nil
	scanOpt.Scopes = nil
	view, err := loadInventory(ctx, scanOpt, false, false)
	if err != nil {
		return fail(err)
	}
	plan := &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}
	environment := stableID(fsutil.PathKey(path))
	expectedBindings := map[string]bool{}
	newLock := environmentLock{SchemaVersion: 1, Skills: []lockedSkill{}}
	cleaners := []func(){}
	defer func() {
		for _, clean := range cleaners {
			clean()
		}
	}()
	for _, item := range manifest.Skills {
		var old *lockedSkill
		for _, entry := range lock.Skills {
			if entry.ID == item.ID {
				old = new(entry)
				break
			}
		}
		advance := command == "update" && (len(opt.Names) == 0 || slices.Contains(opt.Names, item.ID) || slices.Contains(opt.Names, item.Name))
		if !selected[item.ID] && !advance && old != nil && old.Definition == desiredDefinition(item) {
			newLock.Skills = append(newLock.Skills, *old)
			continue
		}
		definition := desiredDefinition(item)
		opctx, cancel := context.WithTimeout(ctx, view.timeout)
		var prepared preparedPackage
		var clean func()
		var entry lockedSkill
		if old != nil && old.Definition == definition && !advance {
			prepared, clean, err = preparedLocked(opctx, *old, item, base, opt.Offline)
			entry = *old
		} else if opt.Frozen {
			err = fmt.Errorf("frozen sync: missing or changed lock entry for %s", item.ID)
		} else {
			var source sourceSpec
			source, err = environmentSource(item, base)
			if err == nil {
				var packages []preparedPackage
				packages, clean, err = preparePackages(opctx, source, []string{item.Name}, opt.Offline)
				if err == nil && len(packages) != 1 {
					err = fmt.Errorf("%s: select an unambiguous skill_path", item.ID)
				}
				if err == nil {
					prepared = packages[0]
					portable := prepared.Source
					portable.URL = item.Source
					entry = lockedSkill{ID: item.ID, Name: item.Name, Definition: definition, Source: portable, Revision: prepared.Revision, Digest: prepared.Digest}
				}
			}
		}
		cancel()
		if clean != nil {
			cleaners = append(cleaners, clean)
		}
		if err != nil {
			return fail(err)
		}
		newLock.Skills = append(newLock.Skills, entry)
		if !selected[item.ID] {
			continue
		}
		id := sourceIdentity(prepared.Source)
		if existing := view.catalog.find(id); existing != nil && existing.Digest != prepared.Digest {
			if existing.Pin != "" {
				return fail(fmt.Errorf("%s is pinned to a different revision", item.ID))
			}
			if err := planPackageUpdate(plan, existing, prepared); err != nil {
				return fail(err)
			}
		}
		targetOpt := options{Hosts: item.Hosts, Scopes: []string{item.Scope}, Project: base, Copy: item.Mode == "copy"}
		bindings, err := targetBindings(targetOpt, view.roots, item.Name)
		if err != nil {
			return fail(err)
		}
		if err := planInstallPackage(plan, view.catalog, prepared, bindings, environment); err != nil {
			return fail(err)
		}
		pkg := view.catalog.find(id)
		for i := range pkg.Bindings {
			binding := &pkg.Bindings[i]
			for _, desired := range bindings {
				if binding.Host == desired.Host && fsutil.SamePath(binding.Path, desired.Path) {
					binding.Environments = appendUniqueString(binding.Environments, environment)
					expectedBindings[pkg.ID+"\x00"+binding.Host+"\x00"+binding.Path] = true
				}
			}
		}
	}

	// Release declarations in two passes so a shared physical path can be
	// disabled atomically when every implicit consumer leaves this profile.
	released := map[string]bool{}
	for p := range view.catalog.Packages {
		pkg := &view.catalog.Packages[p]
		for b := range pkg.Bindings {
			binding := &pkg.Bindings[b]
			key := pkg.ID + "\x00" + binding.Host + "\x00" + binding.Path
			if slices.Contains(binding.Environments, environment) && !expectedBindings[key] {
				binding.Environments = slices.DeleteFunc(binding.Environments, func(value string) bool { return value == environment })
				released[key] = true
			}
		}
	}
	for p := range view.catalog.Packages {
		pkg := &view.catalog.Packages[p]
		for b := range pkg.Bindings {
			binding := &pkg.Bindings[b]
			if !released[pkg.ID+"\x00"+binding.Host+"\x00"+binding.Path] || len(binding.Environments) > 0 || binding.Manual || !binding.Enabled {
				continue
			}
			for _, other := range pkg.Bindings {
				if other.Enabled && fsutil.SamePath(other.Path, binding.Path) && (other.Manual || len(other.Environments) > 0) && other.Host != binding.Host {
					return fail(fmt.Errorf("profile change cannot partially disable shared path %s", binding.Path))
				}
			}
			if !plan.updated[pkg.ID] {
				if err := verifyPackageUnmodified(*pkg); err != nil {
					return fail(err)
				}
			}
			change, err := mutation(binding.Path, "remove")
			if err != nil {
				return fail(err)
			}
			if err := plan.add(change); err != nil {
				return fail(err)
			}
			binding.Enabled = false
			plan.Affected = appendUniqueString(plan.Affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
		}
	}

	if err := plan.finish(view.catalog); err != nil {
		return fail(err)
	}
	slices.SortFunc(newLock.Skills, func(a, b lockedSkill) int { return strings.Compare(a.ID, b.ID) })
	lockBytes, err := json.Marshal(newLock)
	if err != nil {
		return fail(err)
	}
	if err := addFileIfChanged(plan, filepath.Join(base, "skillctl.lock"), append(lockBytes, '\n')); err != nil {
		return fail(err)
	}
	if opt.Profile != "" {
		statePath, err := activeProfilePath(path)
		if err != nil {
			return fail(err)
		}
		data, _ := json.Marshal(struct {
			Profile string `json:"profile"`
		}{profile})
		if err := addFileIfChanged(plan, statePath, append(data, '\n')); err != nil {
			return fail(err)
		}
	}
	return executePlan(plan, nil, opt, stdout, stderr)
}

func addFileIfChanged(plan *lifecyclePlan, path string, data []byte) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes.Equal(old, data) {
		return nil
	}
	change, err := mutation(path, "file")
	if err != nil {
		return err
	}
	change.Data = data
	return plan.add(change)
}

func runProfile(ctx context.Context, opt options, stdout, stderr io.Writer) int {
	if len(opt.Names) == 2 && opt.Names[0] == "use" {
		opt.Profile, opt.Names = opt.Names[1], nil
		return runManifestSync(ctx, "sync", opt, stdout, stderr)
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	if len(opt.Names) > 1 || len(opt.Names) == 1 && opt.Names[0] != "list" {
		return fail(fmt.Errorf("usage: skillctl profile [list | use NAME] --file skillctl.toml"))
	}
	path, err := manifestFile(opt)
	if err != nil {
		return fail(err)
	}
	manifest, _, err := loadEnvironment(path)
	if err != nil {
		return fail(err)
	}
	_, active, err := selectedProfile(manifest, path, opt.Profile)
	if err != nil {
		return fail(err)
	}
	result := struct {
		Active   string                        `json:"active"`
		Profiles map[string]environmentProfile `json:"profiles"`
	}{active, manifest.Profiles}
	if opt.JSON {
		return outputResult(commandResult{Command: "profile", Result: result}, opt, stdout, stderr, false)
	}
	fmt.Fprintf(stdout, "Active profile: %s\n", active)
	names := []string{"all"}
	for name := range manifest.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return 0
}
