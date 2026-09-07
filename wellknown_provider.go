package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	wellKnownSchemaV2      = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"
	wellKnownMaxIndexBytes = 2 << 20
	wellKnownMaxArchive    = 50 << 20
	wellKnownMaxFiles      = 1000
	wellKnownProviderName  = "vercel-skills-lock-v3"
)

type wellKnownIndex struct {
	Schema string                `json:"$schema"`
	Skills []wellKnownIndexEntry `json:"skills"`
}

type wellKnownIndexEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Digest      string `json:"digest"`
}

type wellKnownRemote struct {
	Name        string
	Type        string
	ArtifactURL string
	Digest      string
}

type wellKnownTarget struct {
	Item     skill
	Claim    vercelClaim
	Evidence []string
}

type wellKnownInspection struct {
	Target wellKnownTarget
	Remote wellKnownRemote
	Report report
}

type wellKnownUpdateRequest struct {
	Names         []string
	SourceBaseURL string
	ManifestPath  string
}

type preparedWellKnownSkill struct {
	Directory string
	Hash      string
}

var runWellKnownUpdater = executeWellKnownUpdater

func inspectWellKnown(ctx context.Context, networkTimeout time.Duration, action string, targets []wellKnownTarget, state *trackedState, progress io.Writer) (map[string]report, bool) {
	reports := make(map[string]report, len(targets))
	groups := make(map[string][]wellKnownTarget)
	for _, target := range targets {
		entry := target.Claim.Entry
		r := reportFor(target.Item, wellKnownProviderName, "provider", target.Evidence, "unknown", "provider check unsupported", false, "report-only", "")
		r.Revision = entry.WellKnownDigest
		reports[target.Item.Path] = r
		if entry.SourceBaseURL == "" || !validWellKnownDigest(entry.WellKnownDigest) {
			r.Status = "tracked source (updates unavailable): well-known"
			r.State, r.ReasonCode = "unknown", "updates_unavailable"
			reports[target.Item.Path] = r
			continue
		}
		key := target.Claim.ManifestPath + "\x00" + entry.SourceBaseURL
		groups[key] = append(groups[key], target)
	}

	inspectionsByGroup := make(map[string][]wellKnownInspection, len(groups))
	baselineChanged := false
	failed := false
	for key, group := range groups {
		operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		remotes, err := fetchWellKnownIndex(operationCtx, group[0].Claim.Entry.SourceBaseURL)
		cancel()
		if err != nil {
			for _, target := range group {
				r := reports[target.Item.Path]
				setWellKnownReportError(&r, "provider check failed", err)
				reports[target.Item.Path] = r
			}
			failed = true
			continue
		}

		for _, target := range group {
			r := reports[target.Item.Path]
			r.Executor = "vercel-skills-cli"
			remote, ok := remotes[target.Claim.Name]
			if !ok {
				setWellKnownReportError(&r, "provider check failed", fmt.Errorf("provider index removed skill %q", target.Claim.Name))
				reports[target.Item.Path] = r
				failed = true
				continue
			}
			available := remote.Digest != target.Claim.Entry.WellKnownDigest
			r.UpdateAvailable = available
			drift, changed, err := checkWellKnownDrift(ctx, networkTimeout, target, remote, state)
			baselineChanged = baselineChanged || changed
			r.Drift = drift
			if err != nil {
				setWellKnownReportError(&r, "provider check failed", err)
				reports[target.Item.Path] = r
				failed = true
				continue
			}
			r.Status = vercelStatus(action, available, drift)
			checkedReport(&r)
			if action == "update" && available && drift == "unknown" {
				r.Status = "update available, skipped (local baseline unavailable)"
			}
			reports[target.Item.Path] = r
			inspectionsByGroup[key] = append(inspectionsByGroup[key], wellKnownInspection{Target: target, Remote: remote, Report: r})
		}
	}

	if baselineChanged {
		if err := state.save(); err != nil {
			for path, r := range reports {
				if r.Drift == "clean" {
					setWellKnownReportError(&r, "provider check failed", fmt.Errorf("save provider baseline: %w", err))
					reports[path] = r
				}
			}
			return reports, true
		}
	}

	if action != "update" {
		return reports, failed
	}

	for _, inspections := range inspectionsByGroup {
		eligible := slices.DeleteFunc(slices.Clone(inspections), func(item wellKnownInspection) bool {
			return !item.Report.UpdateAvailable || item.Report.Drift != "clean" || item.Report.Error != ""
		})
		if len(eligible) == 0 {
			continue
		}
		err := updateWellKnownBatch(ctx, networkTimeout, eligible, state, progress)
		if err != nil {
			for _, item := range eligible {
				r := reports[item.Target.Item.Path]
				setWellKnownReportError(&r, "provider update failed", err)
				reports[item.Target.Item.Path] = r
			}
			failed = true
			continue
		}
		for _, item := range eligible {
			r := reports[item.Target.Item.Path]
			r.Revision = item.Remote.Digest
			r.Drift = "clean"
			r.UpdateAvailable = false
			r.Status = "updated"
			checkedReport(&r)
			reports[item.Target.Item.Path] = r
		}
	}

	return reports, failed
}

func setWellKnownReportError(r *report, prefix string, err error) {
	r.Error = oneLine(err.Error())
	r.State, r.ReasonCode = "error", "provider_error"
	r.Status = prefix + ": " + r.Error
}

func fetchWellKnownIndex(ctx context.Context, baseURL string) (map[string]wellKnownRemote, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid well-known source base URL")
	}
	paths := []string{".well-known/agent-skills/index.json", ".well-known/skills/index.json"}
	var lastErr error
	for _, path := range paths {
		indexURL := strings.TrimRight(baseURL, "/") + "/" + path
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build well-known index request: %w", err)
		}
		request.Header.Set("X-Skills-Update-Check", "1")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, wellKnownMaxIndexBytes+1))
		closeErr := response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			lastErr = fmt.Errorf("well-known index returned HTTP %d", response.StatusCode)
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read well-known index: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close well-known index response: %w", closeErr)
		}
		if len(body) > wellKnownMaxIndexBytes {
			return nil, fmt.Errorf("well-known index exceeds size limit")
		}
		var index wellKnownIndex
		if err := json.Unmarshal(body, &index); err != nil {
			return nil, fmt.Errorf("read well-known index: invalid JSON")
		}
		if index.Schema != wellKnownSchemaV2 {
			return nil, fmt.Errorf("unsupported well-known index schema")
		}
		parsedIndexURL, err := url.Parse(indexURL)
		if err != nil {
			return nil, fmt.Errorf("parse well-known index URL: %w", err)
		}
		remotes := make(map[string]wellKnownRemote, len(index.Skills))
		for _, entry := range index.Skills {
			if entry.Name == "" || (entry.Type != "archive" && entry.Type != "skill-md") || !validWellKnownDigest(entry.Digest) {
				continue
			}
			artifactReference, err := url.Parse(entry.URL)
			if err != nil {
				continue
			}
			artifactURL := parsedIndexURL.ResolveReference(artifactReference)
			if artifactURL.Scheme != "http" && artifactURL.Scheme != "https" {
				continue
			}
			remotes[entry.Name] = wellKnownRemote{Name: entry.Name, Type: entry.Type, ArtifactURL: artifactURL.String(), Digest: entry.Digest}
		}
		if len(remotes) == 0 {
			return nil, fmt.Errorf("well-known index contains no supported skills")
		}
		return remotes, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("fetch well-known index: %w", lastErr)
	}
	return nil, fmt.Errorf("well-known index was not found")
}

func validWellKnownDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func checkWellKnownDrift(ctx context.Context, networkTimeout time.Duration, target wellKnownTarget, remote wellKnownRemote, state *trackedState) (string, bool, error) {
	localHash, err := hashDirectory(target.Item.Path)
	if err != nil {
		return "unknown", false, fmt.Errorf("hash local skill: %w", err)
	}
	entry := target.Claim.Entry
	if baseline, ok := state.findProviderBaseline(target.Item.Path, wellKnownProviderName); ok && baseline.Revision == entry.WellKnownDigest {
		if baseline.InstalledHash == localHash {
			return "clean", false, nil
		}
		return "modified", false, nil
	}

	verification := remote
	if remote.Digest != entry.WellKnownDigest {
		verification = wellKnownRemote{Name: target.Claim.Name, Type: remote.Type, ArtifactURL: entry.SourceURL, Digest: entry.WellKnownDigest}
	}
	operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
	prepared, err := prepareWellKnownSkill(operationCtx, verification)
	cancel()
	if err != nil {
		if remote.Digest != entry.WellKnownDigest {
			return "unknown", false, nil
		}
		return "unknown", false, err
	}
	defer os.RemoveAll(prepared.Directory)
	changed := state.putProviderBaseline(providerBaseline{
		Path:          filepath.Clean(target.Item.Path),
		Provider:      wellKnownProviderName,
		Revision:      entry.WellKnownDigest,
		InstalledHash: prepared.Hash,
	})
	if prepared.Hash == localHash {
		return "clean", changed, nil
	}
	return "modified", changed, nil
}

func prepareWellKnownSkill(ctx context.Context, remote wellKnownRemote) (preparedWellKnownSkill, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.ArtifactURL, nil)
	if err != nil {
		return preparedWellKnownSkill{}, fmt.Errorf("build well-known artifact request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return preparedWellKnownSkill{}, fmt.Errorf("fetch well-known artifact: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, wellKnownMaxArchive+1))
	closeErr := response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return preparedWellKnownSkill{}, fmt.Errorf("well-known artifact returned HTTP %d", response.StatusCode)
	}
	if readErr != nil {
		return preparedWellKnownSkill{}, fmt.Errorf("read well-known artifact: %w", readErr)
	}
	if closeErr != nil {
		return preparedWellKnownSkill{}, fmt.Errorf("close well-known artifact response: %w", closeErr)
	}
	if len(body) > wellKnownMaxArchive {
		return preparedWellKnownSkill{}, fmt.Errorf("well-known artifact exceeds size limit")
	}
	digest := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(digest[:]) != remote.Digest {
		return preparedWellKnownSkill{}, fmt.Errorf("well-known artifact digest mismatch")
	}

	directory, err := os.MkdirTemp("", ".skillctl-well-known-")
	if err != nil {
		return preparedWellKnownSkill{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	if remote.Type == "skill-md" {
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), body, 0o644); err != nil {
			return preparedWellKnownSkill{}, err
		}
	} else if err := extractWellKnownTarGzip(body, directory); err != nil {
		return preparedWellKnownSkill{}, err
	}
	name, err := readSkill(filepath.Join(directory, "SKILL.md"))
	if err != nil || name != remote.Name {
		return preparedWellKnownSkill{}, fmt.Errorf("well-known artifact does not contain skill %q", remote.Name)
	}
	hash, err := hashDirectory(directory)
	if err != nil {
		return preparedWellKnownSkill{}, fmt.Errorf("hash well-known artifact: %w", err)
	}
	cleanup = false
	return preparedWellKnownSkill{Directory: directory, Hash: hash}, nil
}

func extractWellKnownTarGzip(data []byte, destination string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open well-known archive: %w", err)
	}
	defer reader.Close()

	archive := tar.NewReader(reader)
	files := 0
	var unpacked int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read well-known archive: %w", err)
		}
		files++
		if files > wellKnownMaxFiles {
			return fmt.Errorf("well-known archive contains too many files")
		}
		if header.Size < 0 || unpacked+header.Size > wellKnownMaxArchive {
			return fmt.Errorf("well-known archive exceeds unpacked size limit")
		}
		unpacked += header.Size
		clean, safe := artifactEntryPath(header.Name)
		if !safe {
			return fmt.Errorf("well-known archive contains unsafe path")
		}
		path := filepath.Join(destination, clean)
		if !within(destination, path) {
			return fmt.Errorf("well-known archive path escapes destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("well-known archive contains unsupported entry type")
		}
	}
	return nil
}

func updateWellKnownBatchNative(ctx context.Context, networkTimeout time.Duration, items []wellKnownInspection, state *trackedState, progress io.Writer) error {
	manifestPath := items[0].Target.Claim.ManifestPath
	baseURL := items[0].Target.Claim.Entry.SourceBaseURL
	previousBaselines := slices.Clone(state.ProviderBaselines)
	snapshots := make([]struct {
		item     wellKnownInspection
		snapshot *vercelUpdateSnapshot
	}, 0, len(items))
	for _, item := range items {
		snapshot, err := createVercelUpdateSnapshot(item.Target.Item.Path, manifestPath)
		if err != nil {
			for _, created := range snapshots {
				created.snapshot.cleanup()
			}
			return fmt.Errorf("create update backup: %w", err)
		}
		snapshots = append(snapshots, struct {
			item     wellKnownInspection
			snapshot *vercelUpdateSnapshot
		}{item: item, snapshot: snapshot})
	}
	defer func() {
		for _, item := range snapshots {
			item.snapshot.cleanup()
		}
	}()
	rollback := func(cause error) error {
		state.ProviderBaselines = previousBaselines
		var rollbackErrors []error
		for _, item := range snapshots {
			if err := item.snapshot.restore(item.item.Target.Item.Path, manifestPath); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		if len(rollbackErrors) > 0 {
			return fmt.Errorf("%w; rollback failed: %v", cause, errors.Join(rollbackErrors...))
		}
		return cause
	}

	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Target.Claim.Name)
	}
	slices.Sort(names)
	started := time.Now()
	fmt.Fprintf(progress, "Updating %d skills from %s with Vercel Skills...\n", len(names), sourceDisplayLabel(baseURL, ""))
	operationCtx, cancel := context.WithTimeout(ctx, scaledProviderTimeout(networkTimeout, len(items)))
	_, updateErr := runWellKnownUpdater(operationCtx, wellKnownUpdateRequest{Names: names, SourceBaseURL: baseURL, ManifestPath: manifestPath}, progress)
	cancel()
	if updateErr != nil {
		return rollback(updateErr)
	}

	for _, item := range items {
		updated, err := readVercelLockEntry(manifestPath, item.Target.Claim.Name)
		if err != nil {
			return rollback(err)
		}
		if updated.SourceType != "well-known" || updated.SourceBaseURL != baseURL || updated.SourceURL != item.Remote.ArtifactURL {
			return rollback(fmt.Errorf("provider changed the skill source"))
		}
		if updated.WellKnownDigest != item.Remote.Digest || updated.WellKnownDigest == item.Target.Claim.Entry.WellKnownDigest {
			return rollback(fmt.Errorf("provider did not advance the lock revision"))
		}
		operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		prepared, err := prepareWellKnownSkill(operationCtx, item.Remote)
		cancel()
		if err != nil {
			return rollback(err)
		}
		localHash, hashErr := hashDirectory(item.Target.Item.Path)
		_ = os.RemoveAll(prepared.Directory)
		if hashErr != nil {
			return rollback(hashErr)
		}
		if localHash != prepared.Hash {
			return rollback(fmt.Errorf("post-update verification failed for %q", item.Target.Claim.Name))
		}
		state.putProviderBaseline(providerBaseline{Path: filepath.Clean(item.Target.Item.Path), Provider: wellKnownProviderName, Revision: updated.WellKnownDigest, InstalledHash: localHash})
	}
	if err := state.save(); err != nil {
		return rollback(fmt.Errorf("save provider baseline: %w", err))
	}
	fmt.Fprintf(progress, "Vercel Skills batch update verified (%s).\n", time.Since(started).Round(time.Millisecond))
	return nil
}

func executeWellKnownUpdater(ctx context.Context, request wellKnownUpdateRequest, progress io.Writer) (string, error) {
	activeLock, err := activeVercelLockPath()
	if err != nil {
		return "", err
	}
	if !samePath(activeLock, request.ManifestPath) {
		return "", fmt.Errorf("configured manifest is not the active Vercel global lock: %s", request.ManifestPath)
	}
	command, err := exec.LookPath("skills")
	args := []string{"add", request.SourceBaseURL, "--skill"}
	args = append(args, request.Names...)
	args = append(args, "--global", "--yes")
	if err != nil {
		command, err = exec.LookPath("npx")
		if err != nil {
			return "", fmt.Errorf("provider: Vercel Skills CLI was not found; install Node.js or skills")
		}
		args = append([]string{"--yes", "skills"}, args...)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, progress)
	cmd.Stderr = io.MultiWriter(&output, progress)
	err = cmd.Run()
	if ctx.Err() != nil {
		return output.String(), fmt.Errorf("provider update timeout: %w", ctx.Err())
	}
	message := oneLine(output.String())
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return output.String(), fmt.Errorf("provider: Vercel Skills CLI: %s", message)
	}
	return output.String(), nil
}

func scaledProviderTimeout(timeout time.Duration, count int) time.Duration {
	if count <= 1 || timeout > time.Duration(1<<63-1)/time.Duration(count) {
		return timeout
	}
	return timeout * time.Duration(count)
}

func updateWellKnownBatch(ctx context.Context, networkTimeout time.Duration, items []wellKnownInspection, state *trackedState, progress io.Writer) error {
	if len(items) == 0 {
		return nil
	}
	paths := []string{items[0].Target.Claim.ManifestPath, state.path}
	affected := []string{}
	for _, item := range items {
		paths = appendUnique(paths, item.Target.Item.Path)
		affected = append(affected, item.Target.Item.Name+": "+item.Target.Item.Path)
	}
	operation, err := beginExternalOperation("update well-known", paths, affected)
	if err != nil {
		return err
	}
	return operation.finishExternal(updateWellKnownBatchNative(ctx, networkTimeout, items, state, progress))
}
