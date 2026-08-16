package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type codexCuratedCache struct {
	Skills []codexCuratedEntry `json:"skills"`
}

type codexCuratedEntry struct {
	Name     string `json:"name"`
	RepoPath string `json:"repoPath"`
}

type hostMetadataClaim struct {
	Provider string
	Owner    string
	Evidence []string
	Revision string
}

// loadCodexCuratedClaims trusts only Codex's own cache mapping and an exact
// content match with its vendor checkout. A matching name alone is never a
// provenance claim.
func loadCodexCuratedClaims(skills []skill) map[string]hostMetadataClaim {
	claims := map[string]hostMetadataClaim{}
	codexHome := codexHomePath()
	if codexHome == "" {
		return claims
	}
	manifestPath := filepath.Join(codexHome, "vendor_imports", "skills-curated-cache.json")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return claims
	}
	var cache codexCuratedCache
	if err := json.Unmarshal(content, &cache); err != nil {
		return claims
	}

	vendorRoot := filepath.Join(codexHome, "vendor_imports", "skills")
	revision, _ := gitOutput(vendorRoot, "rev-parse", "HEAD")
	byInstallPath := make(map[string]codexCuratedEntry, len(cache.Skills))
	for _, entry := range cache.Skills {
		if entry.Name == "" || entry.RepoPath == "" || filepath.IsAbs(entry.RepoPath) {
			continue
		}
		byInstallPath[canonicalPathKey(filepath.Join(codexHome, "skills", entry.Name))] = entry
	}
	for _, item := range skills {
		entry, ok := byInstallPath[canonicalPathKey(item.Path)]
		if !ok {
			for _, alias := range item.Aliases {
				if candidate, found := byInstallPath[canonicalPathKey(alias)]; found {
					entry, ok = candidate, true
					break
				}
			}
		}
		if !ok || entry.Name != item.Name {
			continue
		}
		sourcePath := filepath.Join(vendorRoot, filepath.FromSlash(entry.RepoPath))
		if !within(vendorRoot, sourcePath) {
			continue
		}
		installedHash, err := hashDirectory(item.Path)
		if err != nil {
			continue
		}
		sourceHash, err := hashDirectory(sourcePath)
		if err != nil || installedHash != sourceHash {
			continue
		}
		claims[item.Path] = hostMetadataClaim{
			Provider: "codex-curated-cache",
			Owner:    "host",
			Evidence: []string{manifestPath, sourcePath},
			Revision: revision,
		}
	}
	return claims
}

func codexHomePath() string {
	if path := os.Getenv("CODEX_HOME"); path != "" {
		return resolvePath(path, ".")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}
