package cli

import (
	"fmt"
	"io"
	"slices"
)

// commandHelp documents one command: what it does, which options it accepts,
// and at least one runnable example. It stays beside Usage so every command
// answers `skillctl COMMAND --help` without a separate manual lookup.
type commandHelp struct {
	Summary  string
	Usage    string
	Details  []string
	Options  []string
	Examples []string
	SeeAlso  []string
}

var commandHelpText = map[string]commandHelp{
	"list": {
		Summary: "List local skills with their Agents, scope, and path.",
		Usage:   "skillctl list [SKILL...] [options]",
		Details: []string{
			"Without arguments every discovered skill is listed, including invalid and broken entries.",
			"Each line is: NAME [AGENT+AGENT, SCOPE] PATH. Every Agent that consumes the content is listed, and the path is one an Agent actually reads.",
			"Listing succeeds even when broken or invalid entries exist; add --strict to fail instead.",
			"Text output is unchanged by --json; select --json-version 2 for the structured envelope.",
		},
		Options:  []string{"--host HOST, -a", "--scope SCOPE", "--project PATH", "--path PATH", "--all-matches", "--strict", "--verbose, -v", "--json", "--json-version 2"},
		Examples: []string{"skillctl list", "skillctl list --host claude", "skillctl list my-skill --json-version 2"},
		SeeAlso:  []string{"show", "check", "diff"},
	},
	"show": {
		Summary: "Show one skill's source, revision, drift, and Agent bindings.",
		Usage:   "skillctl show [SKILL...] [options]",
		Details: []string{
			"Reports the recorded source, the local revision digest, and every binding.",
			"Capabilities state which lifecycle actions are available for the entry.",
			"Duplicate names are listed separately; disambiguate with --host, --scope, or --path.",
		},
		Options:  []string{"--host HOST, -a", "--scope SCOPE", "--project PATH", "--all-matches", "--json-version 2"},
		Examples: []string{"skillctl show my-skill", "skillctl show my-skill --host claude"},
		SeeAlso:  []string{"list", "diff", "history"},
	},
	"check": {
		Summary: "Check installed skills against their sources.",
		Usage:   "skillctl check [SKILL...] [options]",
		Details: []string{
			"Verifies provenance and reports how many skills are current, outdated, modified, or unverified.",
			"Validation may save trusted provenance and a baseline. Use --dry-run for a read-only report.",
			"Trusted installer records are recovered automatically; --no-history disables that.",
			"Exit code 1 means at least one skill failed; an out-of-date skill is reported, not failed.",
		},
		Options:  []string{"--dry-run", "--offline", "--no-history", "--recovery-timeout DURATION", "--timeout DURATION", "--json", "--json-version 2"},
		Examples: []string{"skillctl check", "skillctl check --dry-run", "skillctl check my-skill --offline"},
		SeeAlso:  []string{"update", "diff", "track", "doctor"},
	},
	"diff": {
		Summary: "Compare installed content with its source.",
		Usage:   "skillctl diff [SKILL...] [options]",
		Details: []string{
			"Prints a unified diff per skill; JSON output carries each diff in result.",
			"When no comparable source exists the command explains why instead of reporting a match.",
		},
		Options:  []string{"--offline", "--host HOST, -a", "--json-version 2"},
		Examples: []string{"skillctl diff", "skillctl diff my-skill"},
		SeeAlso:  []string{"check", "update"},
	},
	"doctor": {
		Summary: "Diagnose local state and recover safe interrupted operations.",
		Usage:   "skillctl doctor [--fix] [options]",
		Details: []string{
			"Without --fix the check is read-only and reports findings plus the exit status.",
			"--fix removes dangling skill links whose content was deleted, and clears stale tracked-source entries.",
			"--reinstall additionally installs such skills again when a source is on record, instead of only clearing the links. It requires --fix.",
			"A skill already present is never overwritten, and a skill with no recorded source is reported rather than guessed at.",
			"A link is kept when its target's parent directory is also missing, since the target may live on an unmounted volume. Those are reported as broken_link_kept.",
			"Repairs are recorded in history and can be undone with rollback.",
			"--quiet hides informational findings; --json keeps every per-root detail.",
		},
		Options:  []string{"--fix", "--reinstall", "--dry-run", "--quiet, -q", "--path PATH", "--json-version 2"},
		Examples: []string{"skillctl doctor", "skillctl doctor --quiet", "skillctl doctor --fix --dry-run", "skillctl doctor --fix", "skillctl doctor --fix --reinstall"},
		SeeAlso:  []string{"check", "history", "rollback"},
	},
	"search": {
		Summary: "Search public skills.sh or a specific source.",
		Usage:   "skillctl search QUERY [options]",
		Details: []string{
			"The default query uses the public skills.sh API. --source searches local, Git, well-known, or downloaded sources.",
			"--offline searches only local assets that were already discovered.",
			"Results are a discovery entry point; they are not verified or quality-assessed.",
		},
		Options:  []string{"--source SOURCE", "--offline", "--timeout DURATION", "--json", "--json-version 2"},
		Examples: []string{"skillctl search tdd", "skillctl search commit --source ./vendor/skills", "skillctl search --offline my-skill"},
		SeeAlso:  []string{"install", "show"},
	},
	"install": {
		Summary: "Install skills from Git, local directories, archives, or SKILL.md.",
		Usage:   "skillctl install SOURCE [options]",
		Details: []string{
			"SOURCE accepts a local directory, owner/repository, Git URL, GitHub tree URL, ZIP, tar.gz, standalone SKILL.md, or well-known v0.2 source.",
			"Select which skills to take with --skill. Without it every valid skill in the source is installed.",
			"Targets come from --host. Without it detected Agents are used; with none detected an interactive menu appears, or the command fails and lists valid names.",
			"Reinstalling identical content is a no-op that reports No changes.",
			"Use --project PATH for a project install, or --global for the user scope (the default).",
			"Omitting --skill installs every skill in the source. A large selection asks for confirmation first; --yes skips it.",
		},
		Options: []string{"--skill NAME, -s", "--host HOST, -a", "--scope SCOPE", "--project PATH", "--global, -g", "--ref REF", "--skill-path PATH", "--copy", "--offline", "--dry-run", "--timeout DURATION", "--json-version 2"},
		Examples: []string{
			"skillctl install ./my-skill --host codex --host claude",
			"skillctl install owner/repository --skill example --host claude",
			"skillctl install https://github.com/owner/repository.git --ref v1.0 --skill-path skills/example --host claude",
			"skillctl install ./skills.zip --skill example --project ./app --host claude",
		},
		SeeAlso: []string{"list", "enable", "update", "track", "remove"},
	},
	"enable": {
		Summary: "Add or restore Agent bindings for an installed skill.",
		Usage:   "skillctl enable SKILL [options]",
		Details: []string{
			"Restores a disabled binding, or adds a new Agent to an existing skill.",
			"Content that already lives in place keeps its location; a disabled directory is restored from its snapshot.",
			"A shared physical path cannot be enabled for only one Agent at a time.",
		},
		Options:  []string{"--host HOST, -a", "--project PATH", "--package", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl enable my-skill --host claude", "skillctl enable my-skill --host codex --project ./app"},
		SeeAlso:  []string{"disable", "remove", "show"},
	},
	"disable": {
		Summary: "Suspend Agent bindings while keeping installed content.",
		Usage:   "skillctl disable SKILL [options]",
		Details: []string{
			"An in-place directory is snapshotted before it is disabled so enable can restore it.",
			"Disabling a shared path affects every Agent that consumes it.",
			"Host plugins act on the whole package and require --package.",
		},
		Options:  []string{"--host HOST, -a", "--project PATH", "--package", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl disable my-skill --host claude", "skillctl disable plugin-skill --host claude --package"},
		SeeAlso:  []string{"enable", "remove"},
	},
	"remove": {
		Summary: "Remove selected Agent bindings for a skill.",
		Usage:   "skillctl remove SKILL [options]",
		Details: []string{
			"Only the chosen bindings are removed. Shared content, history, and other Agents are retained.",
			"Removal works even when the installed content is already gone, so a broken installation can always be cleaned up.",
			"Unreferenced store revisions are reclaimed later by config gc, not by remove.",
		},
		Options:  []string{"--host HOST, -a", "--project PATH", "--package", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl remove my-skill --host claude", "skillctl remove my-skill --host codex --project ./app"},
		SeeAlso:  []string{"disable", "config"},
	},
	"update": {
		Summary: "Advance installed skills while preserving owners and local edits.",
		Usage:   "skillctl update [SKILL...] [options]",
		Details: []string{
			"Updates all selected skills; without names every installed skill is considered.",
			"Local modifications, forks, or a missing upstream block an overwrite instead of silently discarding work.",
			"Independent sources may succeed partially; failures are reported per source.",
			"--file advances the revisions locked in a project manifest instead of local installs.",
		},
		Options:  []string{"--dry-run", "--offline", "--host HOST, -a", "--file FILE", "--profile NAME", "--recovery-timeout DURATION", "--json", "--json-version 2"},
		Examples: []string{"skillctl update --dry-run", "skillctl update my-skill", "skillctl update --file ./skillctl.toml"},
		SeeAlso:  []string{"check", "pin", "rollback", "sync"},
	},
	"pin": {
		Summary: "Fix a skill at its currently installed revision.",
		Usage:   "skillctl pin SKILL [options]",
		Details: []string{
			"Pinning records the installed revision so later updates do not move it.",
			"To install a different ref, use install --ref REF to create a separate identity instead.",
		},
		Options:  []string{"--host HOST, -a", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl pin my-skill", "skillctl unpin my-skill"},
		SeeAlso:  []string{"unpin", "update", "install"},
	},
	"unpin": {
		Summary:  "Release a pinned skill so updates can advance it.",
		Usage:    "skillctl unpin SKILL [options]",
		Details:  []string{"Removes the pin record; the next update may move the skill again."},
		Options:  []string{"--host HOST, -a", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl unpin my-skill"},
		SeeAlso:  []string{"pin", "update"},
	},
	"history": {
		Summary: "Inspect persistent operation records.",
		Usage:   "skillctl history [OPERATION_ID] [options]",
		Details: []string{
			"Lists recorded operations newest first, or shows one record in detail.",
			"Each record carries the before and after state used for rollback.",
			"History is retained so rollback and evidence review stay possible.",
		},
		Options:  []string{"--json", "--json-version 2"},
		Examples: []string{"skillctl history", "skillctl history OPERATION_ID --json-version 2"},
		SeeAlso:  []string{"rollback", "doctor"},
	},
	"rollback": {
		Summary: "Restore a verified operation.",
		Usage:   "skillctl rollback [OPERATION_ID] [options]",
		Details: []string{
			"Defaults to latest, the most recent committed operation, which may itself be a rollback.",
			"The current state must still match the record, so later edits are never overwritten.",
			"Plugin updates and removals cannot be rolled back when the host CLI has no exact-version restore.",
		},
		Options:  []string{"--dry-run", "--json-version 2"},
		Examples: []string{"skillctl rollback latest --dry-run", "skillctl rollback latest", "skillctl rollback OPERATION_ID"},
		SeeAlso:  []string{"history", "doctor"},
	},
	"track": {
		Summary: "Register the verified source of an existing copied skill.",
		Usage:   "skillctl track SKILL --source SOURCE [options]",
		Details: []string{
			"Content must match the source before provenance is recorded; a mismatched name or digest is refused.",
			"--from-history recovers provenance from trusted installer records instead of --source.",
			"--offline uses already verified cached content only.",
		},
		Options: []string{"--source SOURCE", "--ref REF", "--skill-path PATH", "--from-history", "--offline", "--timeout DURATION", "--json-version 2"},
		Examples: []string{
			"skillctl track my-skill --source https://github.com/example/skills.git --skill-path skills/my-skill",
			"skillctl track my-skill --from-history",
		},
		SeeAlso: []string{"check", "update", "install"},
	},
	"sync": {
		Summary: "Reconcile skillctl.toml and skillctl.lock for a project.",
		Usage:   "skillctl sync [--file FILE] [options]",
		Details: []string{
			"Installs what the manifest declares at the revisions the lock records.",
			"A missing lock, a changed declaration, a missing artifact, or a digest mismatch fails under --frozen.",
			"--offline resolves only from verified cached content.",
			"Ordinary sync keeps locked revisions; use update --file to advance them.",
		},
		Options:  []string{"--file FILE", "--profile NAME", "--frozen", "--offline", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl sync", "skillctl sync --frozen --offline", "skillctl sync --file ./skillctl.toml"},
		SeeAlso:  []string{"update", "import", "export", "profile"},
	},
	"import": {
		Summary: "Sync an exported environment manifest.",
		Usage:   "skillctl import [FILE] [options]",
		Details: []string{
			"Reads a manifest produced by export and reconciles the current environment to it.",
			"--frozen requires the exported exact revisions and fails on any mismatch.",
		},
		Options:  []string{"--frozen", "--offline", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl import ./shared/skillctl.toml", "skillctl import ./shared/skillctl.toml --frozen"},
		SeeAlso:  []string{"export", "sync"},
	},
	"export": {
		Summary: "Write a portable manifest, lock, and required local artifacts.",
		Usage:   "skillctl export [SKILL...] --output FILE [options]",
		Details: []string{
			"Local sources are copied next to the manifest so the export is self-contained.",
			"Remote addresses are written without credentials.",
		},
		Options:  []string{"--output FILE, -o", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl export --output ./shared/skillctl.toml", "skillctl export my-skill --output ./shared/skillctl.toml"},
		SeeAlso:  []string{"import", "sync"},
	},
	"profile": {
		Summary: "List declared profiles or activate one.",
		Usage:   "skillctl profile [list | use NAME] [--file FILE] [options]",
		Details: []string{
			"An active profile selects which declared skills an environment syncs.",
			"use all syncs every declaration; an empty profile disables its exclusive bindings.",
		},
		Options:  []string{"--file FILE", "--dry-run", "--json-version 2"},
		Examples: []string{"skillctl profile list --file ./skillctl.toml", "skillctl profile use default --file ./skillctl.toml"},
		SeeAlso:  []string{"sync", "import"},
	},
	"config": {
		Summary: "Show or manage local discovery configuration.",
		Usage:   "skillctl config [show | path | hosts | init | gc] [options]",
		Details: []string{
			"show prints the effective configuration without creating it; path prints its location.",
			"hosts lists supported Agent directories. init creates config.toml explicitly.",
			"gc removes store revisions that no binding, lock, or history reference.",
			"gc also reclaims source clone staging directories left behind by an interrupted run. Only stages idle for over a day are removed, so a clone still in progress is never touched.",
			"Ordinary queries never write the default configuration.",
		},
		Options:  []string{"--path PATH", "--dry-run", "--json", "--json-version 2"},
		Examples: []string{"skillctl config show", "skillctl config path", "skillctl config hosts", "skillctl config init", "skillctl config gc --dry-run"},
		SeeAlso:  []string{"doctor", "remove"},
	},
	"completion": {
		Summary: "Print a shell completion script.",
		Usage:   "skillctl completion bash|zsh|fish|powershell",
		Examples: []string{
			"skillctl completion bash > /etc/bash_completion.d/skillctl",
			"skillctl completion zsh > \"${fpath[1]}/_skillctl\"",
			"skillctl completion fish > ~/.config/fish/completions/skillctl.fish",
			"skillctl completion powershell | Out-String | Invoke-Expression",
		},
	},
	"version": {
		Summary:  "Print the installed version.",
		Usage:    "skillctl version | skillctl --version",
		Options:  []string{"--json"},
		Examples: []string{"skillctl version", "skillctl --version"},
	},
}

// CommandUsage writes help for one command. It returns false when the command
// has no dedicated help so the caller can fall back to the general reference.
func CommandUsage(w io.Writer, command string) bool {
	help, ok := commandHelpText[command]
	if !ok {
		return false
	}
	fmt.Fprintf(w, "%s\n\nUsage:\n  %s\n", help.Summary, help.Usage)
	if len(help.Details) > 0 {
		fmt.Fprint(w, "\nDetails:\n")
		for _, line := range help.Details {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(help.Options) > 0 {
		fmt.Fprint(w, "\nOptions:\n")
		for _, line := range help.Options {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(help.Examples) > 0 {
		fmt.Fprint(w, "\nExamples:\n")
		for _, line := range help.Examples {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(help.SeeAlso) > 0 {
		fmt.Fprintf(w, "\nSee also:\n  %s\n", joinNames(help.SeeAlso))
	}
	fmt.Fprint(w, "\nRun skillctl help for the full command reference.\n")
	return true
}

func joinNames(names []string) string {
	out := ""
	for index, name := range names {
		if index > 0 {
			out += ", "
		}
		out += name
	}
	return out
}

// Commands returns every command name that has dedicated help, sorted.
func Commands() []string {
	names := make([]string, 0, len(commandHelpText))
	for name := range commandHelpText {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
