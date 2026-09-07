package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type preparedPackage struct {
	Name      string
	Source    sourceSpec
	Revision  string
	Digest    string
	Directory string
}

func validateSourceURL(value string) error {
	if strings.HasPrefix(value, "git@") {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid source URL")
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		if u.User != nil {
			return fmt.Errorf("credentials must not be embedded in source URLs; use the host credential helper")
		}
		for key := range u.Query() {
			key = strings.ToLower(key)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "signature") || strings.Contains(key, "credential") || key == "key" || key == "auth" {
				return fmt.Errorf("credential-bearing source URLs cannot be persisted")
			}
		}
	}
	return nil
}

func parseSource(value, ref, skillPath string) (sourceSpec, error) {
	if err := validateSourceURL(value); err != nil {
		return sourceSpec{}, err
	}
	path := resolvePath(value, ".")
	if info, err := os.Stat(path); err == nil {
		kind := "local"
		if !info.IsDir() {
			kind = "archive"
		}
		return sourceSpec{Kind: kind, URL: path, Ref: ref, SkillPath: skillPath}, nil
	}
	if strings.HasPrefix(value, "git+") {
		return sourceSpec{Kind: "git", URL: strings.TrimPrefix(value, "git+"), Ref: ref, SkillPath: skillPath}, nil
	}
	if !strings.Contains(value, "://") && !strings.HasPrefix(value, "git@") && strings.Count(value, "/") == 1 && !strings.HasPrefix(value, ".") {
		value = "https://github.com/" + strings.TrimSuffix(value, ".git") + ".git"
	}
	kind := "git"
	if u, err := url.Parse(value); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if u.Hostname() == "github.com" && strings.Contains(u.Path, "/tree/") {
			source, inferredRef, inferredPath := skillsSource(value)
			value = source
			if ref == "" {
				ref = inferredRef
			}
			if skillPath == "" {
				skillPath = inferredPath
			}
		} else if !strings.HasSuffix(u.Path, ".git") && u.Hostname() != "github.com" && u.Hostname() != "gitlab.com" {
			kind = "http"
		}
	}
	return sourceSpec{Kind: kind, URL: value, Ref: ref, SkillPath: skillPath}, nil
}

func preparePackages(ctx context.Context, source sourceSpec, names []string, offline bool) ([]preparedPackage, func(), error) {
	cleanup := func() {}
	if err := validateSourceURL(source.URL); err != nil {
		return nil, cleanup, err
	}
	if source.Kind == "well-known" || source.Kind == "http" {
		if offline {
			return nil, cleanup, fmt.Errorf("offline: source is not available without a locked cached artifact")
		}
		index, indexErr := fetchWellKnownIndex(ctx, source.URL)
		if indexErr == nil {
			var dirs []string
			cleanup = func() {
				for _, dir := range dirs {
					_ = os.RemoveAll(dir)
				}
			}
			var result []preparedPackage
			for name, remote := range index {
				if len(names) > 0 && !slices.Contains(names, name) {
					continue
				}
				if err := validateSourceURL(remote.ArtifactURL); err != nil {
					cleanup()
					return nil, func() {}, err
				}
				prepared, err := prepareWellKnownSkill(ctx, remote)
				if err != nil {
					cleanup()
					return nil, func() {}, err
				}
				dirs = append(dirs, prepared.Directory)
				spec := sourceSpec{Kind: "well-known", URL: source.URL, SkillPath: name, Artifact: remote.ArtifactURL}
				result = append(result, preparedPackage{Name: name, Source: spec, Revision: remote.Digest, Digest: prepared.Hash, Directory: prepared.Directory})
			}
			slices.SortFunc(result, func(a, b preparedPackage) int { return strings.Compare(a.Name, b.Name) })
			if err := requireSelectedPackages(result, names); err != nil {
				cleanup()
				return nil, func() {}, err
			}
			return result, cleanup, nil
		}
		if source.Kind == "well-known" {
			return nil, cleanup, indexErr
		}
		source.Kind = "archive"
	}
	root, revision := source.URL, ""
	switch source.Kind {
	case "git":
		cache, err := sourceCachePath("worktree", source.URL, source.Ref)
		if err != nil {
			return nil, cleanup, err
		}
		if offline {
			if !validWorktreeCache(cache) {
				return nil, cleanup, fmt.Errorf("offline: Git source is not cached")
			}
			root = cache
		} else {
			root, err = syncWorktreeSource(ctx, source.URL, source.Ref)
			if err != nil {
				return nil, cleanup, err
			}
		}
		revision, err = gitOutput(root, "rev-parse", "HEAD")
		if err != nil {
			return nil, cleanup, err
		}
	case "archive":
		body, err := readArtifact(ctx, source.URL, offline)
		if err != nil {
			return nil, cleanup, err
		}
		hash := sha256.Sum256(body)
		revision = "sha256:" + hex.EncodeToString(hash[:])
		root, err = os.MkdirTemp("", "skillctl-artifact-")
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(root) }
		if err := unpackArtifact(body, root); err != nil {
			cleanup()
			return nil, func() {}, err
		}
	case "local":
	default:
		return nil, cleanup, fmt.Errorf("unsupported source kind: %s", source.Kind)
	}
	discoveryRoot := root
	if source.SkillPath != "" {
		if filepath.IsAbs(source.SkillPath) {
			cleanup()
			return nil, func() {}, fmt.Errorf("skill path must be relative")
		}
		discoveryRoot = filepath.Join(root, filepath.FromSlash(source.SkillPath))
		if !within(root, discoveryRoot) {
			cleanup()
			return nil, func() {}, fmt.Errorf("skill path escapes source")
		}
	}
	var diagnostics bytes.Buffer
	items, failed := scan([]scanRoot{{Path: discoveryRoot, Host: "source", Scope: "source", Required: true}}, &diagnostics)
	if failed {
		cleanup()
		return nil, func() {}, fmt.Errorf("source contains invalid skills: %s", oneLine(diagnostics.String()))
	}
	var result []preparedPackage
	for _, item := range items {
		if len(names) > 0 && !slices.Contains(names, item.Name) {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		if !within(realRoot, item.Path) {
			cleanup()
			return nil, func() {}, fmt.Errorf("skill link escapes source: %s", item.Name)
		}
		rel, err := filepath.Rel(realRoot, item.Path)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		digest, err := hashDirectory(item.Path)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		spec := source
		spec.SkillPath = filepath.ToSlash(rel)
		itemRevision := revision
		if itemRevision == "" {
			itemRevision = "sha256:" + digest
		}
		result = append(result, preparedPackage{Name: item.Name, Source: spec, Revision: itemRevision, Digest: digest, Directory: item.Path})
	}
	if err := requireSelectedPackages(result, names); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return result, cleanup, nil
}

func requireSelectedPackages(packages []preparedPackage, names []string) error {
	if len(packages) == 0 {
		return fmt.Errorf("no matching skills found in source")
	}
	for _, name := range names {
		found := false
		for _, pkg := range packages {
			if pkg.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("skill not found in source: %s", name)
		}
	}
	return nil
}

func readArtifact(ctx context.Context, source string, offline bool) ([]byte, error) {
	if info, err := os.Stat(source); err == nil && info.Mode().IsRegular() {
		if info.Size() > wellKnownMaxArchive {
			return nil, fmt.Errorf("artifact exceeds size limit")
		}
		return os.ReadFile(source)
	}
	if offline {
		return nil, fmt.Errorf("offline: artifact is not cached")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("artifact returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, wellKnownMaxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(body) > wellKnownMaxArchive {
		return nil, fmt.Errorf("artifact exceeds size limit")
	}
	return body, nil
}

func unpackArtifact(body []byte, destination string) error {
	if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		if len(archive.File) > wellKnownMaxFiles {
			return fmt.Errorf("archive contains too many files")
		}
		var total uint64
		for _, entry := range archive.File {
			clean, safe := artifactEntryPath(entry.Name)
			if !safe || !within(destination, filepath.Join(destination, clean)) {
				return fmt.Errorf("archive contains unsafe path")
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("archive contains unsupported symlink")
			}
			target := filepath.Join(destination, clean)
			if entry.FileInfo().IsDir() {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
				continue
			}
			if entry.UncompressedSize64 > wellKnownMaxArchive || total+entry.UncompressedSize64 > wellKnownMaxArchive {
				return fmt.Errorf("archive exceeds unpacked size limit")
			}
			total += entry.UncompressedSize64
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			input, err := entry.Open()
			if err != nil {
				return err
			}
			mode := entry.Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				input.Close()
				return err
			}
			_, copyErr := io.CopyN(output, input, int64(entry.UncompressedSize64))
			closeErr := errors.Join(input.Close(), output.Close())
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		}
		return nil
	}
	if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		return extractWellKnownTarGzip(body, destination)
	}
	if _, err := extractFrontMatter(body); err != nil {
		return fmt.Errorf("expected a SKILL.md, ZIP or tar.gz artifact")
	}
	return os.WriteFile(filepath.Join(destination, "SKILL.md"), body, 0o644)
}

func artifactRevision(body []byte) string {
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// Archive names use slash separators on every host. Validate them before OS
// path conversion so a Unix absolute name, Windows drive, or alternate stream
// cannot become an apparently relative name on another platform.
func artifactEntryPath(name string) (string, bool) {
	if strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:`) {
		return "", false
	}
	clean := filepath.FromSlash(path.Clean(name))
	return clean, clean != "." && filepath.IsLocal(clean)
}
