package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"time"
)

func inspectDetailed(ctx context.Context, networkTimeout time.Duration, action string, skills []skill, state *trackedState, manifests []manifest, managed []managedRoot, progress io.Writer) ([]report, bool) {
	provenance, lockErrors := newProvenanceIndex(skills, state, manifests, managed)
	var wellKnownTargets []wellKnownTarget
	for _, item := range skills {
		if item.Broken || item.Invalid != "" {
			continue
		}
		claims := provenance.claims(item)
		if claims.count() == 1 && claims.hasVercel && claims.vercel.Entry.SourceType == "well-known" {
			wellKnownTargets = append(wellKnownTargets, wellKnownTarget{Item: item, Claim: claims.vercel, Evidence: claims.vercelEvidence})
		}
	}
	wellKnownReports, wellKnownFailed := inspectWellKnown(ctx, networkTimeout, action, wellKnownTargets, state, progress)
	session := newSourceSession(ctx, networkTimeout, progress)
	defer session.close()
	var sourceRequests []sourceRequest
	for _, item := range skills {
		if item.Broken || item.Invalid != "" {
			continue
		}
		claims := provenance.claims(item)
		if claims.count() != 1 {
			continue
		}
		if claims.hasVercel && claims.vercel.Entry.SourceURL != "" && claims.vercel.Entry.SkillPath != "" && claims.vercel.Entry.SkillFolderHash != "" && (claims.vercel.Entry.SourceType == "github" || claims.vercel.Entry.SourceType == "git") {
			sourceRequests = append(sourceRequests, sourceRequest{Source: claims.vercel.Entry.SourceURL, Ref: claims.vercel.Entry.Ref, Skills: []string{item.Name}, Worktree: claims.vercel.Entry.SourceType == "git"})
		}
		if claims.hasTracked {
			sourceRequests = append(sourceRequests, sourceRequest{Source: claims.tracked.Source, Ref: claims.tracked.Ref, Skills: []string{item.Name}, Worktree: true})
		}
		if claims.gh.Found && claims.gh.Err == nil && claims.gh.Claim.Repository != "" && !claims.gh.Claim.Pinned {
			sourceRequests = append(sourceRequests, sourceRequest{Source: ghRepositoryURL(claims.gh.Claim.Repository), Ref: claims.gh.Claim.Ref, Skills: []string{item.Name}})
		}
	}
	session.prefetch(sourceRequests)
	reports := make([]report, 0, len(skills))
	remaining := make([]skill, 0, len(skills))
	failed := wellKnownFailed
	errorPaths := slices.Sorted(maps.Keys(lockErrors))
	for _, item := range skills {
		if item.Invalid != "" {
			r := reportFor(item, "filesystem", "unknown", nil, "unknown", "invalid skill", false, "report-only", item.Invalid)
			r.State, r.ReasonCode = "invalid", item.IssueCode
			reports = append(reports, r)
			failed = true
			continue
		}
		if item.Broken {
			reports = append(reports, reportFor(item, "filesystem", "unknown", nil, "broken", "broken link -> "+item.LinkTarget, false, "report-only", ""))
			continue
		}
		manifestBad := false
		for _, path := range errorPaths {
			message := lockErrors[path]
			matches := within(manifestInstallRoot(manifests, path), item.Path)
			for _, alias := range item.Aliases {
				matches = matches || within(manifestInstallRoot(manifests, path), alias)
			}
			if matches {
				reports = append(reports, reportFor(item, "vercel-skills-lock", "unknown", []string{path}, "unknown", "provider manifest unsupported", false, "report-only", message))
				failed = true
				manifestBad = true
				break
			}
		}
		if manifestBad {
			continue
		}
		claims := provenance.claims(item)
		claim, evidence, found := claims.vercel, claims.vercelEvidence, claims.hasVercel
		ghClaim := claims.gh
		hostClaim, hasHostClaim := claims.host, claims.hasHost
		owner, managedEvidence := claims.managedOwner, claims.managedEvidence
		tracked, isTracked := claims.tracked, claims.hasTracked
		if claims.count() > 1 {
			allEvidence := slices.Clone(managedEvidence)
			allEvidence = append(allEvidence, evidence...)
			if isTracked {
				allEvidence = append(allEvidence, tracked.Source)
			}
			if ghClaim.Found {
				allEvidence = append(allEvidence, filepath.Join(item.Path, "SKILL.md"))
			}
			if hasHostClaim {
				allEvidence = append(allEvidence, hostClaim.Evidence...)
			}
			reports = append(reports, reportFor(item, "ambiguous", "unknown", allEvidence, "unknown", "ambiguous provenance", false, "report-only", "authoritative claims conflict"))
			failed = true
			continue
		}
		if owner != "" {
			r := reportFor(item, "codex-host", "host", managedEvidence, "none", "managed by "+owner, false, "report-only", "")
			r.State, r.ReasonCode = "managed", "host_managed"
			reports = append(reports, r)
			continue
		}
		if hasHostClaim {
			r := reportFor(item, hostClaim.Provider, hostClaim.Owner, hostClaim.Evidence, "clean", "managed by codex", false, "report-only", "")
			r.Revision = hostClaim.Revision
			r.State, r.ReasonCode = "managed", "host_managed"
			reports = append(reports, r)
			continue
		}
		if ghClaim.Found {
			evidence := []string{filepath.Join(item.Path, "SKILL.md")}
			r := reportFor(item, "gh-skill", "provider", evidence, "unknown", "GitHub skill metadata invalid", false, "report-only", "")
			r.Revision = ghClaim.Claim.TreeSHA
			if ghClaim.Err != nil {
				r.Error = oneLine(ghClaim.Err.Error())
				r.State, r.ReasonCode = "invalid", "invalid_provider_metadata"
				r.Status += ": " + r.Error
				failed = true
			} else if ghClaim.Claim.LocalPath != "" {
				r.Status = "managed from local path"
				r.State, r.ReasonCode = "managed", "host_managed"
			} else if ghClaim.Claim.Pinned {
				r.Status = "pinned"
				r.State, r.ReasonCode = "pinned", "pinned_revision"
				r.Executor = "gh-skill-cli"
			} else {
				r.Executor = "gh-skill-cli"
				available, err := checkGHSkill(session, ghClaim.Claim)
				r.UpdateAvailable = available
				if err == nil {
					r.Drift, err = checkGHLocalDrift(session, ghClaim.Claim, item.Path)
				}
				checkedReport(&r)
				if err != nil {
					r.Status = "GitHub skill check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					r.State, r.ReasonCode = "error", "provider_error"
					attachSourceFailure(&r, err)
					failed = true
				} else if action == "update" && available && r.Drift == "clean" {
					operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
					updated, err := updateGHSkillProvider(operationCtx, session, item, ghClaim.Claim, progress)
					cancel()
					if err != nil {
						r.Status = "GitHub skill update failed: " + oneLine(err.Error())
						r.Error = oneLine(err.Error())
						r.State, r.ReasonCode = "error", "provider_error"
						attachSourceFailure(&r, err)
						failed = true
					} else {
						r.Revision = updated.TreeSHA
						r.UpdateAvailable = false
						r.Status = "updated"
						checkedReport(&r)
					}
				} else if r.Drift == "modified" {
					r.Status = vercelStatus(action, available, r.Drift)
					checkedReport(&r)
				} else if available {
					r.Status = "update available"
				} else {
					r.Status = "up to date"
					checkedReport(&r)
				}
			}
			reports = append(reports, r)
			continue
		}
		if found {
			if claim.Entry.SourceType == "well-known" {
				if r, ok := wellKnownReports[item.Path]; ok {
					reports = append(reports, r)
				} else {
					reports = append(reports, reportFor(item, "vercel-skills-lock-v3", "provider", evidence, "unknown", "provider check failed", false, "report-only", "well-known result missing"))
					failed = true
				}
				continue
			}
			r := reportFor(item, "vercel-skills-lock-v3", "provider", evidence, "unknown", "provider check unsupported", false, "report-only", "")
			r.Revision = claim.Entry.SkillFolderHash
			if claim.Entry.SourceType != "github" && claim.Entry.SourceType != "git" {
				r.Status = "tracked source (updates unavailable): " + claim.Entry.SourceType
				r.State, r.ReasonCode = "unknown", "updates_unavailable"
			} else {
				r.Executor = "vercel-skills-cli"
				available, drift, err := checkVercelEntry(session, claim.Entry, item.Path)
				r.Drift = drift
				r.UpdateAvailable = available
				if err != nil {
					r.Status = "provider check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					r.State, r.ReasonCode = "error", "provider_error"
					attachSourceFailure(&r, err)
					failed = true
				} else if action == "update" && available && drift == "clean" {
					operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
					updated, err := updateVercelProvider(operationCtx, session, item, claim, progress)
					cancel()
					if err != nil {
						r.Status = "provider update failed: " + oneLine(err.Error())
						r.Error = oneLine(err.Error())
						r.State, r.ReasonCode = "error", "provider_error"
						attachSourceFailure(&r, err)
						failed = true
					} else {
						r.Revision = updated.SkillFolderHash
						r.Drift = "clean"
						r.UpdateAvailable = false
						r.Status = "updated"
						checkedReport(&r)
					}
				} else {
					r.Status = vercelStatus(action, available, r.Drift)
					checkedReport(&r)
				}
			}
			reports = append(reports, r)
			continue
		}
		remaining = append(remaining, item)
	}

	if len(remaining) > 0 {
		sink := newReportSink(remaining, state)
		failed = processGit(action, remaining, state, session, sink, sink) || failed
		reports = append(reports, sink.reports...)
	}
	slices.SortFunc(reports, func(a, b report) int {
		return cmp.Or(cmp.Compare(a.Identity, b.Identity), cmp.Compare(a.Path, b.Path))
	})
	return finalizeReports(reports), failed
}

func printTrackRepairHint(w io.Writer, reports []report) {
	count := 0
	name := ""
	for _, r := range reports {
		if r.Provider == "local-authoring" && r.Status == "local/untracked (no update source)" {
			count++
			name = r.Identity
		}
	}
	if count == 1 {
		fmt.Fprintf(w, "Hint: register its update source: skillctl track --source SOURCE_URL %s\n", name)
	}
	if count > 1 {
		fmt.Fprintf(w, "Hint: %d skills have no update source; register one with: skillctl track --source SOURCE_URL SKILL_NAME\n", count)
	}
}
