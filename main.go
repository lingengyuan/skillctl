package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

var version = "0.0.3"

const defaultNetworkTimeout = 10 * time.Second

type options struct {
	allowInvalidState bool
	Paths             []string
	ConfigPath        string
	Names             []string
	Hosts             []string
	Scopes            []string
	Help              bool
	Source            string
	Ref               string
	SkillPath         string
	FromHistory       bool
	JSON              bool
	DryRun            bool
	AllMatches        bool
	Fix               bool
	Timeout           time.Duration
	JSONVersion       int
	Project           string
	Skills            []string
	Copy              bool
	Offline           bool
	Frozen            bool
	File              string
	Profile           string
	Output            string
	NoHistory         bool
	Package           bool
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
	if args[0] == "--help" || args[0] == "help" {
		usage(stdout)
		return 0
	}
	command := args[0]
	if !knownCommand(command) {
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
		if opt.JSONVersion == 2 {
			_ = outputResult(commandResult{Command: command, Diagnostics: []diagnostic{{Code: "invalid_arguments", Level: "error", Message: err.Error()}}}, opt, stdout, stderr, true)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}
	return runManager(context.Background(), command, opt, stdout, stderr)
}
