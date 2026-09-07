package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func parseCommand(args []string, stderr io.Writer) (options, int) {
	var opt options
	fail := func(err error) (options, int) { fmt.Fprintln(stderr, err); return options{}, 2 }
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			opt.Names = append(opt.Names, args...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			opt.Names = append(opt.Names, arg)
			continue
		}
		key, value, inline := strings.Cut(arg, "=")
		boolean := true
		switch key {
		case "--help", "-h":
			opt.Help = true
		case "--json", "-j":
			opt.JSON = true
		case "--dry-run":
			opt.DryRun = true
		case "--all-matches":
			opt.AllMatches = true
		case "--fix":
			opt.Fix = true
		case "--from-history":
			opt.FromHistory = true
		case "--no-history":
			opt.NoHistory = true
		case "--copy":
			opt.Copy = true
		case "--offline":
			opt.Offline = true
		case "--frozen":
			opt.Frozen = true
		case "--package":
			opt.Package = true
		case "--global", "-g":
			opt.Scopes = append(opt.Scopes, "user")
		default:
			boolean = false
		}
		if boolean {
			if inline {
				return fail(fmt.Errorf("%s does not take a value", key))
			}
			continue
		}
		switch key {
		case "--path", "--config", "--host", "--agent", "-a", "--scope", "--timeout", "--source", "--ref", "--skill-path", "--skill", "-s", "--project", "--file", "--profile", "--output", "-o", "--json-version":
		default:
			return fail(fmt.Errorf("unknown option: %s", key))
		}
		if !inline {
			if len(args) == 0 || strings.HasPrefix(args[0], "--") {
				return fail(fmt.Errorf("%s requires a value", key))
			}
			value, args = args[0], args[1:]
		}
		if value == "" {
			return fail(fmt.Errorf("%s requires a non-empty value", key))
		}
		switch key {
		case "--path":
			opt.Paths = append(opt.Paths, value)
		case "--config":
			opt.ConfigPath = value
		case "--host", "--agent", "-a":
			opt.Hosts = append(opt.Hosts, canonicalHost(value))
		case "--scope":
			opt.Scopes = append(opt.Scopes, value)
		case "--source":
			opt.Source = value
		case "--ref":
			opt.Ref = value
		case "--skill-path":
			opt.SkillPath = value
		case "--skill", "-s":
			opt.Skills = append(opt.Skills, value)
		case "--project":
			opt.Project = value
		case "--file":
			opt.File = value
		case "--profile":
			opt.Profile = value
		case "--output", "-o":
			opt.Output = value
		case "--json-version":
			n, err := strconv.Atoi(value)
			if err != nil || (n != 1 && n != 2) {
				return fail(fmt.Errorf("--json-version must be 1 or 2"))
			}
			opt.JSON, opt.JSONVersion = true, n
		case "--timeout":
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return fail(fmt.Errorf("--timeout requires a positive duration, for example 10s"))
			}
			opt.Timeout = duration
		}
	}
	return opt, 0
}

func validateOptions(command string, opt options) error {
	if opt.Offline && command == "track" {
		return fmt.Errorf("track requires source verification; use check --offline for local inventory")
	}
	if command == "sync" && len(opt.Names) > 0 {
		return fmt.Errorf("sync takes --file and --profile, not positional skill names")
	}
	if opt.Package && command != "update" && command != "enable" && command != "disable" && command != "remove" {
		return fmt.Errorf("--package is only valid for native package operations")
	}

	if opt.DryRun && !isWriteCommand(command) && command != "check" && command != "track" && !(command == "doctor" && opt.Fix) {
		return fmt.Errorf("--dry-run is not valid with %s", command)
	}
	if opt.Fix && command != "doctor" {
		return fmt.Errorf("--fix is only valid with doctor")
	}
	if opt.FromHistory && command != "track" {
		return fmt.Errorf("--from-history is only valid with track")
	}
	if opt.Source != "" && command != "track" && command != "search" {
		return fmt.Errorf("--source is only valid with track or search")
	}
	if (opt.Ref != "" || opt.SkillPath != "") && command != "track" && command != "install" && command != "pin" {
		return fmt.Errorf("--ref/--skill-path are only valid with track, install, or pin")
	}
	if opt.FromHistory && (opt.Source != "" || opt.Ref != "" || opt.SkillPath != "") {
		return fmt.Errorf("--from-history cannot be combined with --source, --ref, or --skill-path")
	}
	if opt.AllMatches && len(opt.Names) == 0 {
		return fmt.Errorf("--all-matches requires at least one skill name")
	}
	if opt.Frozen && command != "sync" && command != "import" {
		return fmt.Errorf("--frozen is only valid with sync or import")
	}
	return nil
}

func isWriteCommand(command string) bool {
	switch command {
	case "update", "install", "remove", "enable", "disable", "pin", "unpin", "rollback", "sync", "import", "export", "profile", "config":
		return true
	default:
		return false
	}
}
