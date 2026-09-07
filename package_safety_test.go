package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageArchivesRejectUnsafeEntries(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		for _, name := range []string{"../escape", "/absolute", "dir/../../escape", `dir\..\escape`, "C:/absolute", "C:drive-relative", `\\server\share`, "file:stream"} {
			t.Run(format+"/"+name, func(t *testing.T) {
				if err := unpackArtifact(testArtifactEntry(t, format, name), t.TempDir()); err == nil {
					t.Fatalf("unsafe archive accepted: %s", name)
				}
			})
		}
	}
}

func TestPackageArchivesExtractRelativeEntries(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			destination := t.TempDir()
			if err := unpackArtifact(testArtifactEntry(t, format, "./skill/SKILL.md"), destination); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "skill", "SKILL.md"))
			if err != nil || string(data) != "payload" {
				t.Fatalf("relative extraction: %q %v", data, err)
			}
		})
	}
}

func testArtifactEntry(t *testing.T, format, name string) []byte {
	t.Helper()
	var data bytes.Buffer
	if format == "zip" {
		archive := zip.NewWriter(&data)
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		compressed := gzip.NewWriter(&data)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 7, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return data.Bytes()
}

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
