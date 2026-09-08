package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenSyncRejectsTamperedArtifact(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "original")
	runV2(t, config, "install", source, "--host", "claude", "--copy")
	file := filepath.Join(home, "export", "skillctl.toml")
	runV2(t, config, "export", "demo", "--output", file)
	manifest, _, err := loadEnvironment(file)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(filepath.Dir(file), manifest.Skills[0].Source)
	writeTestSkill(t, artifact, "demo", "tampered")
	t.Setenv("SKILLCTL_HOME", filepath.Join(home, "clean-state"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sync", "--frozen", "--offline", "--file", file, "--config", config, "--json-version", "2"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "mismatch") {
		t.Fatalf("tampered artifact accepted: %d %s %s", code, &stdout, &stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), ".claude", "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("tampered artifact installed: %v", err)
	}
}

func TestCredentialsNeverEnterSharedConfigOrOutput(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	for _, source := range []string{"https://user:secret-value@example.invalid/repo.git", "https://example.invalid/a.zip?token=secret-value", "git+https://user:secret-value@example.invalid/repo.git"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"install", source, "--host", "codex", "--config", config, "--json-version", "2"}, &stdout, &stderr); code == 0 {
			t.Fatal("credentials accepted")
		}
		if strings.Contains(stdout.String()+stderr.String(), "secret-value") {
			t.Fatalf("credential exposed: %s %s", &stdout, &stderr)
		}
	}
	manifest := filepath.Join(home, "skillctl.toml")
	data := "version=1\n[[skills]]\nname='demo'\nsource='https://user:secret-value@example.invalid/repo.git'\nhosts=['codex']\n"
	if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadEnvironment(manifest); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("manifest credential handling: %v", err)
	}
}

func TestWellKnownInstallVerifiesDigest(t *testing.T) {
	setTestHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "index.json") {
			fmt.Fprintf(w, `{"$schema":%q,"skills":[{"name":"demo","type":"skill-md","url":"/SKILL.md","digest":%q}]}`, wellKnownSchemaV2, "sha256:"+strings.Repeat("a", 64))
			return
		}
		fmt.Fprint(w, "---\nname: demo\ndescription: test\n---\nuntrusted\n")
	}))
	defer server.Close()
	if _, cleanup, err := preparePackages(context.Background(), sourceSpec{Kind: "well-known", URL: server.URL}, nil, false); err == nil {
		cleanup()
		t.Fatal("invalid advertised digest accepted")
	}
}

func TestSearchUsesExplicitSourceAndAPIShape(t *testing.T) {
	home, _, config := lifecycleFixture(t)
	source := filepath.Join(home, "source")
	writeTestSkill(t, source, "demo", "search")
	result := runV2(t, config, "search", "demo", "--source", source, "--offline")
	if result.Result == nil {
		t.Fatal("search returned no result")
	}
	old := publicSearchURL
	t.Cleanup(func() { publicSearchURL = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "demo" {
			t.Error("wrong query")
		}
		fmt.Fprint(w, `{"skills":[{"id":"owner/repo/demo","name":"demo","source":"owner/repo","installs":12}]}`)
	}))
	defer server.Close()
	publicSearchURL = server.URL
	runV2(t, config, "search", "demo")
}
