//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

// This sanitized workload preserves the observed name/installation cardinality
// and ownership mix without retaining any user history or private source URLs.
func TestIntegrationInspectionContract120Names122Installations(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	installed := filepath.Join(home, ".codex", "skills")
	var sources []string
	for range 8 {
		sources = append(sources, newTestSourceRepository(t))
	}
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 42 {
		name := fmt.Sprintf("known-%02d", i)
		if i < 2 {
			name = fmt.Sprintf("unknown-%02d", i)
		}
		directory := filepath.Join(installed, "tracked", name)
		writeTestSkill(t, directory, name, "original")
		sourceDirectory := filepath.Join(sources[i%8], name)
		if err := fsutil.CopyDirectory(directory, sourceDirectory); err != nil {
			t.Fatal(err)
		}
		digest, _ := fsutil.HashDirectory(directory)
		state.put(trackedEntry{Path: directory, Source: sources[i%8], Ref: "main", SkillPath: name, InstalledHash: digest})
	}
	for _, source := range sources {
		commitTestSource(t, source, "installed revision")
	}
	writeTestSkill(t, filepath.Join(sources[2], "known-02"), "known-02", "upstream changed")
	commitTestSource(t, sources[2], "new upstream")
	writeTestSkill(t, filepath.Join(installed, "tracked", "known-02"), "known-02", "local edits")
	writeTestSkill(t, filepath.Join(installed, "tracked", "known-03"), "known-03", "local edits")
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		name := fmt.Sprintf("unknown-%02d", i)
		writeTestSkill(t, filepath.Join(installed, "unresolved", name), name, "local content")
	}
	plugin := filepath.Join(home, ".codex", "plugins", "cache", "fixture", "bundle", "1.0")
	for i := range 60 {
		name := fmt.Sprintf("host-%02d", i)
		declared := name
		if i < 2 {
			declared = strings.ToUpper(name)
		}
		writeTestSkill(t, filepath.Join(plugin, "skills", name), declared, "host content")
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[plugins.'bundle@fixture']\nenabled=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	record := `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"wildcard","arguments":"{\"cmd\":\"npx skills add https://example.invalid/unrelated.git -g -y\"}"}}` + "\n" + `{"type":"response_item","payload":{"type":"function_call_output","call_id":"wildcard","output":"{\"exit_code\":0}"}}` + "\n"
	if err := os.WriteFile(filepath.Join(history, "install.jsonl"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	oldHost, oldSync := readNativePluginList, syncSourceForSession
	t.Cleanup(func() { readNativePluginList = oldHost; syncSourceForSession = oldSync })
	readNativePluginList = func(context.Context, string) ([]byte, error) {
		return []byte(`{"installed":[{"pluginId":"bundle@fixture","name":"bundle","marketplaceName":"fixture","version":"1.0","installed":true,"enabled":true}]}`), nil
	}
	var mu sync.Mutex
	calls := map[string]int{}
	syncSourceForSession = func(ctx context.Context, source, ref string) (string, error) {
		mu.Lock()
		calls[source]++
		mu.Unlock()
		if strings.Contains(source, "example.invalid") {
			return "", fmt.Errorf("unrelated wildcard must not be fetched")
		}
		return source, nil
	}
	var out, errout strings.Builder
	if code := run([]string{"check", "--json-version", "2"}, &out, &errout); code != 0 {
		t.Fatalf("check=%d %s %s", code, &out, &errout)
	}
	var result commandResult
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.Summary == nil || result.Summary.Names != 120 || result.Summary.Installations != 122 || result.Summary.Untracked != 20 || result.Summary.Modified != 2 || result.Summary.Outdated != 1 {
		t.Fatalf("wrong summary: %+v", result.Summary)
	}
	if len(calls) != 8 {
		t.Fatalf("source fanout: %v", calls)
	}
	for source, count := range calls {
		if count != 1 {
			t.Fatalf("%s fetched %d times", source, count)
		}
	}
	warnings := 0
	for _, d := range result.Diagnostics {
		if d.Code == "skill_portability" {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("portability diagnostics=%d", warnings)
	}
	for _, asset := range result.Items {
		if strings.HasPrefix(asset.Name, "host-") && (asset.Provider != "host-plugin" || asset.Revision != "1.0") {
			t.Fatalf("host identity lost: %+v", asset)
		}
		if asset.Name == "known-02" && (asset.State != "modified" || asset.Upstream.Status != "outdated" || asset.Update.Eligible) {
			t.Fatalf("outdated edits lost: %+v", asset)
		}
		if asset.Name == "known-03" && (asset.State != "modified" || asset.Upstream.Status != "current" || asset.Update.Eligible) {
			t.Fatalf("current edits lost: %+v", asset)
		}
	}
	out.Reset()
	errout.Reset()
	if code := run([]string{"check", "--json"}, &out, &errout); code != 0 {
		t.Fatalf("v1=%d %s", code, &errout)
	}
	var legacy []report
	if err := json.Unmarshal([]byte(out.String()), &legacy); err != nil {
		t.Fatal(err)
	}
	installations := 0
	for _, report := range legacy {
		if len(report.Installations) > 0 {
			installations += len(report.Installations)
		} else {
			installations++
		}
	}
	if len(legacy) != 120 || installations != 122 {
		t.Fatalf("v1 cardinality: names=%d installations=%d", len(legacy), installations)
	}
	out.Reset()
	errout.Reset()
	if code := run([]string{"check"}, &out, &errout); code != 0 {
		t.Fatalf("text=%d %s", code, &errout)
	}
	if !strings.Contains(out.String(), "20 skills have no update source") || !strings.Contains(out.String(), "120 names, 122 installations") {
		t.Fatalf("text summary lost nested installations: %s", &out)
	}
}
