package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "0.3.8"

const defaultNetworkTimeout = 10 * time.Second

type options struct {
	Paths       []string
	ConfigPath  string
	Names       []string
	Hosts       []string
	Scopes      []string
	Help        bool
	Source      string
	Ref         string
	SkillPath   string
	FromHistory bool
	JSON        bool
	DryRun      bool
	AllMatches  bool
	Fix         bool
	Timeout     time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	if args[0] == "--version" {
		fmt.Fprintf(stdout, "skillctl %s\n", version)
		return 0
	}
	if args[0] == "--help" {
		usage(stdout)
		return 0
	}
	command := args[0]
	if command != "check" && command != "update" && command != "track" && command != "list" && command != "doctor" {
		fmt.Fprintf(stderr, "unknown command: %s\n", command)
		return 2
	}

	opt, code := parseCommand(args[1:], stderr)
	if code != 0 {
		return code
	}
	if opt.Help {
		usage(stdout)
		return 0
	}
	if err := validateOptions(command, opt); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	roots, manifests, managed, networkTimeout, ignoreMissing, err := loadConfig(opt.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(opt.Paths) > 0 {
		roots = nil
		ignoreMissing = false
		for _, path := range opt.Paths {
			roots = append(roots, scanRoot{Path: resolvePath(path, "."), Host: "manual", Scope: "local", Required: true})
		}
	}
	if opt.Timeout > 0 {
		networkTimeout = opt.Timeout
	}

	progress := io.Writer(io.Discard)
	if command != "track" && command != "list" && command != "doctor" && !opt.JSON {
		progress = stderr
	}
	started := time.Now()
	scanStarted := time.Now()
	fmt.Fprintln(progress, "Scanning skills...")
	skills, scanFailed := scan(roots, ignoreMissing, stderr)
	skills = filterSkills(skills, opt.Hosts, opt.Scopes)
	fmt.Fprintf(progress, "Found %d unique skills (%d installations, %s).\n", uniqueSkillCount(skills), len(skills), time.Since(scanStarted).Round(time.Millisecond))
	if len(opt.Names) > 0 {
		allowMultiple := opt.AllMatches || command == "check" || command == "list" || command == "doctor"
		skills, err = selectSkillsWithMode(skills, opt.Names, allowMultiple)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if command == "list" {
		if err := writeSkillList(stdout, skills, opt.JSON); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if scanFailed {
			return 1
		}
		return 0
	}

	state, stateErr := loadTrackedState()
	if command == "doctor" {
		var operationLock *commandLock
		if opt.Fix {
			operationLock, err = acquireCommandLock()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			defer operationLock.release()
		}
		findings, fixed, doctorFailed := diagnose(roots, skills, state, stateErr, opt.Fix)
		if err := writeDoctorReport(stdout, findings, fixed, opt.JSON); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if scanFailed || doctorFailed {
			return 1
		}
		return 0
	}
	if stateErr != nil {
		fmt.Fprintln(stderr, stateErr)
		return 2
	}

	if len(skills) == 0 {
		if opt.JSON {
			_, _ = stdout.Write([]byte("[]\n"))
		} else {
			fmt.Fprintln(stdout, "No skills found.")
		}
		if scanFailed {
			return 1
		}
		return 0
	}

	ctx := context.Background()
	mutating := command == "track" || command == "update" && !opt.DryRun
	var operationLock *commandLock
	if mutating {
		operationLock, err = acquireCommandLock()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer operationLock.release()
	}

	if command == "track" {
		if opt.FromHistory {
			if trackFromInstallHistory(ctx, networkTimeout, skills, state, manifests, managed, len(opt.Names) > 0, stdout, stderr) || scanFailed {
				return 1
			}
			return 0
		}
		if len(skills) != 1 {
			fmt.Fprintln(stderr, "track requires exactly one unambiguous skill")
			return 1
		}
		operationCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		err := trackCopiedSkill(operationCtx, skills[0], opt.Source, opt.Ref, opt.SkillPath, state)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "%s: failed (%s)\n", skills[0].Name, oneLine(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "%s: tracked\n", skills[0].Name)
		return 0
	}

	resultWriter := stdout
	if opt.JSON {
		resultWriter = io.Discard
	}
	action := command
	if command == "update" && opt.DryRun {
		action = "check"
	}
	fmt.Fprintf(progress, "Checking %d unique skills (%d installations)...\n", uniqueSkillCount(skills), len(skills))
	reports, gitFailed := inspect(ctx, networkTimeout, action, skills, state, manifests, managed, resultWriter, progress)
	reports = finalizeReports(reports)
	if opt.JSON {
		if err := json.NewEncoder(stdout).Encode(reports); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if scanFailed || gitFailed {
		fmt.Fprintf(progress, "Finished with errors (%s).\n", time.Since(started).Round(time.Millisecond))
		return 1
	}
	fmt.Fprintf(progress, "Finished (%s).\n", time.Since(started).Round(time.Millisecond))
	return 0
}

func parseCommand(args []string, stderr io.Writer) (options, int) {
	var opt options
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--help":
			opt.Help = true
			args = args[1:]
		case "--path":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--path requires a directory")
				return options{}, 2
			}
			opt.Paths = append(opt.Paths, args[1])
			args = args[2:]
		case "--config":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--config requires a file")
				return options{}, 2
			}
			opt.ConfigPath = args[1]
			args = args[2:]
		case "--host":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--host requires a host name")
				return options{}, 2
			}
			opt.Hosts = append(opt.Hosts, args[1])
			args = args[2:]
		case "--scope":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--scope requires a scope")
				return options{}, 2
			}
			opt.Scopes = append(opt.Scopes, args[1])
			args = args[2:]
		case "--json":
			opt.JSON = true
			args = args[1:]
		case "--dry-run":
			opt.DryRun = true
			args = args[1:]
		case "--all-matches":
			opt.AllMatches = true
			args = args[1:]
		case "--fix":
			opt.Fix = true
			args = args[1:]
		case "--timeout":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--timeout requires a positive duration")
				return options{}, 2
			}
			duration, err := time.ParseDuration(args[1])
			if err != nil || duration <= 0 {
				fmt.Fprintln(stderr, "--timeout requires a positive duration, for example 10s")
				return options{}, 2
			}
			opt.Timeout = duration
			args = args[2:]
		case "--source":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--source requires a Git URL or path")
				return options{}, 2
			}
			opt.Source = args[1]
			args = args[2:]
		case "--ref":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--ref requires a branch, tag, or commit")
			return options{}, 2
			}
			opt.Ref = args[1]
			args = args[2:]
		case "--skill-path":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "--skill-path requires a repository-relative path")
				return options{}, 2
			}
			opt.SkillPath = args[1]
			args = args[2:]
		case "--from-history":
			opt.FromHistory = true
			args = args[1:]
		default:
			fmt.Fprintf(stderr, "unknown option: %s\n", args[0])
			return options{}, 2
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			fmt.Fprintln(stderr, "options must appear before skill names")
			return options{}, 2
		}
	}
	opt.Names = args
	return opt, 0
}

func validateOptions(command string, opt options) error {
	if opt.DryRun && command != "update" {
		return fmt.Errorf("--dry-run is only valid with update")
	}
	if opt.Fix && command != "doctor" {
		return fmt.Errorf("--fix is only valid with doctor")
	}
	if command != "track" && (opt.Source != "" || opt.Ref != "" || opt.SkillPath != "" || opt.FromHistory) {
		return fmt.Errorf("--source, --ref, --skill-path, and --from-history are only valid with track")
	}
	if command == "track" && opt.JSON {
		return fmt.Errorf("--json is not supported with track")
	}
	if opt.FromHistory && (opt.Source != "" || opt.Ref != "" || opt.SkillPath != "") {
		return fmt.Errorf("--from-history cannot be combined with --source, --ref, or --skill-path")
	}
	if opt.AllMatches && len(opt.Names) == 0 {
		return fmt.Errorf("--all-matches requires at least one skill name")
	}
	return nil
}

func printSkills(w io.Writer, skills []skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.set(skills, message)
		return
	}
	for _, item := range skills {
		fmt.Fprintf(w, "%s: %s\n", item.Name, message)
	}
}

func reportFailure(w io.Writer, item skill, message string) {
	if sink, ok := w.(*reportSink); ok {
		sink.failure(item, message)
		return
	}
	fmt.Fprintf(w, "%s: failed (%s)\n", item.Name, message)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func filterSkills(all []skill, hosts, scopes []string) []skill {
	if len hosts) == 0 && len(scopes) == 0 {
		return all
	}
hostSet := stringSet(hosts)
	scopeSet := stringSet(scopes)
	filtered := make([]skill, 0, len(all))
	for _, item := range all {
		if len(hostSet) > 0 && !hostSet[strings.ToLower(item.Host)] {
			continue
		}
		if len(scopeSet) > 0 && !scopeSet[strings.ToLower(item.Scope)] {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = true
	}
	return result
}

func selectSkills(all []skill, names []string) ([]skill, error) {
	return selectSkillsWithMode(all, names, false)
}

func selectSkillsWithMode(all []skill, names []string, allMatches bool) ([]skill, error) {
	byName := map[string][]skill{}
	for _, item := range all {
		byName[item.Name] = append(byName[item.Name], item)
	}
	var selected []skill
	seenPaths := map[string]bool{}
	for _, name := range names {
		matches := byName[name]
		if len(matches) == 0 {
			return nil, fmt.Errorf("skill not found: %s", name)
		}
		if len(matches) > 1 && !allMatches {
			paths := make([]string, 0, len(matches))
			for _, match := range matches {
				paths = append(paths, match.Path)
		}
		return nil, fmt.Errorf("skill name %q is ambiguous across %d installations: %s; narrow the scan with --path/--host/--scope or pass --all-matches", name, len(matches), strings.Join(paths, ", "))
		}
		for _, match := range matches {
			key := canonicalPathKey(match.Path)
			if !seenPaths[key] {
				selected = append(selected, match)
				seenPaths[key] = true
			}
		}
	}
	return selected, nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  skillctl check [options] [skill...]
  skillctl update [options] [skill...]
  skillctl list [options] [skill...]
  skillctl doctor [options]
  skillctl track [options] skill
  skillctl --version

Commands:
  check               check installed skills for updates
  update              safely update installed skills
  list                list installed skills without network access
  doctor              diagnose local skill and source state
  track               register the source of one copied skill

Options:
  --path PATH         replace configured roots; repeatable
  --config FILE       use a specific configuration file
  --host HOST         include only one host; repeatable
  --scope SCOPE       include only one scope; repeatable
  --timeout DURATION  set each network operation timeout, for example 30s
  --json              emit structured JSON for check, update, list, or doctor
  --all-matches       operate on every installation sharing a requested name
  --help              show this help

Update options:
  --dry-run           report the update plan without changing files

Doctor options:
  --fix               remove stale tracked-source entries after verification

Track options:
  --source SOURCE     Git URL or local repository path
  --ref REF           branch, tag, or commit
  --skill-path PATH   repository-relative skill path
  --from-history      recover sources from trusted Codex/Claude install records

Options must appear before skill names.

Examples:
  skillctl check
  skillctl check --json
  skillctl list --host codex
  skillctl update --dry-run
  skillctl update obsidian-assistant
  skillctl doctor
  skillctl doctor --fix
  skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
  skillctl track --from-history
  skillctl track --from-history example
`)
}
