package app

import (
	"github.com/lingengyuan/skillctl/internal/fsutil"
)

type provenanceIndex struct {
	locks      vercelLocks
	state      *trackedState
	managed    []managedRoot
	hostClaims map[string]hostMetadataClaim
	ghClaims   map[string]ghSkillClaimResult
}

type provenanceClaims struct {
	vercel          vercelClaim
	vercelEvidence  []string
	hasVercel       bool
	tracked         *trackedEntry
	hasTracked      bool
	managedOwner    string
	managedEvidence []string
	host            hostMetadataClaim
	hasHost         bool
	gh              ghSkillClaimResult
}

func newProvenanceIndex(items []skill, state *trackedState, manifests []manifest, managed []managedRoot) (*provenanceIndex, map[string]string) {
	locks, lockErrors := loadVercelLocks(manifests)
	ghClaims := make(map[string]ghSkillClaimResult, len(items))
	for _, item := range items {
		if !item.Broken {
			ghClaims[item.Path] = readGHSkillClaim(item)
		}
	}
	return &provenanceIndex{
		locks:      locks,
		state:      state,
		managed:    managed,
		hostClaims: loadCodexCuratedClaims(items),
		ghClaims:   ghClaims,
	}, lockErrors
}

func (p *provenanceIndex) claims(item skill) provenanceClaims {
	vercel, evidence, hasVercel := p.locks.claim(item)
	tracked, hasTracked := p.state.findSkill(item)
	owner, managedEvidence := managedOwner(item, p.managed)
	host, hasHost := p.hostClaims[item.Path]
	return provenanceClaims{
		vercel:          vercel,
		vercelEvidence:  evidence,
		hasVercel:       hasVercel,
		tracked:         tracked,
		hasTracked:      hasTracked,
		managedOwner:    owner,
		managedEvidence: managedEvidence,
		host:            host,
		hasHost:         hasHost,
		gh:              p.ghClaims[item.Path],
	}
}

func (c provenanceClaims) count() int {
	count := 0
	if c.hasVercel {
		count++
	}
	if c.hasTracked {
		count++
	}
	if c.managedOwner != "" {
		count++
	}
	if c.hasHost {
		count++
	}
	if c.gh.Found {
		count++
	}
	return count
}

func managedOwner(item skill, roots []managedRoot) (string, []string) {
	for _, root := range roots {
		if fsutil.Within(root.Path, item.Path) || fsutil.SamePath(root.Path, item.Path) {
			return root.Owner, []string{root.Path}
		}
		for _, alias := range item.Aliases {
			if fsutil.Within(root.Path, alias) || fsutil.SamePath(root.Path, alias) {
				return root.Owner, []string{root.Path}
			}
		}
	}
	return "", nil
}
