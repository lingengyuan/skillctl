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

var version = "0.3.7"

const defaultNetworkTimeout = 10 * time.Second

type options struct {
	Paths       []string
	ConfigPath  string
	Names       []string
	Help        bool
	Source      string
	Ref         string
	SkillPath   string
	FromHistory bool
	JSON        bool
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
	if args[0] != "check" && args[0] != "update" && args[0] != "track" {
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
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
	var roots []scanRoot
	var manifests []manifest
	var managed []managedRoot
	networkTimeout := defaultNetworkTimeout
	ignoreMissing := false
	var err error
	roots, manifests, managed, networkTimeout, ignoreMissing, err = loadConfig(opt.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(opt.Paths) > 0 {
		roots = nil
		for _, path := range opt.Paths {
			roots = append(roots, scanRoot{Path: resolvePath(path, "."), Host: "manual", Scope: "local"})
		}
	}
	if opt.Timeout > 0 {
		networkTimeout = opt.Timeout
	}

	progress := io.Writer(io.Discard)
	if args[0] != "track" && !opt.JSON {
		progress = stderr
	}
	started := time.Now()
	scanStarted := time.Now()
	fmt.Fprintln(progress, "Scanning skills...")
	skills, scanFailed := scan(roots, ignoreMissing, stderr)
	fmt.Fprintf(progress, "Found %d skill instances (%s).\n", len(skills), time.Since(scanStarted).Round(time.Millisecond))
	if len(opt.Names) > 0 {
		var err error
		skills, err = selectSkills(skills, opt.Names)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
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
	state, err := loadTrackedState()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx := context.Background()
	if args[0] == "track" {
		if opt.FromHistory {
			if opt.Source != "" || opt.Ref != "" || opt.SkillPath != "" {
				fmt.Fprintln(stderr, "--from-history cannot be combined with --source, --ref, or --skill-path")
				return 2
			}
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
	fmt.Fprintf(progress, "Checking %d skill instances...\n", len(skills))
	reports, gitFailed := inspect(ctx, networkTimeout, args[0], skills, state, manifests, managed, resultWriter, progress)
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
		case "--json":
			opt.JSON = true
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

func selectSkills(all []skill, names []string) ([]skill, error) {
	byName := map[string][]skill{}
	for _, item := range all {
		byName[item.Name] = append(byName[item.Name], item)
	}
	var selected []skill
	for _, name := range names {
		matches := byName[name]
		if len(matches) == 0 {
			return nil, fmt.Errorf("skill not found: %s", name)
		}
		if len(matches) > 1 {
			var paths []string
			for _, match := range matches {
				paths = append(paths, match.Path)
			}
			return nil, fmt.Errorf("skill name is ambiguous: %s (%s)", name, strings.Join(paths, ", "))
		}
		selected = append(selected, matches[0])
	}
	return selected, nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  skillctl check [options] [skill...]
  skillctl update [options] [skill...]
  skillctl track [options] skill
  skillctl --version

Commands:
  check               check installed skills for updates
  update              safely update installed skills
  track               register the source of one copied skill

Options:
  --path PATH         replace configured roots; repeatable
  --config FILE       use a specific configuration file
  --timeout DURATION  set each network operation timeout, for example 30s
  --json              emit JSON for check or update
  --help              show this help

Track options:
  --source SOURCE     Git URL or local repository path
  --ref REF           branch, tag, or commit
  --skill-path PATH   repository-relative skill path
  --from-history      recover sources from trusted Codex/Claude install records

Options must appear before skill names.

Examples:
  skillctl check
  skillctl check --json
  skillctl update obsidian-assistant
  skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
  skillctl track --from-history
  skillctl track --from-history example
`)
}
