package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

var version = "0.3.9"

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
