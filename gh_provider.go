package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type ghSkillClaim struct {
	Repository string
	Ref        string
	TreeSHA    string
	SkillPath  string
	LocalPath  string
	Pinned     bool
}

type ghSkillClaimResult struct {
	Claim ghSkillClaim
	Found bool
	Err   error
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func readGHSkillClaim(item skill) ghSkillClaimResult {
	filePath := filepath.Join(item.Path, "SKILL.md")
	document, err := readSkillDocument(filePath)
	if err != nil {
		if errors.Is(err, errMissingFrontMatter) {
			return ghSkillClaimResult{}
		}
		return ghSkillClaimResult{Err: err}
	}
	found := false
	for key := range document.Metadata {
		if strings.HasPrefix(key, "github-") || key == "local-path" {
			found = true
			break
		}
	}
	if !found {
		return ghSkillClaimResult{}
	}
	claim := ghSkillClaim{
		Repository: metadataString(document.Metadata, "github-repo"),
		Ref:        metadataString(document.Metadata, "github-ref"),
		TreeSHA:    metadataString(document.Metadata, "github-tree-sha"),
		SkillPath:  metadataString(document.Metadata, "github-path"),
		LocalPath:  metadataString(document.Metadata, "local-path"),
		Pinned:     metadataBool(document.Metadata, "github-pinned"),
	}
	if claim.LocalPath != "" && claim.Repository == "" && claim.Ref == "" && claim.TreeSHA == "" && claim.SkillPath == "" {
		return ghSkillClaimResult{Claim: claim, Found: true}
	}
	if !githubRepository.MatchString(claim.Repository) || claim.Ref == "" || !validGitHubTreeSHA(claim.TreeSHA) {
		return ghSkillClaimResult{Claim: claim, Found: true, Err: fmt.Errorf("incomplete GitHub skill metadata")}
	}
	if _, err := githubSkillFolder(claim.SkillPath); err != nil {
		return ghSkillClaimResult{Claim: claim, Found: true, Err: fmt.Errorf("invalid github-path")}
	}
	return ghSkillClaimResult{Claim: claim, Found: true}
}

func validGitHubTreeSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func githubSkillFolder(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	if value == "" || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("empty or absolute path")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes repository")
	}
	if path.Base(clean) == "SKILL.md" {
		clean = path.Dir(clean)
	}
	return clean, nil
}

var ghRepositoryURL = func(repository string) string {
	return "https://github.com/" + repository + ".git"
}

func checkGHSkill(session *sourceSession, claim ghSkillClaim) (bool, error) {
	cache, err := session.source(ghRepositoryURL(claim.Repository), claim.Ref)
	if err != nil {
		return false, err
	}
	folder, err := githubSkillFolder(claim.SkillPath)
	if err != nil {
		return false, err
	}
	current, err := session.gitObject(cache, "HEAD:"+folder)
	if err != nil {
		return false, fmt.Errorf("resolve GitHub skill folder: %w", err)
	}
	if current.Type != "tree" {
		return false, fmt.Errorf("GitHub skill path is not a directory")
	}
	return !strings.EqualFold(current.Hash, claim.TreeSHA), nil
}

type ghSkillUpdateRequest struct {
	Name      string
	Directory string
}

var runGHSkillUpdater = executeGHSkillUpdater

func updateGHSkillProvider(ctx context.Context, session *sourceSession, item skill, claim ghSkillClaim, progress io.Writer) (ghSkillClaim, error) {
	snapshot, err := createDirectorySnapshot(item.Path)
	if err != nil {
		return ghSkillClaim{}, fmt.Errorf("create update backup: %w", err)
	}
	defer snapshot.cleanup()

	started := time.Now()
	fmt.Fprintf(progress, "Updating %s with GitHub CLI...\n", item.Name)
	_, err = runGHSkillUpdater(ctx, ghSkillUpdateRequest{Name: item.Name, Directory: item.Path}, progress)
	if err == nil {
		result := readGHSkillClaim(item)
		if !result.Found || result.Err != nil {
			err = fmt.Errorf("GitHub CLI did not preserve valid skill metadata")
		} else if result.Claim.Repository != claim.Repository || result.Claim.Ref != claim.Ref || result.Claim.SkillPath != claim.SkillPath {
			err = fmt.Errorf("GitHub CLI changed the skill source")
		} else if result.Claim.TreeSHA == claim.TreeSHA {
			err = fmt.Errorf("GitHub CLI did not advance the tree revision")
		} else if available, checkErr := checkGHSkill(session, result.Claim); checkErr != nil {
			err = checkErr
		} else if available {
			err = fmt.Errorf("post-update verification still finds an update")
		} else {
			name, readErr := readSkill(filepath.Join(item.Path, "SKILL.md"))
			if readErr != nil || name != item.Name {
				err = fmt.Errorf("updated directory does not contain skill %q", item.Name)
			} else {
				fmt.Fprintf(progress, "GitHub CLI update verified (%s).\n", time.Since(started).Round(time.Millisecond))
				return result.Claim, nil
			}
		}
	}
	if rollbackErr := snapshot.restore(item.Path); rollbackErr != nil {
		return ghSkillClaim{}, fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
	}
	return ghSkillClaim{}, err
}

func ghSkillUpdateArgs(request ghSkillUpdateRequest) []string {
	return []string{"skill", "update", request.Name, "--all", "--dir", filepath.Dir(request.Directory)}
}

func executeGHSkillUpdater(ctx context.Context, request ghSkillUpdateRequest, progress io.Writer) (string, error) {
	command, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("GitHub CLI was not found")
	}
	cmd := exec.CommandContext(ctx, command, ghSkillUpdateArgs(request)...)
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, progress)
	cmd.Stderr = io.MultiWriter(&output, progress)
	err = cmd.Run()
	if ctx.Err() != nil {
		return output.String(), fmt.Errorf("GitHub CLI update timeout: %w", ctx.Err())
	}
	if err != nil {
		message := oneLine(output.String())
		if message == "" {
			message = err.Error()
		}
		return output.String(), fmt.Errorf("GitHub CLI: %s", message)
	}
	return output.String(), nil
}

type directorySnapshot struct {
	directory string
}

func createDirectorySnapshot(installed string) (*directorySnapshot, error) {
	directory, err := os.MkdirTemp(filepath.Dir(installed), ".skillctl-provider-snapshot-")
	if err != nil {
		return nil, err
	}
	if err := copyDirectory(installed, directory); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &directorySnapshot{directory: directory}, nil
}

func (s *directorySnapshot) cleanup() {
	_ = os.RemoveAll(s.directory)
}

func (s *directorySnapshot) restore(installed string) error {
	if _, err := os.Lstat(installed); os.IsNotExist(err) {
		stage, err := os.MkdirTemp(filepath.Dir(installed), ".skillctl-restore-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := copyDirectory(s.directory, stage); err != nil {
			return err
		}
		return os.Rename(stage, installed)
	}
	replacement, err := beginDirectoryReplacement(installed, s.directory)
	if err != nil {
		return err
	}
	return replacement.commit()
}
