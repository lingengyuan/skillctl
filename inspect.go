package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"
)

func inspect(ctx context.Context, networkTimeout time.Duration, action string, skills []skill, state *trackedState, manifests []manifest, managed []managedRoot, stdout, progress io.Writer) ([]report, bool) {
	provenance, lockErrors := newProvenanceIndex(skills, state, manifests, managed)
	session := newSourceSession(ctx, networkTimeout, progress)
	defer session.close()
	var sourceRequests []sourceRequest
	for _, item := range skills {
		if item.Broken {
			continue
		}
		claims := provenance.claims(item)
		if claims.count() != 1 {
			continue
		}
		if claims.hasVercel && claims.vercel.Entry.SourceURL != "" && claims.vercel.Entry.SkillPath != "" && claims.vercel.Entry.SkillFolderHash != "" && (claims.vercel.Entry.SourceType == "github" || claims.vercel.Entry.SourceType == "git") {
			sourceRequests = append(sourceRequests, sourceRequest{Source: claims.vercel.Entry.SourceURL, Ref: claims.vercel.Entry.Ref})
		}
		if claims.hasTracked {
			sourceRequests = append(sourceRequests, sourceRequest{Source: claims.tracked.Source, Ref: claims.tracked.Ref})
		}
		if claims.gh.Found && claims.gh.Err == nil && claims.gh.Claim.Repository != "" && !claims.gh.Claim.Pinned {
			sourceRequests = append(sourceRequests, sourceRequest{Source: ghRepositoryURL(claims.gh.Claim.Repository), Ref: claims.gh.Claim.Ref})
		}
	}
	session.prefetch(sourceRequests)
	reports := make([]report, 0, len(skills))
	remaining := make([]skill, 0, len(skills))
	failed := false
	var errorPaths []string
	for path := range lockErrors {
		errorPaths = append(errorPaths, path)
	}
	sort.Strings(errorPaths)
	for _, item := range skills {
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
			allEvidence := append([]string{}, managedEvidence...)
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
			reports = append(reports, reportFor(item, "codex-host", "host", managedEvidence, "none", "managed by "+owner, false, "report-only", ""))
			continue
		}
		if hasHostClaim {
			r := reportFor(item, hostClaim.Provider, hostClaim.Owner, hostClaim.Evidence, "clean", "managed by codex", false, "report-only", "")
			r.Revision = hostClaim.Revision
			reports = append(reports, r)
			continue
		}
		if ghClaim.Found {
			evidence := []string{filepath.Join(item.Path, "SKILL.md")}
			r := reportFor(item, "gh-skill", "provider", evidence, "unknown", "GitHub skill metadata invalid", false, "report-only", "")
			r.Revision = ghClaim.Claim.TreeSHA
			if ghClaim.Err != nil {
				r.Error = oneLine(ghClaim.Err.Error())
				r.Status += ": " + r.Error
				failed = true
			} else if ghClaim.Claim.LocalPath != "" {
				r.Status = "managed from local path"
			} else if ghClaim.Claim.Pinned {
				r.Status = "pinned"
				r.Executor = "gh-skill-cli"
			} else {
				r.Executor = "gh-skill-cli"
				available, err := checkGHSkill(session, ghClaim.Claim)
				r.UpdateAvailable = available
				if err != nil {
					r.Status = "GitHub skill check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					failed = true
				} else if action == "update" && available {
					operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
					updated, err := updateGHSkillProvider(operationCtx, session, item, ghClaim.Claim, progress)
					cancel()
					if err != nil {
						r.Status = "GitHub skill update failed: " + oneLine(err.Error())
						r.Error = oneLine(err.Error())
						failed = true
					} else {
						r.Revision = updated.TreeSHA
						r.UpdateAvailable = false
						r.Status = "updated"
					}
				} else if available {
					r.Status = "update available"
				} else {
					r.Status = "up to date"
				}
			}
			reports = append(reports, r)
			continue
		}
		if found {
			r := reportFor(item, "vercel-skills-lock-v3", "provider", evidence, "unknown", "provider check unsupported", false, "report-only", "")
			r.Revision = claim.Entry.SkillFolderHash
			if claim.Entry.SourceType != "github" && claim.Entry.SourceType != "git" {
				r.Status = "unsupported source type: " + claim.Entry.SourceType
			} else {
				r.Executor = "vercel-skills-cli"
				available, drift, err := checkVercelEntry(session, claim.Entry, item.Path)
				r.Drift = drift
				r.UpdateAvailable = available
				if err != nil {
					r.Status = "provider check failed: " + oneLine(err.Error())
					r.Error = oneLine(err.Error())
					failed = true
				} else if action == "update" && available && drift == "clean" {
					operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
					updated, err := updateVercelProvider(operationCtx, session, item, claim, progress)
					cancel()
					if err != nil {
						r.Status = "provider update failed: " + oneLine(err.Error())
						r.Error = oneLine(err.Error())
						failed = true
					} else {
						r.Revision = updated.SkillFolderHash
						r.Drift = "clean"
						r.UpdateAvailable = false
						r.Status = "updated"
					}
				} else {
					r.Status = vercelStatus(action, available, r.Drift)
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
	reports = mergeReportsByIdentity(reports)
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Identity < reports[j].Identity || reports[i].Identity == reports[j].Identity && reports[i].Path < reports[j].Path
	})
	for _, r := range reports {
		printReport(stdout, r, false)
	}
	printTrackRepairHint(stdout, reports)
	return reports, failed
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
