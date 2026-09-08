package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
)

// External adapters retain ownership. Their exact before and verified after
// images are persisted around the native command, so ordinary rollback can
// restore both provider metadata and content as one operation.
func beginExternalOperation(command string, paths, affected []string) (*operationRecord, error) {
	catalogPath, err := stateFile("inventory.json")
	if err != nil {
		return nil, err
	}
	catalog, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	original := append([]string(nil), paths...)
	for _, pkg := range catalog.Packages {
		if !pkg.External || !pathsCover(original, pkg.Directory) {
			continue
		}
		if err := verifyPackageUnmodified(pkg); err != nil {
			return nil, err
		}
		for _, binding := range pkg.Bindings {
			if binding.Enabled {
				if !pathsCover(paths, binding.Path) {
					paths = append(paths, binding.Path)
				}
				affected = appendUniqueString(affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
			}
		}
	}
	if _, err := os.Stat(catalogPath); err == nil && !pathsCover(paths, catalogPath) {
		paths = append(paths, catalogPath)
	}
	changes := []pathMutation{}
	for _, path := range paths {
		change, err := mutation(path, "remove")
		if err != nil {
			return nil, err
		}
		if change.Expected != "missing" {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				change.Kind = "link"
				change.Target, err = os.Readlink(path)
			case info.IsDir():
				change.Kind, change.Source = "directory", path
			default:
				change.Kind, change.Mode = "file", info.Mode().Perm()
				change.Data, err = os.ReadFile(path)
			}
			if err != nil {
				return nil, err
			}
		}
		changes = append(changes, change)
	}
	operation, err := prepareOperation(command, changes, affected)
	if err != nil {
		return nil, err
	}
	operation.External = true
	operation.State = "external_applying"
	// External commands do not provide a predicted after image. Never guess
	// whether an interrupted command or a concurrent editor produced new bytes.
	for i := range operation.Steps {
		operation.Steps[i].State = "external_pending"
	}
	if err := operation.save(); err != nil {
		return nil, err
	}
	return operation, nil
}

func (r *operationRecord) finishExternal(cause error) error {
	if cause == nil {
		if err := refreshExternalCatalog(r); err != nil {
			cause = err
		}
	}
	unchanged := true
	for i := range r.Steps {
		step := &r.Steps[i]
		after, err := fingerprint(step.Path)
		if err != nil {
			r.State = "recovery_required"
			r.Error = err.Error()
			return errors.Join(cause, err, r.save())
		}
		unchanged = unchanged && after == step.Before
		image := r.imagePath("after", i)
		if err := os.RemoveAll(image); err != nil {
			return errors.Join(cause, err)
		}
		if after != "missing" {
			if err := copySnapshot(step.Path, image); err != nil {
				return errors.Join(cause, err)
			}
		}
		if hash, err := fingerprint(image); err != nil || hash != after {
			return fmt.Errorf("external after-image verification failed for %s", step.Path)
		}
		step.After, step.State = after, "applied"
	}
	if cause == nil {
		r.State = "committed"
		if unchanged {
			r.State = "no_change"
		}
		return r.save()
	}
	r.Error = cause.Error()
	if unchanged {
		r.State = "rolled_back"
		return errors.Join(cause, r.save())
	}
	// Existing adapter verification has failed. Keep both observed states; do
	// not delete evidence if its own rollback could not finish.
	r.State = "recovery_required"
	return errors.Join(cause, r.save(), fmt.Errorf("operation %s retains recovery evidence at %s", r.ID, r.directory))
}

func recoverExternal(r *operationRecord) error {
	unchanged := true
	for _, step := range r.Steps {
		hash, err := fingerprint(step.Path)
		if err != nil {
			return err
		}
		unchanged = unchanged && hash == step.Before
	}
	if unchanged {
		r.State = "rolled_back"
		return r.save()
	}
	r.State = "recovery_required"
	_ = r.save()
	return fmt.Errorf("external operation %s was interrupted; inspect before/after evidence in %s and use rollback %s to restore a recorded after state", r.ID, filepath.Clean(r.directory), r.ID)
}

func trackedUpdateTransaction(item skill, entry *trackedEntry, state *trackedState, source, digest string, sessions ...*sourceSession) error {
	content, err := mutation(item.Path, "directory")
	if err != nil {
		return err
	}
	content.Source, content.ContentDigest = source, digest
	metadata, err := mutation(state.path, "file")
	if err != nil {
		return fmt.Errorf("save source state: %w", err)
	}
	if info, err := os.Stat(state.path); err == nil && info.IsDir() {
		return fmt.Errorf("save source state: target is a directory")
	}
	previous := entry.InstalledHash
	entry.InstalledHash = digest
	data, err := trackedStateBytes(state)
	if err != nil {
		entry.InstalledHash = previous
		return err
	}
	metadata.Data = data
	changes := []pathMutation{content, metadata}
	catalog, loadErr := loadCatalog()
	if loadErr != nil {
		entry.InstalledHash = previous
		return loadErr
	}
	if pkg := catalog.byPath(item.Path); pkg != nil && pkg.External {
		if err := verifyPackageUnmodified(*pkg); err != nil {
			entry.InstalledHash = previous
			return err
		}
		seen := []string{item.Path}
		for _, binding := range pkg.Bindings {
			if !binding.Enabled || binding.Mode == "link" || pathsCover(seen, binding.Path) {
				continue
			}
			change, err := mutation(binding.Path, "directory")
			if err != nil {
				entry.InstalledHash = previous
				return err
			}
			change.Source, change.ContentDigest = source, digest
			changes = append(changes, change)
			seen = append(seen, binding.Path)
		}
		pkg.Digest, pkg.Revision = digest, "sha256:"+digest
		for i := range pkg.Bindings {
			pkg.Bindings[i].Digest = digest
		}
		change, err := catalog.mutation()
		if err != nil {
			entry.InstalledHash = previous
			return err
		}
		changes = append(changes, change)
	}
	affected := []string{item.Name + ": " + item.Path}
	if pkg := catalog.byPath(item.Path); pkg != nil {
		for _, binding := range pkg.Bindings {
			if binding.Enabled {
				affected = appendUniqueString(affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
			}
		}
	}
	operation, err := prepareOperation("update tracked copy", changes, affected)
	if operation != nil && len(sessions) > 0 {
		sessions[0].operations = append(sessions[0].operations, operation)
	}
	if err == nil {
		err = operation.apply()
	}
	if err != nil {
		entry.InstalledHash = previous
		return err
	}
	return nil
}

func beginGitOperation(root string, items []skill, targets ...string) (*operationRecord, error) {
	paths := []string{root}
	gitDir, err := gitstore.Output(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	if !fsutil.Within(fsutil.BindingPath(root), fsutil.BindingPath(gitDir)) {
		paths = append(paths, gitDir)
		common, err := gitstore.Output(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, err
		}
		branch, err := gitstore.Output(root, "symbolic-ref", "HEAD")
		if err != nil {
			return nil, err
		}
		paths = append(paths, filepath.Join(common, filepath.FromSlash(branch)), filepath.Join(common, "logs", filepath.FromSlash(branch)))
	}
	affected := []string{"repository: " + root}
	for _, item := range items {
		for _, binding := range item.Bindings {
			affected = appendUniqueString(affected, binding.Host+"/"+binding.Scope+": "+binding.Path)
		}
	}
	target := "@{upstream}"
	if len(targets) > 0 {
		target = targets[0]
	}
	files, err := gitstore.Output(root, "diff", "--name-only", "--no-ext-diff", "HEAD", target, "--")
	if err != nil {
		return nil, err
	}
	for file := range strings.SplitSeq(files, "\n") {
		if file != "" {
			affected = appendUniqueString(affected, filepath.Join(root, file))
		}
	}
	return beginExternalOperation("update Git repository", paths, affected)
}

func refreshExternalCatalog(operation *operationRecord) error {
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	changed := false
	for i := range catalog.Packages {
		pkg := &catalog.Packages[i]
		if !pkg.External {
			continue
		}
		touched := false
		for _, step := range operation.Steps {
			if fsutil.SamePath(step.Path, pkg.Directory) || fsutil.Within(step.Path, pkg.Directory) {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		digest, err := fsutil.HashDirectory(pkg.Directory)
		if err != nil {
			return err
		}
		if digest != pkg.Digest {
			seen := []string{pkg.Directory}
			for _, binding := range pkg.Bindings {
				if !binding.Enabled || binding.Mode == "link" || pathsCover(seen, binding.Path) {
					continue
				}
				before, err := fsutil.HashDirectory(binding.Path)
				if err != nil || before != binding.Digest {
					return fmt.Errorf("copy changed during update: %s", binding.Path)
				}
				replacement, err := beginDirectoryReplacement(binding.Path, pkg.Directory)
				if err != nil {
					return err
				}
				if after, err := fsutil.HashDirectory(binding.Path); err != nil || after != digest {
					return errors.Join(fmt.Errorf("copy verification failed: %s", binding.Path), replacement.rollback())
				}
				if err := replacement.commit(); err != nil {
					return err
				}
				seen = append(seen, binding.Path)
			}
			pkg.Digest, pkg.Revision = digest, "sha256:"+digest
			for i := range pkg.Bindings {
				pkg.Bindings[i].Digest = digest
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	mutation, err := catalog.mutation()
	if err != nil {
		return err
	}
	return writeFileAtomically(mutation.Path, mutation.Data, 0600)
}

func pathsCover(paths []string, target string) bool {
	for _, path := range paths {
		if fsutil.Within(fsutil.BindingPath(path), fsutil.BindingPath(target)) {
			return true
		}
	}
	return false
}
