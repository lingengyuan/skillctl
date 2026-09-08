package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lingengyuan/skillctl/internal/fsutil"
	"github.com/lingengyuan/skillctl/internal/skilldoc"
)

func runConfig(_ context.Context, opt options, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		return outputResult(commandResult{Command: "config", Diagnostics: []diagnostic{{Code: "config_failed", Level: "error", Message: err.Error()}}}, opt, stdout, stderr, true)
	}
	action := "show"
	if len(opt.Names) > 0 {
		action = opt.Names[0]
	}
	if len(opt.Names) > 1 {
		return fail(fmt.Errorf("config accepts one action: show, path, hosts, init, gc"))
	}
	path := opt.ConfigPath
	var err error
	if path == "" {
		path, err = stateFile("config.toml")
		if err != nil {
			return fail(err)
		}
	}
	switch action {
	case "path":
		if opt.JSON {
			return outputResult(commandResult{Command: "config", Result: map[string]string{"path": path}}, opt, stdout, stderr, false)
		}
		fmt.Fprintln(stdout, path)
		return 0
	case "hosts":
		if opt.JSON {
			return outputResult(commandResult{Command: "config", Result: agentHosts()}, opt, stdout, stderr, false)
		}
		for _, host := range agentHosts() {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", host.Name, host.UserPath, host.ProjectPath)
		}
		return 0
	case "show":
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) && opt.ConfigPath == "" {
			data = []byte(defaultConfig)
		} else if err != nil {
			return fail(err)
		}
		if opt.JSON {
			return outputResult(commandResult{Command: "config", Result: map[string]string{"path": path, "toml": string(data)}}, opt, stdout, stderr, false)
		}
		_, err = stdout.Write(data)
		if err != nil {
			return fail(err)
		}
		return 0
	case "init":
		if _, err := os.Lstat(path); err == nil {
			return fail(fmt.Errorf("config already exists: %s", path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		plan := &lifecyclePlan{Command: "config init", Affected: []string{}, Changes: []plannedChange{}}
		if err := addFileIfChanged(plan, path, []byte(defaultConfig)); err != nil {
			return fail(err)
		}
		return executePlan(plan, nil, opt, stdout, stderr)
	case "gc":
		plan, err := garbageCollectionPlan()
		if err != nil {
			return fail(err)
		}
		return executePlan(plan, nil, opt, stdout, stderr)
	default:
		return fail(fmt.Errorf("unknown config action: %s", action))
	}
}

func garbageCollectionPlan() (*lifecyclePlan, error) {
	catalog, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	plan := &lifecyclePlan{Command: "config gc", Affected: []string{}, Changes: []plannedChange{}}
	store, err := stateFile("store")
	if err != nil {
		return nil, err
	}
	ids, err := os.ReadDir(store)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return nil, err
	}
	// Any recorded revision is retained for rollback. Explicitly clearing history
	// is a separate user decision; gc never silently shortens recovery retention.
	history, err := readOperations()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		dir := filepath.Join(store, id.Name())
		revisions, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			path := filepath.Join(dir, revision.Name())
			referenced := false
			for _, pkg := range catalog.Packages {
				if fsutil.SamePath(pkg.Directory, path) && len(pkg.Bindings) > 0 {
					referenced = true
				}
			}
			for _, operation := range history {
				for _, step := range operation.Steps {
					if fsutil.SamePath(step.Path, path) {
						referenced = true
					}
				}
			}
			if referenced {
				continue
			}
			change, err := mutation(path, "remove")
			if err != nil {
				return nil, err
			}
			if err := plan.add(change); err != nil {
				return nil, err
			}
		}
	}
	return plan, nil
}

type searchResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
	Installs    int    `json:"installs,omitempty"`
}

var publicSearchURL = "https://skills.sh/api/search"

func runSearch(ctx context.Context, opt options, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		return outputResult(commandResult{Command: "search", Diagnostics: []diagnostic{{Code: "search_failed", Level: "error", Message: err.Error()}}}, opt, stdout, stderr, true)
	}
	query := strings.ToLower(strings.Join(opt.Names, " "))
	timeout := opt.Timeout
	if timeout == 0 {
		timeout = defaultNetworkTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := []searchResult{}
	if opt.Source != "" {
		source, err := parseSource(opt.Source, "", "")
		if err != nil {
			return fail(err)
		}
		packages, cleanup, err := preparePackages(ctx, source, nil, opt.Offline)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		for _, pkg := range packages {
			doc, err := skilldoc.Read(filepath.Join(pkg.Directory, "SKILL.md"))
			if err != nil {
				return fail(err)
			}
			if query == "" || strings.Contains(strings.ToLower(pkg.Name+" "+doc.Description), query) {
				results = append(results, searchResult{ID: sourceIdentity(pkg.Source), Name: pkg.Name, Source: opt.Source, Description: doc.Description})
			}
		}
	} else if opt.Offline {
		view, err := loadInventory(ctx, opt, false, false)
		if err != nil {
			return fail(err)
		}
		for _, asset := range view.Items {
			if query == "" || strings.Contains(strings.ToLower(asset.Name+" "+asset.Description), query) {
				source := ""
				if asset.Source != nil {
					source = asset.Source.URL
				}
				results = append(results, searchResult{ID: asset.ID, Name: asset.Name, Source: source, Description: asset.Description})
			}
		}
	} else {
		if query == "" {
			return fail(fmt.Errorf("search requires a query or --source"))
		}
		endpoint := publicSearchURL + "?" + url.Values{"q": {query}, "limit": {"20"}}.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fail(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return fail(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fail(fmt.Errorf("public search returned HTTP %d", response.StatusCode))
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
		if err != nil {
			return fail(err)
		}
		if len(body) > 2<<20 {
			return fail(fmt.Errorf("search response exceeds size limit"))
		}
		var parsed struct {
			Skills *[]searchResult `json:"skills"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil || parsed.Skills == nil {
			return fail(fmt.Errorf("unsupported public search response"))
		}
		for _, item := range *parsed.Skills {
			if err := validateSourceURL(item.Source); err == nil {
				results = append(results, item)
			}
		}
	}
	slices.SortFunc(results, func(a, b searchResult) int { return strings.Compare(a.Name, b.Name) })
	if opt.JSON {
		return outputResult(commandResult{Command: "search", Result: results}, opt, stdout, stderr, false)
	}
	for _, item := range results {
		fmt.Fprintf(stdout, "%s\t%s\n", item.Name, item.Source)
	}
	if len(results) == 0 {
		fmt.Fprintln(stdout, "No matching skills found.")
	}
	return 0
}
