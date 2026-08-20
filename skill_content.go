package main

import (
	"path/filepath"
	"strings"
)

func shouldIgnoreSkillContent(rel string) bool {
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		switch component {
		case ".git", ".hg", ".svn":
			return true
		}
		if strings.HasPrefix(component, ".skillctl-stage-") ||
			strings.HasPrefix(component, ".skillctl-backup-") ||
			strings.HasPrefix(component, ".skillctl-provider-snapshot-") ||
			strings.HasPrefix(component, ".skillctl-restore-") {
			return true
		}
	}
	return false
}
