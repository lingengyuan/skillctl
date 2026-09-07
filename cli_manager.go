package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"slices"
	"strings"
)

var commands = []string{"list", "show", "check", "diff", "doctor", "search", "install", "remove", "enable", "disable", "sync", "update", "pin", "unpin", "track", "history", "rollback", "export", "import", "profile", "config", "completion", "version"}

func knownCommand(command string) bool { return slices.Contains(commands, command) }

type commandResult struct {
	SchemaVersion int               `json:"schemaVersion"`
	Command       string            `json:"command"`
	Items         []skillAsset      `json:"items"`
	Diagnostics   []diagnostic      `json:"diagnostics"`
	Plan          *lifecyclePlan    `json:"plan,omitempty"`
	Operation     *operationRecord  `json:"operation,omitempty"`
	Operations    []operationRecord `json:"operations,omitempty"`
	Result        any               `json:"result,omitempty"`
}

func writeJSON(w io.Writer, value any) error { return json.MarshalWrite(w, value) }

func commandWrites(command string, opt options) bool {
	if opt.DryRun {
		return false
	}
	switch command {
	case "check", "track":
		return !opt.Offline
	case "doctor":
		return opt.Fix
	case "config":
		return len(opt.Names) > 0 && (opt.Names[0] == "init" || opt.Names[0] == "gc")
	case "profile":
		return len(opt.Names) > 0 && opt.Names[0] == "use"
	case "export":
		return true
	default:
		return isWriteCommand(command)
	}
}

func runManager(ctx context.Context, command string, opt options, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		if opt.JSONVersion == 2 {
			_ = writeJSON(stdout, commandResult{SchemaVersion: 2, Command: command, Items: []skillAsset{}, Diagnostics: []diagnostic{{Code: "command_failed", Level: "error", Message: err.Error()}}})
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	persist := commandWrites(command, opt)
	if persist {
		lock, err := acquireCommandLock()
		if err != nil {
			return fail(err)
		}
		defer lock.release()
		exceptions := []string{}
		if command == "rollback" && len(opt.Names) == 1 {
			exceptions = opt.Names
		}
		if err := recoverOperations(exceptions...); err != nil {
			return fail(err)
		}
	}
	switch command {
	case "version":
		if opt.JSON {
			return outputResult(commandResult{Command: command, Result: map[string]string{"version": version}}, opt, stdout, stderr, false)
		}
		fmt.Fprintf(stdout, "skillctl %s\n", version)
		return 0
	case "completion":
		return runCompletion(opt, stdout, stderr)
	case "history":
		history, err := readOperations()
		if err != nil {
			return fail(err)
		}
		if len(opt.Names) > 1 {
			return fail(fmt.Errorf("history accepts one operation id"))
		}
		if len(opt.Names) == 1 {
			history = slices.DeleteFunc(history, func(r operationRecord) bool { return r.ID != opt.Names[0] })
			if len(history) == 0 {
				return fail(fmt.Errorf("operation not found: %s", opt.Names[0]))
			}
		}
		if opt.JSONVersion == 2 {
			return outputResult(commandResult{Command: command, Result: history}, opt, stdout, stderr, false)
		}
		if opt.JSON {
			if err := writeJSON(stdout, history); err != nil {
				return fail(err)
			}
		} else {
			for _, op := range history {
				fmt.Fprintf(stdout, "%s  %s  %s  %s\n", op.ID, op.Created.Format("2006-01-02 15:04:05Z"), op.Command, op.State)
			}
		}
		return 0
	case "rollback":
		id := "latest"
		if len(opt.Names) > 1 {
			return fail(fmt.Errorf("rollback accepts one operation id"))
		}
		if len(opt.Names) == 1 {
			id = opt.Names[0]
		}
		if code, handled := rollbackNative(ctx, id, opt, stdout, stderr); handled {
			return code
		}
		changes, err := rollbackMutations(id)
		if err != nil {
			return fail(err)
		}
		plan := &lifecyclePlan{Command: "rollback", Affected: []string{}, Changes: []plannedChange{}}
		for _, change := range changes {
			if err := plan.add(change); err != nil {
				return fail(err)
			}
		}
		return executePlan(plan, nil, opt, stdout, stderr)
	case "config":
		return runConfig(ctx, opt, stdout, stderr)
	case "profile":
		return runProfile(ctx, opt, stdout, stderr)
	case "sync", "import":
		if command == "import" && len(opt.Names) > 0 {
			if len(opt.Names) != 1 || opt.File != "" {
				return fail(fmt.Errorf("import requires one manifest path"))
			}
			opt.File, opt.Names = opt.Names[0], nil
		}
		return runManifestSync(ctx, command, opt, stdout, stderr)
	case "search":
		return runSearch(ctx, opt, stdout, stderr)
	}
	if command == "update" && opt.File != "" {
		return runManifestSync(ctx, command, opt, stdout, stderr)
	}
	scanOpt := opt
	scanOpt.allowInvalidState = command == "doctor"
	if command == "enable" {
		scanOpt.Hosts = nil
		scanOpt.Scopes = nil
	}
	view, err := loadInventory(ctx, scanOpt, (command == "check" || command == "update") && !opt.Offline, persist)
	if err != nil {
		fail(err)
		return 2
	}
	if command == "install" {
		if len(opt.Names) != 1 {
			return fail(fmt.Errorf("install requires one source; select its skills with --skill"))
		}
		source, err := parseSource(opt.Names[0], opt.Ref, opt.SkillPath)
		if err != nil {
			return fail(err)
		}
		opctx, cancel := context.WithTimeout(ctx, view.timeout)
		defer cancel()
		packages, cleanup, err := preparePackages(opctx, source, opt.Skills, opt.Offline)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		plan := &lifecyclePlan{Command: command, Affected: []string{}, Changes: []plannedChange{}}
		for _, pkg := range packages {
			bindings, err := targetBindings(opt, view.roots, pkg.Name)
			if err != nil {
				return fail(err)
			}
			if err := planInstallPackage(plan, view.catalog, pkg, bindings, ""); err != nil {
				return fail(err)
			}
		}
		if err := plan.finish(view.catalog); err != nil {
			return fail(err)
		}
		return executePlan(plan, nil, opt, stdout, stderr)
	}
	if command == "export" {
		return runExport(view, opt, stdout, stderr)
	}
	allow := opt.AllMatches || command == "list" || command == "show" || command == "check" || command == "doctor" || command == "diff"
	selected, err := selectAssets(view.Items, opt.Names, allow)
	if err != nil {
		return fail(err)
	}
	result := commandResult{Command: command, Items: selected, Diagnostics: view.Diagnostics}
	if command == "list" {
		if opt.JSONVersion == 2 {
			return outputResult(result, opt, stdout, stderr, view.scanFailed)
		}
		items := skillsForAssets(view, selected, true)
		if err := writeSkillList(stdout, items, opt.JSON); err != nil {
			return fail(err)
		}
		if view.scanFailed {
			return 1
		}
		return 0
	}
	if command == "show" {
		return outputResult(result, opt, stdout, stderr, view.scanFailed)
	}
	if command == "doctor" {
		findings, fixed, failed := diagnose(view.roots, view.skills, view.state, view.stateErr, opt.Fix && !opt.DryRun)
		if opt.JSONVersion == 2 {
			result.Result = struct {
				Findings []doctorFinding `json:"findings"`
				Fixed    int             `json:"fixed"`
			}{findings, fixed}
			return outputResult(result, opt, stdout, stderr, failed || view.scanFailed)
		}
		if err := writeDoctorReport(stdout, findings, fixed, opt.JSON); err != nil {
			return fail(err)
		}
		if failed || view.scanFailed {
			return 1
		}
		return 0
	}
	if command == "track" {
		skills := skillsForAssets(view, selected, false)
		if opt.FromHistory {
			writer := stdout
			var diagnostics bytes.Buffer
			if opt.JSON {
				writer = io.Discard
			}
			failed := trackFromInstallHistory(ctx, view.timeout, skills, view.state, view.manifests, view.managed, len(opt.Names) > 0, writer, &diagnostics) || view.scanFailed
			if opt.JSON {
				result.Result = map[string]bool{"dryRun": opt.DryRun}
				if diagnostics.Len() > 0 {
					result.Diagnostics = append(result.Diagnostics, diagnostic{Code: "source_recovery_failed", Message: oneLine(diagnostics.String()), Level: "error"})
				}
				return outputResult(result, opt, stdout, stderr, failed)
			}
			fmt.Fprint(stderr, diagnostics.String())
			if failed {
				return 1
			}
			return 0
		}
		if len(skills) != 1 {
			return fail(fmt.Errorf("track requires exactly one unambiguous skill"))
		}
		if err := validateSourceURL(opt.Source); err != nil {
			return fail(err)
		}
		opctx, cancel := context.WithTimeout(ctx, view.timeout)
		defer cancel()
		if err := trackCopiedSkill(opctx, skills[0], opt.Source, opt.Ref, opt.SkillPath, view.state); err != nil {
			return fail(err)
		}
		if opt.JSON {
			provenance, _ := newProvenanceIndex(skills, view.state, view.manifests, view.managed)
			result.Items = []skillAsset{describeSkill(skills[0], provenance, nil)}
			result.Result = map[string]bool{"dryRun": opt.DryRun}
			return outputResult(result, opt, stdout, stderr, false)
		}
		fmt.Fprintf(stdout, "%s: tracked\n", skills[0].Name)
		return 0
	}
	if command == "diff" {
		return runDiff(ctx, view, selected, opt, stdout, stderr)
	}
	if command == "check" || command == "update" {
		return runInspection(ctx, view, selected, command, opt, stdout, stderr)
	}
	if len(opt.Names) == 0 {
		return fail(fmt.Errorf("%s requires a skill name or id", command))
	}
	for _, asset := range selected {
		if asset.Plugin != nil {
			return runPluginOperation(ctx, command, view, selected, opt, stdout, stderr)
		}
	}
	plan, err := planBindingChange(command, view, selected, opt)
	if err != nil {
		return fail(err)
	}
	return executePlan(plan, selected, opt, stdout, stderr)
}

func outputResult(result commandResult, opt options, stdout, stderr io.Writer, failed bool) int {
	result.SchemaVersion = 2
	if result.Items == nil {
		result.Items = []skillAsset{}
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []diagnostic{}
	}
	if opt.JSON {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		for _, asset := range result.Items {
			fmt.Fprintf(stdout, "%s [%s] %s (%s)\n  %s\n", asset.Name, asset.ID, asset.State, asset.Provider, asset.Path)
			if result.Command == "show" {
				if asset.Source != nil {
					fmt.Fprintf(stdout, "  Source: %s %s (%s)\n", asset.Source.URL, asset.Source.SkillPath, asset.Revision)
				}
				for _, b := range asset.Bindings {
					state := "disabled"
					if b.Enabled {
						state = "enabled"
					}
					fmt.Fprintf(stdout, "  %s/%s %s %s: %s\n", b.Host, b.Scope, state, b.Mode, b.Path)
				}
				for _, c := range []string{"check", "diff", "update", "enable", "disable", "remove", "pin", "rollback"} {
					cap := asset.Capabilities[c]
					fmt.Fprintf(stdout, "  %s: %t (%s) %s\n", c, cap.Supported, cap.Unit, cap.Reason)
				}
			}
		}
		if result.Plan != nil {
			if len(result.Plan.Changes) == 0 {
				fmt.Fprintln(stdout, "No changes.")
			} else {
				if opt.DryRun {
					fmt.Fprintln(stdout, "Plan (dry run):")
				}
				for _, affected := range result.Plan.Affected {
					fmt.Fprintf(stdout, "  Affects %s\n", affected)
				}
				for _, change := range result.Plan.Changes {
					fmt.Fprintf(stdout, "  %s %s", change.Action, change.Path)
					if change.Target != "" {
						fmt.Fprintf(stdout, " -> %s", change.Target)
					}
					fmt.Fprintln(stdout)
				}
			}
		}
		if result.Operation != nil {
			fmt.Fprintf(stdout, "Operation %s: %s\n", result.Operation.ID, result.Operation.State)
		}
		for _, d := range result.Diagnostics {
			fmt.Fprintf(stderr, "%s: %s\n", d.Code, d.Message)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func executePlan(plan *lifecyclePlan, items []skillAsset, opt options, stdout, stderr io.Writer) int {
	sortedAffected(plan)
	result := commandResult{Command: plan.Command, Items: items, Plan: plan}
	if !opt.DryRun && len(plan.mutations) > 0 {
		operation, err := prepareOperation(plan.Command, plan.mutations, plan.Affected)
		if err == nil {
			result.Operation = operation
			err = operation.apply()
		}
		if err != nil {
			result.Diagnostics = []diagnostic{{Code: "operation_failed", Message: err.Error(), Level: "error"}}
			return outputResult(result, opt, stdout, stderr, true)
		}
	}
	if !opt.DryRun && (items != nil || slices.Contains([]string{"install", "sync", "import", "update"}, plan.Command)) {
		catalog, err := loadCatalog()
		if err != nil {
			result.Diagnostics = []diagnostic{{Code: "post_operation_inventory_failed", Level: "error", Message: err.Error()}}
			return outputResult(result, opt, stdout, stderr, true)
		}
		result.Items = []skillAsset{}
		for _, pkg := range catalog.Packages {
			if len(pkg.Bindings) == 0 {
				continue
			}
			affected := false
			for _, prior := range items {
				affected = affected || prior.ID == pkg.ID || samePath(prior.Path, pkg.Directory)
			}
			for _, change := range plan.mutations {
				affected = affected || samePath(change.Path, pkg.Directory)
				for _, binding := range pkg.Bindings {
					affected = affected || samePath(change.Path, binding.Path)
				}
			}
			if affected {
				result.Items = append(result.Items, describePackage(pkg))
			}
		}
	}
	return outputResult(result, opt, stdout, stderr, false)
}

func skillsForAssets(view *inventoryView, assets []skillAsset, includeManaged bool) []skill {
	result := []skill{}
	for _, asset := range assets {
		found := false
		for _, item := range view.skills {
			if samePath(item.Path, asset.Path) {
				item.Bindings = asset.Bindings
				result = append(result, item)
				found = true
				break
			}
		}
		if !found && includeManaged {
			item := skill{Name: asset.Name, Path: asset.Path, Bindings: asset.Bindings}
			if len(asset.Bindings) > 0 {
				item.Host, item.Scope = asset.Bindings[0].Host, asset.Bindings[0].Scope
			}
			result = append(result, item)
		}
	}
	return result
}

func runCompletion(opt options, stdout, stderr io.Writer) int {
	output := stdout
	var script strings.Builder
	if opt.JSON {
		stdout = &script
	}
	shell := "bash"
	if len(opt.Names) == 1 {
		shell = opt.Names[0]
	} else if len(opt.Names) > 1 {
		fmt.Fprintln(stderr, "completion accepts one shell")
		return 2
	}
	words := strings.Join(commands, " ") + " --help --host --scope --path --config --json --json-version --dry-run --project --file --profile --offline --frozen --copy --skill --ref --package"
	switch shell {
	case "bash":
		fmt.Fprintf(stdout, "complete -W '%s' skillctl\n", words)
	case "zsh":
		fmt.Fprintf(stdout, "#compdef skillctl\n_arguments '*:command:(%s)'\n", words)
	case "fish":
		for _, command := range commands {
			fmt.Fprintf(stdout, "complete -c skillctl -f -a %s\n", command)
		}
	case "powershell":
		fmt.Fprintf(stdout, "Register-ArgumentCompleter -Native -CommandName skillctl -ScriptBlock { param($wordToComplete) '%s'.Split(' ') | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_) } }\n", words)
	default:
		fmt.Fprintln(stderr, "supported shells: bash, zsh, fish, powershell")
		return 2
	}
	if opt.JSON {
		return outputResult(commandResult{Command: "completion", Result: map[string]string{"shell": shell, "script": script.String()}}, opt, output, stderr, false)
	}
	return 0
}
