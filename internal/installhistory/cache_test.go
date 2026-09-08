package installhistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryCacheInvalidatesRewritesAppendAndReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "history.jsonl")
	cache := filepath.Join(t.TempDir(), "index.json")
	record := func(name string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npx skills add owner/repo --skill ` + name + `"}}]}}` + "\n"
	}
	if err := os.WriteFile(path, []byte(record("first")), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if historyFileStamp(info) == "" {
		t.Skip("platform does not expose a change-time identity")
	}
	_, first, err := ReadRootsCached(t.Context(), []string{root}, cache)
	if err != nil || first.FilesScanned != 1 {
		t.Fatalf("cold: %+v %v", first, err)
	}
	got, warm, err := ReadRootsCached(t.Context(), []string{root}, cache)
	if err != nil || warm.FilesReused != 1 || warm.BytesRead != 0 || len(got["first"]) != 1 {
		t.Fatalf("warm: %+v %v %v", warm, got, err)
	}
	if err := os.WriteFile(path, []byte(record("other")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, changed, err := ReadRootsCached(t.Context(), []string{root}, cache)
	if err != nil || changed.FilesScanned != 1 || len(got["other"]) != 1 || len(got["first"]) != 0 {
		t.Fatalf("same-size rewrite reused old evidence: %+v %v %v", changed, got, err)
	}
	if err := os.WriteFile(path, []byte(record("other")+record("added")), 0600); err != nil {
		t.Fatal(err)
	}
	got, changed, err = ReadRootsCached(t.Context(), []string{root}, cache)
	if err != nil || changed.FilesScanned != 1 || len(got["added"]) != 1 {
		t.Fatalf("append: %+v %v %v", changed, got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(record("fresh")), 0600); err != nil {
		t.Fatal(err)
	}
	got, changed, err = ReadRootsCached(t.Context(), []string{root}, cache)
	if err != nil || changed.FilesScanned != 1 || len(got) != 1 || len(got["fresh"]) != 1 {
		t.Fatalf("replacement: %+v %v %v", changed, got, err)
	}
}

func TestHistoryCacheDoesNotCopyConversationText(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "index.json")
	secret := "private conversation must not enter derived cache"
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(`{"type":"user","message":{"content":"`+secret+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRootsCached(t.Context(), []string{root}, cache); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("cache retained conversation body")
	}
	for _, source := range []string{"https://user:password@example.com/a.git", "https://example.com/a.git?token=secret"} {
		if cacheSafeSource(source) {
			t.Fatal("credential source can enter index")
		}
	}
}
