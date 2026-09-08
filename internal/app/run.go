package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/lingengyuan/skillctl/internal/cli"
)

const defaultNetworkTimeout = 10 * time.Second

// options combines parsed flags with state used only during command execution.
type options struct {
	cli.Options
	allowInvalidState bool
	version           string
}

func parseCommand(args []string, stderr io.Writer) (options, int) {
	parsed, code := cli.Parse(args, stderr)
	return options{Options: parsed}, code
}

func validateOptions(command string, opt options) error {
	return cli.Validate(command, opt.Options)
}

// Run executes one CLI command with the supplied cancellation, version and output streams.
func Run(ctx context.Context, args []string, version string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		cli.Usage(stderr)
		return 2
	}
	if args[0] == "--version" {
		fmt.Fprintf(stdout, "skillctl %s\n", version)
		return 0
	}
	if args[0] == "--help" || args[0] == "help" {
		cli.Usage(stdout)
		return 0
	}
	command := args[0]
	if !knownCommand(command) {
		fmt.Fprintf(stderr, "unknown command: %s\n", command)
		return 2
	}
	opt, code := parseCommand(args[1:], stderr)
	opt.version = version
	if code != 0 {
		return code
	}
	if opt.Help {
		cli.Usage(stdout)
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
	return runManager(ctx, command, opt, stdout, stderr)
}
