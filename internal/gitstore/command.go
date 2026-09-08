package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Output runs a local Git command and returns its trimmed combined output.
func Output(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// NetworkOutput runs a Git command with cancellation and a bounded wait for child output.
func NetworkOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("network timeout: %w", ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// NetworkOutputWithTimeout applies one operation timeout to a Git network command.
func NetworkOutputWithTimeout(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return NetworkOutput(operationCtx, dir, args...)
}

// NonInteractiveEnv disables interactive credentials for source-cache Git commands.
func NonInteractiveEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=Never",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
}
