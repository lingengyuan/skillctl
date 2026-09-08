package app

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/lingengyuan/skillctl/internal/gitstore"
)

func prepareGitPackages(ctx context.Context, source sourceSpec, names []string, offline bool) ([]preparedPackage, func(), error) {
	session, cleanup := commandSourceSession(ctx, defaultNetworkTimeout, io.Discard)
	fail := func(err error) ([]preparedPackage, func(), error) { cleanup(); return nil, func() {}, err }
	var cache string
	var err error
	if offline {
		cache, err = gitstore.CachePath("object", source.URL, source.Ref)
		if err == nil {
			_, err = session.revision(cache)
			if err != nil {
				// Legacy worktree caches remain readable offline, by object ID.
				cache, err = gitstore.CachePath("worktree", source.URL, source.Ref)
				if err == nil {
					_, err = session.revision(cache)
				}
			}
		}
	} else {
		cache, err = session.source(source.URL, source.Ref)
	}
	if err != nil {
		return fail(err)
	}
	revision, err := session.revision(cache)
	if err != nil {
		return fail(err)
	}
	index := session.sourceIndex(cache)
	if index.err != nil {
		return fail(index.err)
	}
	if source.SkillPath != "" {
		if _, err := sourceTreeSpec(revision, source.SkillPath); err != nil {
			return fail(err)
		}
		scope := path.Clean(strings.ReplaceAll(source.SkillPath, "\\", "/"))
		filtered := sourceSkillIndex{paths: map[string][]string{}}
		for name, folders := range index.paths {
			for _, folder := range folders {
				if scope == "." || folder == scope || strings.HasPrefix(folder, scope+"/") {
					filtered.paths[name] = append(filtered.paths[name], folder)
				}
			}
		}
		index = filtered
	}
	selected := names
	if len(selected) == 0 {
		selected = slices.Sorted(maps.Keys(index.paths))
	}
	var packages []preparedPackage
	for _, name := range selected {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		folder, err := index.find(name)
		if err != nil {
			return fail(err)
		}
		tree, err := sourceTreeSpec(revision, folder)
		if err != nil {
			return fail(err)
		}
		digest, err := hashGitTree(session, cache, tree)
		if err != nil {
			return fail(err)
		}
		directory, err := session.materializeTree(cache, tree)
		if err != nil {
			return fail(err)
		}
		spec := source
		spec.SkillPath = folder
		packages = append(packages, preparedPackage{Name: name, Source: spec, Revision: revision, Digest: digest, Directory: directory})
	}
	if err := requireSelectedPackages(packages, names); err != nil {
		return fail(fmt.Errorf("prepare Git source: %w", err))
	}
	return packages, cleanup, nil
}
