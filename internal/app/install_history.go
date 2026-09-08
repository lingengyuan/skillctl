package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/gitstore"
	"github.com/lingengyuan/skillctl/internal/installhistory"
)

func trackFromInstallHistory(ctx context.Context, timeout time.Duration, items []skill, state *trackedState, manifests []manifest, managed []managedRoot, namesExplicit bool, stdout, stderr io.Writer) bool {
	provenance, _ := newProvenanceIndex(items, state, manifests, managed)
	rootCache := map[string]gitRootResult{}
	var pending []skill
	for _, item := range items {
		if item.Invalid != "" || item.Broken {
			continue
		}
		claims := provenance.claims(item)
		if claims.hasTracked {
			fmt.Fprintf(stdout, "%s: already tracked\n", item.Name)
			continue
		}
		root, gitClaim := findGitRoot(item.Path, rootCache)
		if gitClaim {
			relSkill, err := filepath.Rel(root, filepath.Join(item.Path, "SKILL.md"))
			gitClaim = err == nil && fsutil.Within(root, filepath.Join(item.Path, "SKILL.md")) && gitTracks(root, relSkill)
		}
		if claims.count() > 0 || gitClaim {
			fmt.Fprintf(stdout, "%s: already managed by existing metadata\n", item.Name)
			continue
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return false
	}
	candidates, err := readInstallHistory()
	if err != nil {
		fmt.Fprintf(stderr, "read install history: %s\n", oneLine(err.Error()))
		return true
	}
	return recoverInstallCandidates(ctx, timeout, pending, state, candidates, namesExplicit, stdout, stderr)
}

func recoverInstallCandidates(ctx context.Context, timeout time.Duration, pending []skill, state *trackedState, candidates map[string][]installhistory.Candidate, namesExplicit bool, stdout, stderr io.Writer) bool {
	failed := false
	session := newSourceSession(ctx, timeout, stdout)
	defer session.close()
	var requests []sourceRequest
	for _, item := range pending {
		for _, candidate := range append(slices.Clone(candidates[item.Name]), candidates[""]...) {
			if validateSourceURL(candidate.Source) == nil {
				requests = append(requests, sourceRequest{Source: gitstore.NormalizeSource(candidate.Source), Ref: candidate.Ref, Skills: []string{item.Name}, Worktree: true})
			}
		}
	}
	session.prefetch(requests)
	type recoveryFailure struct {
		message string
		names   []string
	}
	var failures []recoveryFailure
	failureIndex := map[string]int{}
	sourceSkills := map[string]sourceSkillIndex{}
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(stderr, "source recovery canceled: %v\n", err)
			return true
		}
		matches := append(slices.Clone(candidates[item.Name]), candidates[""]...)
		if len(matches) == 0 {
			fmt.Fprintf(stdout, "%s: no trusted install record\n", item.Name)
			if namesExplicit {
				failed = true
			}
			continue
		}
		verified := map[string]trackedEntry{}
		var lastErr error
		for _, candidate := range matches {
			if err := validateSourceURL(candidate.Source); err != nil {
				lastErr = err
				continue
			}
			source := gitstore.NormalizeSource(candidate.Source)
			cache, err := session.worktreeSource(source, candidate.Ref)
			if err != nil {
				lastErr = err
				continue
			}
			skillPath := candidate.SkillPath
			if skillPath == "" {
				index, cached := sourceSkills[cache]
				if !cached {
					index = scanSourceSkills(cache)
					sourceSkills[cache] = index
				}
				skillPath, err = index.find(item.Name)
				if err != nil {
					lastErr = err
					continue
				}
			}
			entry, err := verifyCopiedSkillInCache(item, source, candidate.Ref, skillPath, cache)
			if err != nil {
				lastErr = err
				continue
			}
			verified[historyEntryKey(entry)] = entry
		}
		switch len(verified) {
		case 0:
			message := oneLine(lastErr.Error())
			if _, label, _, ok := sourceErrorDetails(lastErr); ok {
				message = label + ": " + message
			}
			if index, ok := failureIndex[message]; ok {
				failures[index].names = append(failures[index].names, item.Name)
			} else {
				failureIndex[message] = len(failures)
				failures = append(failures, recoveryFailure{message: message, names: []string{item.Name}})
			}
			failed = true
		case 1:
			for _, entry := range verified {
				state.put(entry)
			}
			if err := state.save(); err != nil {
				fmt.Fprintf(stderr, "%s: save source state (%s)\n", item.Name, oneLine(err.Error()))
				failed = true
				continue
			}
			fmt.Fprintf(stdout, "%s: tracked from install history\n", item.Name)
		default:
			fmt.Fprintf(stderr, "%s: ambiguous verified install records\n", item.Name)
			failed = true
		}
	}
	for _, failure := range failures {
		fmt.Fprintf(stderr, "%s: install record found but verification failed (%s)\n", strings.Join(failure.names, ", "), failure.message)
	}
	return failed
}

func historyEntryKey(entry trackedEntry) string {
	return entry.Source + "\x00" + entry.Ref + "\x00" + entry.SkillPath
}

func readInstallHistory() (map[string][]installhistory.Candidate, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return installhistory.ReadRoots([]string{
		filepath.Join(codexHomePath(), "sessions"),
		filepath.Join(home, ".claude", "projects"),
	})
}
