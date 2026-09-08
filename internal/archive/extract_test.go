package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageArchivesRejectUnsafeEntries(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		for _, name := range []string{"../escape", "/absolute", "dir/../../escape", `dir\..\escape`, "C:/absolute", "C:drive-relative", `\\server\share`, "file:stream"} {
			t.Run(format+"/"+name, func(t *testing.T) {
				if err := Unpack(testArtifactEntry(t, format, name), t.TempDir()); err == nil {
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
			if err := Unpack(testArtifactEntry(t, format, "./skill/SKILL.md"), destination); err != nil {
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
