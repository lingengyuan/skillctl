package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckWellKnownEstablishesVerifiedBaseline(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	name := "lark-approval"
	body := "current"
	installed := filepath.Join(root, name)
	writeTestSkill(t, installed, name, body)
	archive := makeWellKnownArchive(t, name, body)
	digest := digestWellKnownArchive(archive)

	server, indexRequests := newWellKnownServer(t, map[string][]byte{name: archive})
	lockPath, configPath := writeWellKnownTestConfig(t, home, root, server.URL, map[string]string{name: digest})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("check failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if *indexRequests != 1 || !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("index requests=%d stdout=%q", *indexRequests, stdout.String())
	}
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	canonicalInstalled, err := filepath.EvalSymlinks(installed)
	if err != nil {
		t.Fatal(err)
	}
	baseline, ok := state.findProviderBaseline(canonicalInstalled, wellKnownProviderName)
	if !ok || baseline.Revision != digest {
		t.Fatalf("verified baseline was not saved: %#v", state.ProviderBaselines)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateWellKnownBatchesSkillsBySource(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	names := []string{"lark-approval", "lark-doc"}
	archives := make(map[string][]byte, len(names))
	oldDigests := make(map[string]string, len(names))
	for _, name := range names {
		installed := filepath.Join(root, name)
		writeTestSkill(t, installed, name, "old")
		oldDigests[name] = "sha256:" + strings.Repeat(map[string]string{"lark-approval": "a", "lark-doc": "b"}[name], 64)
		archives[name] = makeWellKnownArchive(t, name, "new")
	}
	server, indexRequests := newWellKnownServer(t, archives)
	lockPath, configPath := writeWellKnownTestConfig(t, home, root, server.URL, oldDigests)
	seedWellKnownBaselines(t, root, oldDigests)

	originalUpdater := runWellKnownUpdater
	t.Cleanup(func() { runWellKnownUpdater = originalUpdater })
	updaterCalls := 0
	runWellKnownUpdater = func(_ context.Context, request wellKnownUpdateRequest, _ io.Writer) (string, error) {
		updaterCalls++
		if request.SourceBaseURL != server.URL || !slices.Equal(request.Names, names) {
			t.Fatalf("unexpected batch request: %#v", request)
		}
		for _, name := range request.Names {
			writeTestSkill(t, filepath.Join(root, name), name, "new")
		}
		updateWellKnownTestLock(t, lockPath, server.URL, archives)
		return "updated", nil
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("update failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if updaterCalls != 1 {
		t.Fatalf("updater called %d times, want one batch", updaterCalls)
	}
	if *indexRequests != 1 {
		t.Fatalf("well-known index fetched %d times, want once", *indexRequests)
	}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		if err != nil || !strings.Contains(string(content), "new") || !strings.Contains(stdout.String(), name+" [") || !strings.Contains(stdout.String(), "updated") {
			t.Fatalf("%s was not verified as updated: err=%v stdout=%q", name, err, stdout.String())
		}
	}
}

func TestUpdateWellKnownKeepsLocalChanges(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	name := "lark-approval"
	installed := filepath.Join(root, name)
	writeTestSkill(t, installed, name, "old")
	oldDigest := "sha256:" + strings.Repeat("a", 64)
	archive := makeWellKnownArchive(t, name, "new")
	server, _ := newWellKnownServer(t, map[string][]byte{name: archive})
	_, configPath := writeWellKnownTestConfig(t, home, root, server.URL, map[string]string{name: oldDigest})
	seedWellKnownBaselines(t, root, map[string]string{name: oldDigest})
	if err := os.WriteFile(filepath.Join(installed, "local.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalUpdater := runWellKnownUpdater
	t.Cleanup(func() { runWellKnownUpdater = originalUpdater })
	runWellKnownUpdater = func(context.Context, wellKnownUpdateRequest, io.Writer) (string, error) {
		t.Fatal("updater called for a locally modified skill")
		return "", nil
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--config", configPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("update failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "local files were modified") {
		t.Fatalf("local modification was not reported: %s", stdout.String())
	}
	if content, err := os.ReadFile(filepath.Join(installed, "local.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("local file was changed: %q, %v", content, err)
	}
}

func TestUpdateWellKnownRollsBackWholeBatch(t *testing.T) {
	home := setTestHome(t)
	root := filepath.Join(home, "skills")
	names := []string{"lark-approval", "lark-doc"}
	archives := make(map[string][]byte, len(names))
	oldDigests := make(map[string]string, len(names))
	for index, name := range names {
		writeTestSkill(t, filepath.Join(root, name), name, "old")
		oldDigests[name] = "sha256:" + strings.Repeat(string(rune('a'+index)), 64)
		archives[name] = makeWellKnownArchive(t, name, "new")
	}
	server, _ := newWellKnownServer(t, archives)
	lockPath, configPath := writeWellKnownTestConfig(t, home, root, server.URL, oldDigests)
	seedWellKnownBaselines(t, root, oldDigests)
	originalLock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	originalUpdater := runWellKnownUpdater
	t.Cleanup(func() { runWellKnownUpdater = originalUpdater })
	runWellKnownUpdater = func(context.Context, wellKnownUpdateRequest, io.Writer) (string, error) {
		for _, name := range names {
			writeTestSkill(t, filepath.Join(root, name), name, "new")
		}
		updateWellKnownTestLock(t, lockPath, server.URL, archives)
		return "", errors.New("provider failed")
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"update", "--config", configPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("update code=%d, want 1: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertFileContent(t, lockPath, originalLock)
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		if err != nil || !strings.Contains(string(content), "old") {
			t.Fatalf("%s was not rolled back: %q, %v", name, content, err)
		}
	}
}

func TestFetchWellKnownIndexFallsBackAndSendsUpdateHeader(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-Skills-Update-Check") != "1" {
			t.Errorf("missing update-check header")
		}
		if r.URL.Path == "/.well-known/agent-skills/index.json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"$schema":%q,"skills":[{"name":"demo","type":"archive","description":"demo","url":"./demo.tar.gz","digest":%q}]}`, wellKnownSchemaV2, digest)
	}))
	t.Cleanup(server.Close)

	remotes, err := fetchWellKnownIndex(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || remotes["demo"].ArtifactURL != server.URL+"/.well-known/skills/demo.tar.gz" {
		t.Fatalf("requests=%d remotes=%#v", requests, remotes)
	}
}

func TestPrepareWellKnownSkillRejectsUnsafeArchive(t *testing.T) {
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("unsafe")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	archive := output.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	_, err := prepareWellKnownSkill(t.Context(), wellKnownRemote{Name: "demo", Type: "archive", ArtifactURL: server.URL, Digest: digestWellKnownArchive(archive)})
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("unsafe archive was accepted: %v", err)
	}
}

func makeWellKnownArchive(t *testing.T, name, body string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte(fmt.Sprintf("---\nname: %s\ndescription: test\n---\n%s\n", name, body))
	if err := tarWriter.WriteHeader(&tar.Header{Name: "SKILL.md", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func digestWellKnownArchive(archive []byte) string {
	digest := sha256.Sum256(archive)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func newWellKnownServer(t *testing.T, archives map[string][]byte) (*httptest.Server, *int) {
	t.Helper()
	indexRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-skills/index.json" {
			indexRequests++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"$schema":"https://schemas.agentskills.io/discovery/0.2.0/schema.json","skills":[`)
			names := make([]string, 0, len(archives))
			for name := range archives {
				names = append(names, name)
			}
			slices.Sort(names)
			for index, name := range names {
				if index > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"name":%q,"type":"archive","description":"test","url":%q,"digest":%q}`, name, "./"+name+".tar.gz", digestWellKnownArchive(archives[name]))
			}
			fmt.Fprint(w, `]}`)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/.well-known/agent-skills/"), ".tar.gz")
		if archive, ok := archives[name]; ok {
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &indexRequests
}

func writeWellKnownTestConfig(t *testing.T, home, root, baseURL string, digests map[string]string) (string, string) {
	t.Helper()
	lockPath := filepath.Join(home, ".agents", ".skill-lock.json")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := vercelLock{Version: 3, Skills: make(map[string]vercelLockEntry, len(digests))}
	for name, digest := range digests {
		lock.Skills[name] = vercelLockEntry{
			Source:          "example.test",
			SourceType:      "well-known",
			SourceURL:       baseURL + "/.well-known/agent-skills/" + name + ".tar.gz",
			SourceBaseURL:   baseURL,
			WellKnownDigest: digest,
		}
	}
	content, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	config := fmt.Sprintf("[[roots]]\npath = %q\nhost = \"test\"\nscope = \"user\"\n[[manifests]]\nkind = \"vercel-skills-lock-v3\"\npath = %q\ninstall_root = %q\n", filepath.ToSlash(root), filepath.ToSlash(lockPath), filepath.ToSlash(root))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return lockPath, configPath
}

func seedWellKnownBaselines(t *testing.T, root string, digests map[string]string) {
	t.Helper()
	state, err := loadTrackedState()
	if err != nil {
		t.Fatal(err)
	}
	for name, digest := range digests {
		path := filepath.Join(root, name)
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := hashDirectory(path)
		if err != nil {
			t.Fatal(err)
		}
		state.putProviderBaseline(providerBaseline{Path: path, Provider: wellKnownProviderName, Revision: digest, InstalledHash: hash})
	}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
}

func updateWellKnownTestLock(t *testing.T, lockPath, baseURL string, archives map[string][]byte) {
	t.Helper()
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock vercelLock
	if err := json.Unmarshal(content, &lock); err != nil {
		t.Fatal(err)
	}
	for name, archive := range archives {
		entry := lock.Skills[name]
		entry.SourceURL = baseURL + "/.well-known/agent-skills/" + name + ".tar.gz"
		entry.WellKnownDigest = digestWellKnownArchive(archive)
		lock.Skills[name] = entry
	}
	content, err = json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
