package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

// reportDanglingLinks emits one error per distinct dangling link. Several roots
// can observe the same link (for example a shared directory consumed by many
// Agents), and repeating it buries the findings a user must act on.
//
// Each link is re-checked before it is reported, because an earlier repair may
// already have restored or removed it. Reporting a resolved problem would fail
// the command on an issue that no longer exists.
func reportDanglingLinks(add *func(level, code, message, path, skill string), links []skill, repaired map[string]bool) {
	seen := map[string]bool{}
	for _, item := range links {
		key := fsutil.PathKey(item.Path)
		if seen[key] || repaired[key] {
			continue
		}
		seen[key] = true
		if !isDanglingLink(item.Path) {
			continue
		}
		(*add)("error", "broken_link", "skill link target is unavailable: "+item.LinkTarget, item.Path, item.Name)
	}
}

// danglingLinkPlan separates repairable dangling links from ones that must be
// kept for manual review.
type danglingLinkPlan struct {
	remove []pathMutation
	// repaired holds the path keys that this plan will delete, so the caller
	// can drop those findings once the repair succeeds.
	repaired map[string]bool
	held     []string
}

// planDanglingLinks decides which dangling skill links `doctor --fix` may
// delete. It removes only links whose target parent directory still exists.
//
// Safety: when the target's parent is also missing, the target may live on a
// volume that is merely unmounted, so deleting the link would silently discard
// real configuration. Those links are held back and reported instead.
func planDanglingLinks(links []skill) danglingLinkPlan {
	plan := danglingLinkPlan{repaired: map[string]bool{}}
	seen := map[string]bool{}
	for _, item := range links {
		if item.Path == "" || !isDanglingLink(item.Path) {
			continue
		}
		target := resolvedLinkTarget(item)
		if target == "" {
			plan.held = append(plan.held, item.Path+": link target is unreadable")
			continue
		}
		if !parentExists(target) {
			plan.held = append(plan.held, fmt.Sprintf("%s: target parent %s is unavailable; verify the volume is mounted", item.Path, filepath.Dir(filepath.Clean(target))))
			continue
		}
		key := fsutil.PathKey(item.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		plan.remove = append(plan.remove, pathMutation{Path: item.Path, Kind: "remove"})
		plan.repaired[key] = true
	}
	return plan
}

// applyDanglingLinkPlan executes the repair as one recoverable operation.
func applyDanglingLinkPlan(plan danglingLinkPlan) (int, error) {
	if len(plan.remove) == 0 {
		return 0, nil
	}
	affected := make([]string, 0, len(plan.remove))
	for _, change := range plan.remove {
		affected = append(affected, change.Path)
	}
	operation, err := prepareOperation("doctor remove dangling links", plan.remove, affected)
	if err != nil {
		return 0, err
	}
	if err := operation.apply(); err != nil {
		return 0, err
	}
	return len(plan.remove), nil
}

func resolvedLinkTarget(item skill) string {
	if item.LinkTarget != "" {
		if filepath.IsAbs(item.LinkTarget) {
			return item.LinkTarget
		}
		return filepath.Join(filepath.Dir(item.Path), item.LinkTarget)
	}
	target, err := os.Readlink(item.Path)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(item.Path), target)
	}
	return target
}

// isDanglingLink reports whether path is a symlink whose target does not exist.
func isDanglingLink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(path)
	return err != nil
}

// parentExists reports whether the directory containing target is reachable.
func parentExists(target string) bool {
	info, err := os.Stat(filepath.Dir(filepath.Clean(target)))
	return err == nil && info.IsDir()
}
