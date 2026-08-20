package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

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
