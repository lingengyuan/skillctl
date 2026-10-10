package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"slices"
	"strings"
)

// recoverySource is a recorded origin that can be installed again, together
// with where the record came from.
type recoverySource struct {
	Source      sourceSpec
	Evidence    string
	Description string
}

// recoverySourceSet maps each skill name to the origins recorded for it.
// A name is present with a nil value when no usable source was found, and is
// listed in ambiguous when several conflicting records survive.
type recoverySourceSet struct {
	byName    map[string][]recoverySource
	ambiguous []string
}

// collectRecoverySources resolves installable origins for everything that is
// broken: dangling links found by the scan, and recorded packages whose content
// disappeared. It must run before repairs, because every lookup matches the
// install path shared by the links and the recorded entries.
//
// A skill is reported as recoverable only when a record names a usable source;
// anything else is reported as needing one instead of being guessed.
func collectRecoverySources(links []skill, view *inventoryView, state *trackedState) recoverySourceSet {
	manifests, managed := view.manifests, view.managed
	provenance, _ := newProvenanceIndex(links, state, manifests, managed)
	// Gather every origin recorded for a name before deciding. A dangling link
	// often has no usable record while the package behind it does, so recording
	// the first answer seen would discard the usable one.
	gathered := map[string][]recoverySource{}
	// Names seen at all, so a skill with no usable record is still reported
	// instead of being silently cleaned up when --reinstall was requested.
	names := map[string]bool{}
	add := func(name string, sources []recoverySource) {
		names[name] = true
		for _, source := range sources {
			key := source.Source.Kind + "\x00" + source.Source.URL + "\x00" + source.Source.SkillPath + "\x00" + source.Source.Ref
			duplicate := false
			for _, existing := range gathered[name] {
				existingKey := existing.Source.Kind + "\x00" + existing.Source.URL + "\x00" + existing.Source.SkillPath + "\x00" + existing.Source.Ref
				if existingKey == key {
					duplicate = true
					break
				}
			}
			if !duplicate {
				gathered[name] = append(gathered[name], source)
			}
		}
	}
	for _, item := range links {
		if !item.Broken {
			continue
		}
		add(item.Name, recordedSources(item, provenance, state))
	}
	// A stored package records its own source, so a package whose content was
	// deleted can be installed again without any external lookup.
	if view.catalog != nil {
		for _, pkg := range view.catalog.Packages {
			if !packageContentMissing(pkg) {
				continue
			}
			add(pkg.Name, recoveredPackageSource(pkg))
		}
	}
	set := recoverySourceSet{byName: map[string][]recoverySource{}}
	for name := range names {
		sources, ok := gathered[name]
		if !ok {
			// Seen but with nothing recorded: report it so the user learns a
			// source is required instead of losing the entry silently.
			set.byName[name] = nil
			continue
		}
		set.byName[name] = sources
		if len(sources) > 1 {
			set.ambiguous = append(set.ambiguous, name)
		}
	}
	slices.Sort(set.ambiguous)
	return set
}

// packageContentMissing reports whether a recorded package lost its content.
func packageContentMissing(pkg managedPackage) bool {
	if pkg.Directory == "" {
		return false
	}
	_, err := os.Stat(pkg.Directory)
	return errors.Is(err, os.ErrNotExist)
}

// recoveredPackageSource returns the origin recorded on the package itself.
func recoveredPackageSource(pkg managedPackage) []recoverySource {
	if pkg.Source.URL == "" {
		return nil
	}
	source := pkg.Source
	if source.Kind == "" {
		resolved, err := parseSource(source.URL, source.Ref, source.SkillPath)
		if err != nil {
			return nil
		}
		source = resolved
	}
	return []recoverySource{{
		Source:      source,
		Evidence:    "recorded package source",
		Description: describeSource(source),
	}}
}

// recordedSources gathers every distinct origin recorded for one skill.
func recordedSources(item skill, provenance *provenanceIndex, state *trackedState) []recoverySource {
	var sources []recoverySource
	seen := map[string]bool{}
	add := func(source sourceSpec, evidence string) {
		if source.URL == "" {
			return
		}
		key := source.Kind + "\x00" + source.URL + "\x00" + source.SkillPath + "\x00" + source.Ref
		if seen[key] {
			return
		}
		seen[key] = true
		sources = append(sources, recoverySource{
			Source:      source,
			Evidence:    evidence,
			Description: describeSource(source),
		})
	}

	// A tracked source records the exact origin and skill path.
	if state != nil {
		if entry, ok := state.findSkill(item); ok && entry.Source != "" {
			if resolved, err := parseSource(entry.Source, entry.Ref, entry.SkillPath); err == nil {
				add(resolved, "tracked source")
			}
		}
	}

	// The Vercel lock records the repository and the skill path inside it.
	if claim, evidence, ok := provenance.locks.claim(item); ok && claim.Entry.SourceURL != "" {
		if resolved, err := parseSource(claim.Entry.SourceURL, claim.Entry.Ref, skillDirectory(claim.Entry.SkillPath)); err == nil {
			add(resolved, strings.Join(evidence, ", "))
		}
	}
	return sources
}

// skillDirectory converts a recorded SKILL.md location into the directory the
// installer expects. The lock file stores the file path; installers take a
// directory.
func skillDirectory(skillPath string) string {
	cleaned := filepath.ToSlash(strings.TrimSpace(skillPath))
	if cleaned == "" {
		return ""
	}
	if cleaned == "SKILL.md" {
		return "."
	}
	if strings.HasSuffix(cleaned, "/SKILL.md") {
		return strings.TrimSuffix(cleaned, "/SKILL.md")
	}
	return cleaned
}

// describeSource renders a source the way a user can retype it.
func describeSource(source sourceSpec) string {
	value := source.URL
	if source.SkillPath != "" && source.SkillPath != "." {
		value += " --skill-path " + source.SkillPath
	}
	if source.Ref != "" {
		value += " --ref " + source.Ref
	}
	return value
}

// reinstallOutcome reports the result per skill so the caller can print an
// accurate summary and fail only when something still needs attention.
type reinstallOutcome struct {
	Recovered    []string
	NoSource     []string
	Failed       []string
	Ambiguous    []string
	Descriptions map[string]string
}

// reinstallRecoveredSkills installs every skill whose recorded source is known.
// Each source is applied as its own operation, so a partial failure leaves the
// successful skills installed and reports only the rest.
func reinstallRecoveredSkills(ctx context.Context, opt options, view *inventoryView, set recoverySourceSet) reinstallOutcome {
	outcome := reinstallOutcome{Descriptions: map[string]string{}}
	names := make([]string, 0, len(set.byName))
	for name := range set.byName {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if slices.Contains(set.ambiguous, name) {
			outcome.Ambiguous = append(outcome.Ambiguous, name)
			continue
		}
		sources := set.byName[name]
		if len(sources) == 0 {
			outcome.NoSource = append(outcome.NoSource, name)
			continue
		}
		record := sources[0]
		if err := reinstallOne(ctx, opt, view, name, record.Source); err != nil {
			outcome.Failed = append(outcome.Failed, name+": "+err.Error())
			continue
		}
		outcome.Recovered = append(outcome.Recovered, name)
		outcome.Descriptions[name] = record.Description
	}
	return outcome
}

// reinstallOne installs a single skill from its recorded source.
func reinstallOne(ctx context.Context, opt options, view *inventoryView, name string, source sourceSpec) error {
	// Only missing content is restored. When an Agent path still holds the
	// skill, installing would replace content the user may have edited, so the
	// repair stops and reports instead.
	if bindingContentPresent(view, name, opt) {
		return fmt.Errorf("%s is already installed", name)
	}
	packages, cleanup, err := preparePackages(ctx, source, []string{name}, opt.Offline)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(packages) == 0 {
		return fmt.Errorf("source does not contain %s", name)
	}
	// A dangling link left at the target would block the fresh install, and it
	// points at content that no longer exists. It must be cleared before the
	// install plan is built, because the plan decides whether a path is
	// occupied at planning time.
	removals := danglingBindingLinks(view, name, opt)
	if len(removals) > 0 {
		affected := make([]string, 0, len(removals))
		for _, change := range removals {
			affected = append(affected, change.Path)
		}
		clearing, err := prepareOperation("doctor clear dangling links", removals, affected)
		if err != nil {
			return err
		}
		if err := clearing.apply(); err != nil {
			return err
		}
	}
	// Resolve the original targets before the stale record is dropped, since
	// the record is what remembers where the skill was installed.
	bindings, err := reinstallBindings(view, name, opt)
	if err != nil {
		return err
	}
	// Content is gone but its record survives, so drop that record first:
	// installing over it would otherwise report a conflicting installation or
	// leave stale bindings pointing at the deleted directory.
	view.catalog.Packages = slices.DeleteFunc(view.catalog.Packages, func(pkg managedPackage) bool {
		return pkg.Name == name
	})
	plan := &lifecyclePlan{Command: "install", Affected: []string{}, Changes: []plannedChange{}}
	for _, pkg := range packages {
		if err := planInstallPackage(plan, view.catalog, pkg, bindings, ""); err != nil {
			return err
		}
	}
	if err := plan.finish(view.catalog); err != nil {
		return err
	}
	if len(plan.mutations) == 0 {
		return nil
	}
	operation, err := prepareOperation(plan.Command, plan.mutations, plan.Affected)
	if err != nil {
		return err
	}
	return operation.apply()
}

// reinstallBindings returns the Agent targets to restore a skill to. The
// recorded package knows where it was installed, which is more accurate than
// detection, so those hosts are reused. Detection is the fallback when no
// package record survives.
func reinstallBindings(view *inventoryView, name string, opt options) ([]skillBinding, error) {
	var hosts []string
	if view.catalog != nil {
		for _, pkg := range view.catalog.Packages {
			if pkg.Name != name {
				continue
			}
			for _, binding := range pkg.Bindings {
				if binding.Host != "" && !slices.Contains(hosts, binding.Host) {
					hosts = append(hosts, binding.Host)
				}
			}
		}
	}
	if len(hosts) == 0 {
		return targetBindings(opt, view.roots, name)
	}
	scoped := opt
	scoped.Hosts = hosts
	return targetBindings(scoped, view.roots, name)
}

// danglingBindingLinks returns removal mutations for Agent targets that point
// at missing content. Only dangling links are touched: anything still resolving
// is left alone, so existing content is never replaced. Several Agents can
// share one physical path, so the result is deduplicated by path.
func danglingBindingLinks(view *inventoryView, name string, opt options) []pathMutation {
	bindings, err := reinstallBindings(view, name, opt)
	if err != nil {
		return nil
	}
	var changes []pathMutation
	seen := map[string]bool{}
	for _, binding := range bindings {
		if !isDanglingLink(binding.Path) {
			continue
		}
		key := fsutil.PathKey(binding.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		change, err := mutation(binding.Path, "remove")
		if err != nil {
			continue
		}
		changes = append(changes, change)
	}
	return changes
}

// bindingContentPresent reports whether any binding for the name resolves to
// existing content.
func bindingContentPresent(view *inventoryView, name string, opt options) bool {
	bindings, err := reinstallBindings(view, name, opt)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if _, err := os.Stat(binding.Path); err == nil {
			return true
		}
	}
	return false
}
