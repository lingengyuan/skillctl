package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lingengyuan/skillctl/internal/cli"
	"github.com/lingengyuan/skillctl/internal/fsutil"
)

// hostChoice is one selectable install target shown by the interactive prompt.
type hostChoice struct {
	Name string
	Path string
}

// interactiveInput reports whether stdin can safely prompt. A terminal is a
// character device that is not seekable; /dev/null and redirected files are
// seekable, and pipes are not character devices. All three must not prompt, or
// CI, redirected input, and `go test` would hang waiting for a reply.
func interactiveInput(stdin io.Reader) bool {
	file, ok := stdin.(*os.File)
	if !ok || file != os.Stdin {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	_, seekErr := file.Seek(0, io.SeekCurrent)
	return seekErr != nil
}

// detectHosts returns the Agents whose configuration directory already exists,
// mirroring the non-interactive detection used when --host is omitted.
func detectHosts() []string {
	var hosts []string
	for _, host := range agentHosts() {
		detectionPath := filepath.Dir(host.UserPath)
		if host.Name == "codex" {
			detectionPath = codexHomePath()
		}
		if _, err := os.Stat(detectionPath); err == nil {
			hosts = append(hosts, host.Name)
		}
	}
	return hosts
}

// installHostChoices lists every Agent that can receive an install for the
// requested scope. It seeds from every installable Agent, then adds configured
// roots, so the menu never hides a target that --host accepts.
func installHostChoices(roots []scanRoot, scope, project string) []hostChoice {
	paths := map[string]string{}
	var order []string
	add := func(name, path string) {
		if name == "" || name == "universal" {
			// "universal" is the shared-directory alias used by discovery.
			// It resolves to more than one root, so it cannot be an install
			// target; offering it would fail with "multiple roots".
			return
		}
		if existing, seen := paths[name]; seen {
			if existing == "" {
				paths[name] = path
			}
			return
		}
		paths[name] = path
		order = append(order, name)
	}
	for _, host := range installedHostRows() {
		path := host.UserPath
		if scope == "project" {
			path = ""
			if project != "" && host.ProjectPath != "" {
				path = filepath.Join(project, filepath.FromSlash(host.ProjectPath))
			}
		}
		add(host.Name, path)
	}
	for _, root := range roots {
		if root.Scope != scope {
			continue
		}
		if scope == "project" && root.Project != "" && project != "" && !fsutil.SamePath(root.Project, project) {
			continue
		}
		add(root.Host, root.Path)
	}
	slices.Sort(order)
	choices := make([]hostChoice, 0, len(order))
	for _, name := range order {
		choices = append(choices, hostChoice{Name: name, Path: paths[name]})
	}
	return choices
}

// resolveInstallHosts chooses install targets when the caller gave no --host:
// explicit flags win, then detected Agents, then an interactive prompt. Without
// a usable terminal it fails and lists every valid Agent name.
func resolveInstallHosts(opt options, roots []scanRoot, scope, project string) ([]string, error) {
	if len(opt.Hosts) > 0 {
		return slices.Clone(opt.Hosts), nil
	}
	if detected := detectHosts(); len(detected) > 0 {
		return detected, nil
	}
	if interactiveInput(opt.stdin) {
		return promptInstallHosts(opt.stdin, opt.stderr, installHostChoices(roots, scope, project))
	}
	return nil, noAgentError()
}

// promptInstallHosts renders a numbered menu of every selectable Agent and
// reads one selection. Reply with a number or a name; empty input takes 1.
func promptInstallHosts(stdin io.Reader, stderr io.Writer, choices []hostChoice) ([]string, error) {
	if len(choices) == 0 {
		return nil, noAgentError()
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintln(stderr, "No Agent detected. Choose an install target:")
	for index, choice := range choices {
		fmt.Fprintf(stderr, "  %d) %-12s %s\n", index+1, choice.Name, choice.Path)
	}
	fmt.Fprintf(stderr, "Target [1-%d, default 1]: ", len(choices))
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return nil, noAgentError()
	}
	names := make([]string, 0, len(choices))
	for _, choice := range choices {
		names = append(names, choice.Name)
	}
	hosts, err := parseHostSelection(line, names)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stderr, "Installing to: %s\n", strings.Join(hosts, ", "))
	return hosts, nil
}

// parseHostSelection accepts Agent names or 1-based menu numbers separated by
// commas, spaces, or semicolons. Empty input selects the first entry.
func parseHostSelection(input string, names []string) ([]string, error) {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return []string{names[0]}, nil
	}
	var result []string
	for _, field := range fields {
		name := ""
		if index, err := strconv.Atoi(field); err == nil {
			if index < 1 || index > len(names) {
				return nil, fmt.Errorf("target %s is out of range 1-%d", field, len(names))
			}
			name = names[index-1]
		} else {
			candidate := cli.NormalizeHost(field)
			if slices.Contains(names, candidate) {
				name = candidate
			}
			if name == "" {
				return nil, fmt.Errorf("unknown Agent %q; choose one of: %s", field, strings.Join(names, ", "))
			}
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result, nil
}

// noAgentError names every installable Agent so the non-interactive failure is
// directly actionable instead of requiring another lookup.
func noAgentError() error {
	return fmt.Errorf("no Agent detected; choose one with --host (available: %s)", validHostNames())
}

func validHostNames() string {
	rows := installedHostRows()
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return strings.Join(names, ", ")
}

// bulkInstallThreshold is the number of skills that triggers a confirmation
// when the caller did not name the skills to install.
const bulkInstallThreshold = 10

// confirmBulkInstall stops an accidental mass install. Omitting --skill takes
// every valid skill in a source, which is easy to do by mistake and tedious to
// undo, so a large selection is confirmed first.
//
// Selection is skipped when the caller named skills with --skill, when a
// machine-readable format was requested, and when --yes was passed. Without a
// usable terminal the command fails rather than installing unreviewed.
func confirmBulkInstall(packages []preparedPackage, opt options, named bool, stdout, stderr io.Writer) error {
	count := len(packages)
	if named || count < bulkInstallThreshold || opt.Yes {
		return nil
	}
	names := make([]string, 0, count)
	for _, pkg := range packages {
		names = append(names, pkg.Name)
	}
	slices.Sort(names)
	if opt.JSON || opt.JSONVersion == 2 {
		// Machine output cannot ask a question, so the caller must opt in.
		return fmt.Errorf("refusing to install %d skills without --skill; pass --skill NAME or --yes (%s)", count, previewNames(names))
	}
	if !interactiveInput(opt.stdin) {
		return fmt.Errorf("refusing to install %d skills without --skill; pass --skill NAME or --yes (%s)", count, previewNames(names))
	}
	out := stderr
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "This source contains %d skills, and no --skill was given:\n", count)
	fmt.Fprintf(out, "  %s\n", previewNames(names))
	fmt.Fprint(out, "Install all of them? [y/N]: ")
	line, err := bufio.NewReader(opt.stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("cancelled")
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("cancelled; re-run with --skill NAME to select specific skills")
	}
	return nil
}

// previewNames renders a bounded, comma-separated list so a long selection
// stays readable in a terminal and in an error message.
func previewNames(names []string) string {
	const limit = 8
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:limit], ", ") + fmt.Sprintf(", and %d more", len(names)-limit)
}
