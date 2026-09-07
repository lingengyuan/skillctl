package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type sourceSpec struct {
	Kind      string `json:"kind" toml:"kind"`
	URL       string `json:"url" toml:"url"`
	SkillPath string `json:"skillPath,omitempty" toml:"skill_path,omitempty"`
	Ref       string `json:"ref,omitempty" toml:"ref,omitempty"`
	Artifact  string `json:"artifact,omitempty" toml:"artifact,omitempty"`
}

type managedBinding struct {
	Environments []string `json:"environments,omitempty"`
	Manual       bool     `json:"manual,omitempty"`
	skillBinding
	Digest       string `json:"digest"`
	DisabledPath string `json:"disabledPath,omitempty"`
}

type managedPackage struct {
	Owner     string           `json:"owner,omitempty"`
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Source    sourceSpec       `json:"source"`
	Revision  string           `json:"revision"`
	Digest    string           `json:"digest"`
	Directory string           `json:"directory"`
	Bindings  []managedBinding `json:"bindings"`
	Pin       string           `json:"pin,omitempty"`
	External  bool             `json:"external,omitzero"`
	Provider  string           `json:"provider,omitempty"`
}

type packageCatalog struct {
	SchemaVersion int              `json:"schemaVersion"`
	Packages      []managedPackage `json:"packages"`
}

func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:16])
}

func sourceIdentity(source sourceSpec) string {
	return "skill-" + stableID(source.Kind, normalizeSource(source.URL), filepath.ToSlash(source.SkillPath), source.Ref)
}

func loadCatalog() (*packageCatalog, error) {
	path, err := stateFile("inventory.json")
	if err != nil {
		return nil, err
	}
	catalog := &packageCatalog{SchemaVersion: 1, Packages: []managedPackage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return catalog, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, catalog); err != nil {
		return nil, fmt.Errorf("invalid skill inventory: %w", err)
	}
	if catalog.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported inventory schema: %d", catalog.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, pkg := range catalog.Packages {
		if pkg.ID == "" || seen[pkg.ID] || !filepath.IsAbs(pkg.Directory) || !skillName.MatchString(pkg.Name) {
			return nil, fmt.Errorf("invalid inventory package: %s", pkg.ID)
		}
		seen[pkg.ID] = true
		if err := validateSourceURL(pkg.Source.URL); err != nil {
			return nil, err
		}
		for _, binding := range pkg.Bindings {
			if !filepath.IsAbs(binding.Path) || binding.Path == filepath.Dir(binding.Path) {
				return nil, fmt.Errorf("invalid inventory binding: %s", binding.Path)
			}
		}
	}
	return catalog, nil
}

func (c *packageCatalog) find(id string) *managedPackage {
	for i := range c.Packages {
		if c.Packages[i].ID == id {
			return &c.Packages[i]
		}
	}
	return nil
}

func (c *packageCatalog) byPath(path string) *managedPackage {
	for i := range c.Packages {
		pkg := &c.Packages[i]
		if samePath(pkg.Directory, path) {
			return pkg
		}
		for _, binding := range pkg.Bindings {
			if samePath(binding.Path, path) || (binding.DisabledPath != "" && samePath(binding.DisabledPath, path)) {
				return pkg
			}
		}
	}
	return nil
}

func (c *packageCatalog) mutation() (pathMutation, error) {
	path, err := stateFile("inventory.json")
	if err != nil {
		return pathMutation{}, err
	}
	change, err := mutation(path, "file")
	if err != nil {
		return change, err
	}
	slices.SortFunc(c.Packages, func(a, b managedPackage) int { return strings.Compare(a.ID, b.ID) })
	change.Data, err = json.Marshal(c)
	return change, err
}

func (c *packageCatalog) roots() []scanRoot {
	var roots []scanRoot
	for _, pkg := range c.Packages {
		for _, binding := range pkg.Bindings {
			if binding.Enabled {
				roots = append(roots, scanRoot{Path: binding.Path, Host: binding.Host, Scope: binding.Scope, Project: binding.Project, Required: true})
			}
		}
	}
	return roots
}

func packageStorePath(id, digest string) (string, error) {
	if !strings.HasPrefix(id, "skill-") || strings.ContainsAny(id, `/\`) || len(digest) != 64 {
		return "", fmt.Errorf("invalid store identity")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("invalid content digest")
	}
	dir, err := stateFile("store")
	return filepath.Join(dir, id, digest), err
}

func currentPackageDirectory(pkg managedPackage) string {
	if _, err := os.Stat(pkg.Directory); err == nil {
		return pkg.Directory
	}
	for _, binding := range pkg.Bindings {
		if !binding.Enabled && binding.DisabledPath != "" {
			return binding.DisabledPath
		}
	}
	return pkg.Directory
}
